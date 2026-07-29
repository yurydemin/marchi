package gmailapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, AccessToken: "test-token"}
}

func TestClient_GetProfile(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q", got)
		}
		if r.URL.Path != "/users/me/profile" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(Profile{EmailAddress: "user@gmail.com", HistoryID: "1000"})
	})

	p, err := c.GetProfile(context.Background())
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if p.EmailAddress != "user@gmail.com" || p.HistoryID != "1000" {
		t.Errorf("GetProfile = %+v", p)
	}
}

func TestClient_ListMessages_Pagination(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("pageToken") == "" {
			json.NewEncoder(w).Encode(MessageList{
				Messages:      []MessageRef{{ID: "m1"}, {ID: "m2"}},
				NextPageToken: "page2",
			})
			return
		}
		if r.URL.Query().Get("pageToken") != "page2" {
			t.Errorf("pageToken = %q, want page2", r.URL.Query().Get("pageToken"))
		}
		json.NewEncoder(w).Encode(MessageList{Messages: []MessageRef{{ID: "m3"}}})
	})

	first, err := c.ListMessages(context.Background(), "")
	if err != nil {
		t.Fatalf("ListMessages (page 1): %v", err)
	}
	if len(first.Messages) != 2 || first.NextPageToken != "page2" {
		t.Fatalf("page 1 = %+v", first)
	}
	second, err := c.ListMessages(context.Background(), first.NextPageToken)
	if err != nil {
		t.Fatalf("ListMessages (page 2): %v", err)
	}
	if len(second.Messages) != 1 || second.NextPageToken != "" {
		t.Fatalf("page 2 = %+v", second)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestClient_GetMessageRaw_DecodesBase64URL(t *testing.T) {
	rawRFC822 := "From: a@example.com\r\nSubject: hi\r\n\r\nbody"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "raw" {
			t.Errorf("format = %q, want raw", r.URL.Query().Get("format"))
		}
		json.NewEncoder(w).Encode(Message{
			ID:       "m1",
			LabelIDs: []string{"INBOX", "UNREAD"},
			Raw:      base64.RawURLEncoding.EncodeToString([]byte(rawRFC822)),
		})
	})

	msg, err := c.GetMessageRaw(context.Background(), "m1")
	if err != nil {
		t.Fatalf("GetMessageRaw: %v", err)
	}
	got, err := msg.RawBytes()
	if err != nil {
		t.Fatalf("RawBytes: %v", err)
	}
	if string(got) != rawRFC822 {
		t.Errorf("RawBytes = %q, want %q", got, rawRFC822)
	}
	if len(msg.LabelIDs) != 2 {
		t.Errorf("LabelIDs = %v", msg.LabelIDs)
	}
}

func TestClient_GetMessageRaw_DecodesPaddedBase64URL(t *testing.T) {
	rawRFC822 := "From: a@example.com\r\nSubject: hi there\r\n\r\nbody"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Message{Raw: base64.URLEncoding.EncodeToString([]byte(rawRFC822))})
	})

	msg, err := c.GetMessageRaw(context.Background(), "m1")
	if err != nil {
		t.Fatalf("GetMessageRaw: %v", err)
	}
	got, err := msg.RawBytes()
	if err != nil {
		t.Fatalf("RawBytes: %v", err)
	}
	if string(got) != rawRFC822 {
		t.Errorf("RawBytes = %q, want %q", got, rawRFC822)
	}
}

func TestClient_ListHistory(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/me/history" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("startHistoryId"); got != "1000" {
			t.Errorf("startHistoryId = %q", got)
		}
		json.NewEncoder(w).Encode(HistoryList{
			History: []HistoryRecord{{
				MessagesAdded: []struct {
					Message MessageRef `json:"message"`
				}{{Message: MessageRef{ID: "new1"}}},
			}},
			HistoryID: "1050",
		})
	})

	list, err := c.ListHistory(context.Background(), "1000", "")
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if list.HistoryID != "1050" {
		t.Errorf("HistoryID = %q", list.HistoryID)
	}
	if len(list.History) != 1 || len(list.History[0].MessagesAdded) != 1 || list.History[0].MessagesAdded[0].Message.ID != "new1" {
		t.Fatalf("History = %+v", list.History)
	}
}

func TestClient_ListHistory_ExpiredReturnsSentinel(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": 404, "message": "Requested entity was not found."},
		})
	})

	_, err := c.ListHistory(context.Background(), "1", "")
	if err != ErrHistoryExpired {
		t.Errorf("err = %v, want ErrHistoryExpired", err)
	}
}

func TestClient_ModifyLabels(t *testing.T) {
	var gotBody struct {
		AddLabelIDs    []string `json:"addLabelIds"`
		RemoveLabelIDs []string `json:"removeLabelIds"`
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/users/me/messages/m1/modify" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	})

	if err := c.ModifyLabels(context.Background(), "m1", nil, []string{"UNREAD"}); err != nil {
		t.Fatalf("ModifyLabels: %v", err)
	}
	if len(gotBody.RemoveLabelIDs) != 1 || gotBody.RemoveLabelIDs[0] != "UNREAD" {
		t.Errorf("RemoveLabelIDs = %v", gotBody.RemoveLabelIDs)
	}
}

func TestClient_Trash(t *testing.T) {
	called := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/users/me/messages/m1/trash" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte("{}"))
	})

	if err := c.Trash(context.Background(), "m1"); err != nil {
		t.Fatalf("Trash: %v", err)
	}
	if !called {
		t.Error("server was never called")
	}
}

func TestClient_ErrorResponse_IncludesMessage(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": 403, "message": "insufficient scope"},
		})
	})

	_, err := c.GetProfile(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !strings.Contains(got, "insufficient scope") {
		t.Errorf("error = %q, want it to mention %q", got, "insufficient scope")
	}
}
