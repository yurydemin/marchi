package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/msgraph"
)

type msgraphTestEnv struct {
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

func newMSGraphTestEnv(t *testing.T) *msgraphTestEnv {
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
		Email: "user@outlook.com", IsActive: true,
		OAuth2Provider: domain.OAuth2ProviderMicrosoft,
		ConnectorType:  domain.ConnectorMSGraph,
	})
	if err != nil {
		t.Fatalf("creating account fixture: %v", err)
	}

	return &msgraphTestEnv{
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

// fakeMSGraphMessage is one message the fakeMSGraphServer knows about.
type fakeMSGraphMessage struct {
	id      string
	isRead  bool
	flagged bool
	raw     []byte
	deleted bool
	marked  bool
}

// fakeMSGraphFolder is one mail folder the fake server exposes, with its
// own delta state and message set — mirroring how Graph's delta query is
// scoped per folder (see msgraph_sync.go's doc comment).
type fakeMSGraphFolder struct {
	id       string
	name     string
	children []string // child folder ids, for recursive listing tests
	// deltaAdded is the set of message ids the *next* delta call for this
	// folder should report as added — tests populate/mutate this between
	// sync runs to control exactly what an incremental sync sees.
	deltaAdded []string
	deltaToken int // bumped each time a delta response is issued, to produce a fresh deltaLink
}

// fakeMSGraphServer emulates just enough of the Microsoft Graph REST API
// (mailFolders/childFolders/messages/delta/$value/modify/delete) for
// SyncAccountMSGraph's tests — no real Microsoft tenant involved,
// matching this project's established OAuth2-mechanics-only testing
// policy (see internal/gmailapi's and internal/oauth2's own tests).
type fakeMSGraphServer struct {
	topFolders []string // top-level folder ids, in listing order
	folders    map[string]*fakeMSGraphFolder
	messages   map[string]*fakeMSGraphMessage
}

func newFakeMSGraphServer() *fakeMSGraphServer {
	return &fakeMSGraphServer{folders: map[string]*fakeMSGraphFolder{}, messages: map[string]*fakeMSGraphMessage{}}
}

func (s *fakeMSGraphServer) addFolder(id, name string) *fakeMSGraphFolder {
	f := &fakeMSGraphFolder{id: id, name: name}
	s.folders[id] = f
	s.topFolders = append(s.topFolders, id)
	return f
}

func (s *fakeMSGraphServer) addChildFolder(parentID, id, name string) *fakeMSGraphFolder {
	f := &fakeMSGraphFolder{id: id, name: name}
	s.folders[id] = f
	s.folders[parentID].children = append(s.folders[parentID].children, id)
	return f
}

func (s *fakeMSGraphServer) addMessage(id string, isRead bool, raw []byte) *fakeMSGraphMessage {
	m := &fakeMSGraphMessage{id: id, isRead: isRead, raw: raw}
	s.messages[id] = m
	return m
}

func (s *fakeMSGraphServer) start(t *testing.T) *msgraph.Client {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/me/mailFolders", func(w http.ResponseWriter, r *http.Request) {
		var list []msgraph.MailFolder
		for _, id := range s.topFolders {
			f := s.folders[id]
			list = append(list, msgraph.MailFolder{ID: f.id, DisplayName: f.name, ChildFolderCount: len(f.children)})
		}
		json.NewEncoder(w).Encode(map[string]any{"value": list})
	})

	mux.HandleFunc("/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		rest := r.URL.Path[len("/me/messages/"):]
		switch {
		case strings.HasSuffix(rest, "/$value"):
			id := strings.TrimSuffix(rest, "/$value")
			m, ok := s.messages[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(m.raw)
		default:
			id := rest
			m, ok := s.messages[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			switch r.Method {
			case http.MethodPatch:
				m.marked = true
				m.isRead = true
				w.Write([]byte("{}"))
			case http.MethodDelete:
				m.deleted = true
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		}
	})

	mux.HandleFunc("/me/mailFolders/", func(w http.ResponseWriter, r *http.Request) {
		rest := r.URL.Path[len("/me/mailFolders/"):]
		switch {
		case strings.HasSuffix(rest, "/childFolders"):
			id := strings.TrimSuffix(rest, "/childFolders")
			f, ok := s.folders[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var list []msgraph.MailFolder
			for _, childID := range f.children {
				cf := s.folders[childID]
				list = append(list, msgraph.MailFolder{ID: cf.id, DisplayName: cf.name, ChildFolderCount: len(cf.children)})
			}
			json.NewEncoder(w).Encode(map[string]any{"value": list})

		case strings.Contains(rest, "/messages/delta"):
			id := rest[:strings.Index(rest, "/messages/delta")]
			f, ok := s.folders[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.deltaToken++
			var value []msgraph.MessageStub
			for _, msgID := range f.deltaAdded {
				m := s.messages[msgID]
				stub := msgraph.MessageStub{ID: msgID}
				if m != nil {
					stub.IsRead = m.isRead
				}
				value = append(value, stub)
			}
			f.deltaAdded = nil // consumed — next call (without new adds) reports nothing new
			resp := map[string]any{
				"value":            value,
				"@odata.deltaLink": fmt.Sprintf("http://%s/me/mailFolders/%s/messages/delta?$deltatoken=%d", r.Host, id, f.deltaToken),
			}
			json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &msgraph.Client{BaseURL: srv.URL, AccessToken: "test-token"}
}

func msgraphTestMessage(subject string) []byte {
	return testEmail(subject) // reuses fetch_test.go's fixture builder — same package
}

func TestSyncAccountMSGraph_FullSync_ArchivesEveryMessageAcrossFolders(t *testing.T) {
	env := newMSGraphTestEnv(t)
	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1", "m2"}
	srv.addMessage("m1", true, msgraphTestMessage("inbox-one"))
	srv.addMessage("m2", false, msgraphTestMessage("inbox-two"))

	sent := srv.addFolder("f-sent", "Sent Items")
	sent.deltaAdded = []string{"m3"}
	srv.addMessage("m3", true, msgraphTestMessage("sent-one"))

	client := srv.start(t)
	a, err := env.accountsR.GetByID(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	results, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccountMSGraph: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d folder results, want 2", len(results))
	}
	total := 0
	for _, r := range results {
		total += r.Fetched
	}
	if total != 3 {
		t.Errorf("archived %d messages total, want 3", total)
	}

	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 3 {
		t.Fatalf("archived %d emails, want 3", len(emails))
	}

	// Each folder's own delta cursor must have been persisted.
	inboxFolder, err := env.foldersR.GetByID(context.Background(), findFolderResult(t, results, "Inbox").Folder.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if inboxFolder.MSGraphDeltaLink == "" {
		t.Error("Inbox folder's MSGraphDeltaLink was not persisted")
	}
}

func findFolderResult(t *testing.T, results []FolderResult, name string) FolderResult {
	t.Helper()
	for _, r := range results {
		if r.Folder != nil && r.Folder.FolderName == name {
			return r
		}
	}
	t.Fatalf("no folder result named %q among %+v", name, results)
	return FolderResult{}
}

func TestSyncAccountMSGraph_NestedFolders_FlattenedIntoParentChildName(t *testing.T) {
	env := newMSGraphTestEnv(t)
	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	child := srv.addChildFolder("f-inbox", "f-projects", "Projects")
	child.deltaAdded = []string{"m1"}
	srv.addMessage("m1", false, msgraphTestMessage("nested"))
	_ = inbox

	client := srv.start(t)
	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)

	results, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccountMSGraph: %v", err)
	}

	found := false
	for _, r := range results {
		if r.Folder != nil && r.Folder.FolderName == "Inbox/Projects" {
			found = true
			if r.Fetched != 1 {
				t.Errorf("Inbox/Projects Fetched = %d, want 1", r.Fetched)
			}
		}
	}
	if !found {
		t.Fatalf("expected a folder named Inbox/Projects among results: %+v", results)
	}
}

func TestSyncAccountMSGraph_IncrementalSync_OnlyProcessesDeltaAdditions(t *testing.T) {
	env := newMSGraphTestEnv(t)
	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1"}
	srv.addMessage("m1", false, msgraphTestMessage("first-run"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Second run: only a genuinely new message is reported via delta.
	inbox.deltaAdded = []string{"m2"}
	srv.addMessage("m2", false, msgraphTestMessage("second-run"))
	a, _ = env.accountsR.GetByID(context.Background(), env.accountID)
	results, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if len(results) != 1 || results[0].Fetched != 1 {
		t.Fatalf("second sync results = %+v, want exactly 1 fetched", results)
	}

	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 2 {
		t.Fatalf("archived %d emails total, want 2", len(emails))
	}
}

func TestSyncAccountMSGraph_DuplicateMessageIDSkippedNotRearchived(t *testing.T) {
	env := newMSGraphTestEnv(t)
	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1"}
	srv.addMessage("m1", false, msgraphTestMessage("dup"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Second run reports the same message id again (e.g. a metadata-only
	// change bringing it back into the delta) — must be recognized as
	// already archived, not re-archived.
	inbox.deltaAdded = []string{"m1"}
	a, _ = env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
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

func TestSyncAccountMSGraph_ReadFlagDerivedFromIsRead(t *testing.T) {
	env := newMSGraphTestEnv(t)
	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1", "m2"}
	srv.addMessage("m1", true, msgraphTestMessage("read-one"))
	srv.addMessage("m2", false, msgraphTestMessage("unread-one"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("SyncAccountMSGraph: %v", err)
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

func TestSyncAccountMSGraph_ArchiveAndDeleteRuleDeletesMessage(t *testing.T) {
	env := newMSGraphTestEnv(t)
	if _, err := env.rulesR.Create(context.Background(), &domain.Rule{
		Name: "trash junk", Priority: 1, IsActive: true, Action: domain.ActionArchiveAndDelete,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "junk"},
	}); err != nil {
		t.Fatalf("creating rule: %v", err)
	}

	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1"}
	srv.addMessage("m1", false, msgraphTestMessage("junk mail"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("SyncAccountMSGraph: %v", err)
	}

	if !srv.messages["m1"].deleted {
		t.Error("expected m1 to have been deleted via the Graph API")
	}
	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("archived %d emails, want 1 (archive_and_delete still archives)", len(emails))
	}
}

func TestSyncAccountMSGraph_ArchiveAndMarkReadRuleSetsIsRead(t *testing.T) {
	env := newMSGraphTestEnv(t)
	if _, err := env.rulesR.Create(context.Background(), &domain.Rule{
		Name: "mark newsletters read", Priority: 1, IsActive: true, Action: domain.ActionArchiveAndMarkRead,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "newsletter"},
	}); err != nil {
		t.Fatalf("creating rule: %v", err)
	}

	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1"}
	srv.addMessage("m1", false, msgraphTestMessage("newsletter digest"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	if _, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil); err != nil {
		t.Fatalf("SyncAccountMSGraph: %v", err)
	}

	if !srv.messages["m1"].marked {
		t.Error("expected m1 to have been marked read via the Graph API")
	}
}

func TestSyncAccountMSGraph_SkipRuleArchivesNothing(t *testing.T) {
	env := newMSGraphTestEnv(t)
	if _, err := env.rulesR.Create(context.Background(), &domain.Rule{
		Name: "skip promo", Priority: 1, IsActive: true, Action: domain.ActionSkip,
		Conditions: domain.RuleNode{Type: domain.ConditionSubjectContains, Value: "promo"},
	}); err != nil {
		t.Fatalf("creating rule: %v", err)
	}

	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1"}
	srv.addMessage("m1", false, msgraphTestMessage("promo blast"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	results, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("SyncAccountMSGraph: %v", err)
	}
	total := 0
	for _, r := range results {
		total += r.Fetched
	}
	if total != 0 {
		t.Errorf("Fetched total = %d, want 0", total)
	}
	emails, err := env.emailsR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(emails) != 0 {
		t.Errorf("archived %d emails, want 0", len(emails))
	}
}

func TestSyncAccountMSGraph_FetchErrorLeavesCursorUnpersistedForRetry(t *testing.T) {
	env := newMSGraphTestEnv(t)
	srv := newFakeMSGraphServer()
	inbox := srv.addFolder("f-inbox", "Inbox")
	inbox.deltaAdded = []string{"m1", "m2"} // m2 is never added to srv.messages -> GetMessageRaw 404s
	srv.addMessage("m1", false, msgraphTestMessage("ok"))
	client := srv.start(t)

	a, _ := env.accountsR.GetByID(context.Background(), env.accountID)
	_, err := SyncAccountMSGraph(context.Background(), a, client, env.maildirRoot, "test-host",
		env.w, env.foldersR, env.emailsR, env.attachmentsR, env.syncLogsR, env.rulesR, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error from the missing message")
	}

	folders, err := env.foldersR.ListByAccount(context.Background(), env.accountID)
	if err != nil {
		t.Fatalf("ListByAccount (folders): %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("got %d folders, want 1", len(folders))
	}
	if folders[0].MSGraphDeltaLink != "" {
		t.Errorf("MSGraphDeltaLink = %q, want empty (cursor must not advance past a failed batch)", folders[0].MSGraphDeltaLink)
	}
}
