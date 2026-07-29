package sync

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/gmailapi"
)

type gmailTestEnv struct {
	sqlDB        *sql.DB
	w            writer.Writer
	accountsR    *repo.AccountsRepo
	foldersR     *repo.FoldersRepo
	emailsR      *repo.EmailsRepo
	attachmentsR *repo.AttachmentsRepo
	syncLogsR    *repo.SyncLogsRepo
	rulesR       *repo.RulesRepo
	accountID    int64
	maildirRoot  string
}

func newGmailTestEnv(t *testing.T) *gmailTestEnv {
	t.Helper()
	dataDir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dataDir, "marchi.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	w := writer.New(sqlDB)
	t.Cleanup(func() { w.Close() })

	accountsR := repo.NewAccountsRepo(sqlDB, w)
	accountID, err := accountsR.Create(context.Background(), &domain.Account{
		Email: "user@gmail.com", IsActive: true,
		OAuth2Provider: domain.OAuth2ProviderGoogle,
		ConnectorType:  domain.ConnectorGmailAPI,
	})
	if err != nil {
		t.Fatalf("creating account fixture: %v", err)
	}

	return &gmailTestEnv{
		sqlDB:        sqlDB,
		w:            w,
		accountsR:    accountsR,
		foldersR:     repo.NewFoldersRepo(sqlDB, w),
		emailsR:      repo.NewEmailsRepo(sqlDB, w),
		attachmentsR: repo.NewAttachmentsRepo(sqlDB, w),
		syncLogsR:    repo.NewSyncLogsRepo(sqlDB, w),
		rulesR:       repo.NewRulesRepo(sqlDB, w),
		accountID:    accountID,
		maildirRoot:  filepath.Join(dataDir, "maildir"),
	}
}

// fakeGmailMessage is one message the fakeGmailServer knows about.
type fakeGmailMessage struct {
	id       string
	labels   []string
	raw      []byte
	trashed  bool
	modified struct {
		add, remove []string
	}
}

// fakeGmailServer emulates just enough of the Gmail REST API
// (profile/messages.list/messages.get/history.list/modify/trash) for
// SyncAccountGmailAPI's tests — no real Google account or SDK involved,
// matching this project's established OAuth2-mechanics-only testing
// policy (internal/oauth2's own test file does the same against a fake
// token endpoint).
type fakeGmailServer struct {
	historyID string // current mailbox historyId, returned by GetProfile
	messages  map[string]*fakeGmailMessage
	order     []string // insertion order, for a stable messages.list

	// history, if set, is what history.list should report for any
	// startHistoryId — tests populate this to control exactly what an
	// incremental sync sees, independent of the (unused by ListHistory)
	// full messages list above.
	historyAdded []string // message IDs history.list reports as messagesAdded
	historyErr   int      // non-zero: respond to /history with this HTTP status
}

func newFakeGmailServer() *fakeGmailServer {
	return &fakeGmailServer{historyID: "1000", messages: map[string]*fakeGmailMessage{}}
}

func (s *fakeGmailServer) addMessage(id string, labels []string, raw []byte) {
	s.messages[id] = &fakeGmailMessage{id: id, labels: labels, raw: raw}
	s.order = append(s.order, id)
}

func (s *fakeGmailServer) start(t *testing.T) *gmailapi.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gmailapi.Profile{EmailAddress: "user@gmail.com", HistoryID: s.historyID})
	})
	mux.HandleFunc("/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		list := gmailapi.MessageList{}
		for _, id := range s.order {
			list.Messages = append(list.Messages, gmailapi.MessageRef{ID: id})
		}
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("/users/me/history", func(w http.ResponseWriter, r *http.Request) {
		if s.historyErr != 0 {
			w.WriteHeader(s.historyErr)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": s.historyErr, "message": "not found"}})
			return
		}
		list := gmailapi.HistoryList{HistoryID: s.historyID}
		if len(s.historyAdded) > 0 {
			rec := gmailapi.HistoryRecord{}
			for _, id := range s.historyAdded {
				rec.MessagesAdded = append(rec.MessagesAdded, struct {
					Message gmailapi.MessageRef `json:"message"`
				}{Message: gmailapi.MessageRef{ID: id}})
			}
			list.History = []gmailapi.HistoryRecord{rec}
		}
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("/users/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		// path shapes: /users/me/messages/{id} (GET), /{id}/modify (POST), /{id}/trash (POST)
		rest := r.URL.Path[len("/users/me/messages/"):]
		switch {
		case len(rest) > len("/modify") && rest[len(rest)-len("/modify"):] == "/modify":
			id := rest[:len(rest)-len("/modify")]
			msg, ok := s.messages[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var body struct {
				AddLabelIDs    []string `json:"addLabelIds"`
				RemoveLabelIDs []string `json:"removeLabelIds"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			msg.modified.add = body.AddLabelIDs
			msg.modified.remove = body.RemoveLabelIDs
			w.Write([]byte("{}"))
		case len(rest) > len("/trash") && rest[len(rest)-len("/trash"):] == "/trash":
			id := rest[:len(rest)-len("/trash")]
			msg, ok := s.messages[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			msg.trashed = true
			w.Write([]byte("{}"))
		default:
			id := rest
			msg, ok := s.messages[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(gmailapi.Message{
				ID: msg.id, LabelIDs: msg.labels,
				Raw: base64.RawURLEncoding.EncodeToString(msg.raw),
			})
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &gmailapi.Client{BaseURL: srv.URL, AccessToken: "test-token"}
}

func gmailTestMessage(subject string, labels ...string) []byte {
	return testEmail(subject) // reuses fetch_test.go's fixture builder — same package
}

func TestSyncAccountGmailAPI_FullSync_ArchivesEveryMessageAndPersistsCursor(t *testing.T) {
	env := newGmailTestEnv(t)
	srv := newFakeGmailServer()
	srv.historyID = "5000"
	srv.addMessage("m1", []string{"INBOX"}, gmailTestMessage("first"))
	srv.addMessage("m2", []string{"INBOX", "UNREAD"}, gmailTestMessage("second"))
	client := srv.start(t)

	a, err := env.accountsR.GetByID(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	result, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccountGmailAPI: %v", err)
	}
	if result.Fetched != 2 {
		t.Errorf("Fetched = %d, want 2", result.Fetched)
	}
	if result.Folder.FolderName != gmailAllMailFolder {
		t.Errorf("Folder = %q, want %q", result.Folder.FolderName, gmailAllMailFolder)
	}

	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 2 {
		t.Fatalf("archived %d emails, want 2", len(emails))
	}

	a, err = env.accountsR.GetByID(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("GetByID after sync: %v", err)
	}
	if a.GmailHistoryID != "5000" {
		t.Errorf("GmailHistoryID = %q, want 5000 (persisted from GetProfile)", a.GmailHistoryID)
	}
}

func TestSyncAccountGmailAPI_ReadFlagDerivedFromUnreadLabel(t *testing.T) {
	env := newGmailTestEnv(t)
	srv := newFakeGmailServer()
	srv.addMessage("m1", []string{"INBOX"}, gmailTestMessage("read-one")) // no UNREAD label -> \Seen
	srv.addMessage("m2", []string{"INBOX", "UNREAD"}, gmailTestMessage("unread-one"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("SyncAccountGmailAPI: %v", err)
	}

	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	byMsgID := map[string]*domain.Email{}
	for _, e := range emails {
		byMsgID[e.MessageID] = e
	}
	readOne := byMsgID["read-one@example.com"]
	unreadOne := byMsgID["unread-one@example.com"]
	if readOne == nil || unreadOne == nil {
		t.Fatalf("missing expected messages: %+v", byMsgID)
	}
	if !hasFlag(readOne.Flags, `\Seen`) {
		t.Errorf("read-one Flags = %v, want \\Seen", readOne.Flags)
	}
	if hasFlag(unreadOne.Flags, `\Seen`) {
		t.Errorf("unread-one Flags = %v, want no \\Seen", unreadOne.Flags)
	}
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func TestSyncAccountGmailAPI_IncrementalSync_OnlyProcessesHistoryDelta(t *testing.T) {
	env := newGmailTestEnv(t)

	// Seed the account as if a previous full sync already ran and left a
	// cursor behind.
	if err := env.accountsR.UpdateGmailHistoryID(context.Background(), env.accountID, "1000"); err != nil {
		t.Fatalf("UpdateGmailHistoryID: %v", err)
	}

	srv := newFakeGmailServer()
	srv.historyID = "1200"
	// m_old exists on the server (would show up in a full ListMessages
	// call) but is NOT part of the history delta below — a correctly
	// incremental sync must not touch it at all.
	srv.addMessage("m_old", nil, gmailTestMessage("old"))
	srv.addMessage("m_new", nil, gmailTestMessage("new"))
	srv.historyAdded = []string{"m_new"}
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	result, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccountGmailAPI: %v", err)
	}
	if result.Fetched != 1 {
		t.Fatalf("Fetched = %d, want 1 (only the history delta)", result.Fetched)
	}

	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 1 || emails[0].MessageID != "new@example.com" {
		t.Fatalf("archived = %+v, want just new@example.com", emails)
	}

	a, _ = env.accountsR.GetByID(context.Background(), env.accountID)
	if a.GmailHistoryID != "1200" {
		t.Errorf("GmailHistoryID = %q, want 1200", a.GmailHistoryID)
	}
}

func TestSyncAccountGmailAPI_ExpiredHistoryFallsBackToFullResync(t *testing.T) {
	env := newGmailTestEnv(t)
	if err := env.accountsR.UpdateGmailHistoryID(context.Background(), env.accountID, "stale-cursor"); err != nil {
		t.Fatalf("UpdateGmailHistoryID: %v", err)
	}

	srv := newFakeGmailServer()
	srv.historyID = "9000"
	srv.historyErr = http.StatusNotFound // simulates Gmail's "history id too old"
	srv.addMessage("m1", nil, gmailTestMessage("one"))
	srv.addMessage("m2", nil, gmailTestMessage("two"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	result, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccountGmailAPI: %v", err)
	}
	if result.Fetched != 2 {
		t.Fatalf("Fetched = %d, want 2 (fell back to full resync)", result.Fetched)
	}
	a, _ = env.accountsR.GetByID(context.Background(), env.accountID)
	if a.GmailHistoryID != "9000" {
		t.Errorf("GmailHistoryID = %q, want 9000", a.GmailHistoryID)
	}
}

func TestSyncAccountGmailAPI_DuplicateMessageIDSkippedNotRearchived(t *testing.T) {
	env := newGmailTestEnv(t)
	srv := newFakeGmailServer()
	srv.addMessage("m1", nil, gmailTestMessage("dup"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Second run: history delta re-reports the SAME message (e.g. a
	// label-only change bringing it back into messagesAdded from
	// Gmail's perspective) — must be recognized as already archived.
	a, _ = env.accountsR.GetByID(context.Background(), env.accountID)
	srv.historyID = "2000"
	srv.historyAdded = []string{"m1"}
	if _, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("archived %d emails, want 1 (no re-archive of a duplicate)", len(emails))
	}
}

func TestSyncAccountGmailAPI_FetchErrorLeavesCursorUnpersistedForRetry(t *testing.T) {
	env := newGmailTestEnv(t)
	srv := newFakeGmailServer()
	srv.historyID = "7000"
	srv.addMessage("m1", nil, gmailTestMessage("ok"))
	// m2 is listed but never added to srv.messages -> GetMessageRaw 404s.
	srv.order = append(srv.order, "m2")
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	_, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error from the missing message")
	}

	a, _ = env.accountsR.GetByID(context.Background(), env.accountID)
	if a.GmailHistoryID != "" {
		t.Errorf("GmailHistoryID = %q, want empty (cursor must not advance past a failed batch)", a.GmailHistoryID)
	}
}

func TestSyncAccountGmailAPI_ArchiveAndDeleteRuleTrashesMessage(t *testing.T) {
	env := newGmailTestEnv(t)
	if _, err := env.rulesR.Create(context.Background(), &domain.Rule{
		Name: "trash junk", Priority: 1, IsActive: true, Action: domain.ActionArchiveAndDelete,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "junk"},
	}); err != nil {
		t.Fatalf("creating rule: %v", err)
	}

	srv := newFakeGmailServer()
	srv.addMessage("m1", nil, gmailTestMessage("junk mail"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("SyncAccountGmailAPI: %v", err)
	}

	if !srv.messages["m1"].trashed {
		t.Error("expected m1 to have been trashed via the Gmail API")
	}
	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("archived %d emails, want 1 (archive_and_delete still archives)", len(emails))
	}
}

func TestSyncAccountGmailAPI_ArchiveAndMarkReadRuleRemovesUnreadLabel(t *testing.T) {
	env := newGmailTestEnv(t)
	if _, err := env.rulesR.Create(context.Background(), &domain.Rule{
		Name: "mark newsletters read", Priority: 1, IsActive: true, Action: domain.ActionArchiveAndMarkRead,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "newsletter"},
	}); err != nil {
		t.Fatalf("creating rule: %v", err)
	}

	srv := newFakeGmailServer()
	srv.addMessage("m1", []string{"INBOX", "UNREAD"}, gmailTestMessage("newsletter digest"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("SyncAccountGmailAPI: %v", err)
	}

	got := srv.messages["m1"].modified.remove
	if len(got) != 1 || got[0] != "UNREAD" {
		t.Errorf("modify removeLabelIds = %v, want [UNREAD]", got)
	}
	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("archived %d emails, want 1", len(emails))
	}
}

func TestSyncAccountGmailAPI_SkipRuleArchivesNothing(t *testing.T) {
	env := newGmailTestEnv(t)
	if _, err := env.rulesR.Create(context.Background(), &domain.Rule{
		Name: "skip promo", Priority: 1, IsActive: true, Action: domain.ActionSkip,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "promo"},
	}); err != nil {
		t.Fatalf("creating rule: %v", err)
	}

	srv := newFakeGmailServer()
	srv.addMessage("m1", nil, gmailTestMessage("promo blast"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	result, err := SyncAccountGmailAPI(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.accountsR, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccountGmailAPI: %v", err)
	}
	if result.Fetched != 0 {
		t.Errorf("Fetched = %d, want 0", result.Fetched)
	}
	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 0 {
		t.Errorf("archived %d emails, want 0", len(emails))
	}
}
