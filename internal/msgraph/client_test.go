package msgraph

import (
	"context"
	"encoding/json"
	"io"
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

func TestClient_ListMailFolders_FollowsPagination(t *testing.T) {
	var page2Called bool
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/me/mailFolders", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"value":           []MailFolder{{ID: "f1", DisplayName: "Inbox"}},
			"@odata.nextLink": srv.URL + "/page2",
		})
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		page2Called = true
		json.NewEncoder(w).Encode(map[string]any{
			"value": []MailFolder{{ID: "f2", DisplayName: "Archive"}},
		})
	})

	c := &Client{BaseURL: srv.URL, AccessToken: "test-token"}
	folders, err := c.ListMailFolders(context.Background())
	if err != nil {
		t.Fatalf("ListMailFolders: %v", err)
	}
	if len(folders) != 2 || folders[0].ID != "f1" || folders[1].ID != "f2" {
		t.Fatalf("folders = %+v", folders)
	}
	if !page2Called {
		t.Error("expected the nextLink page to have been fetched")
	}
}

func TestClient_ListChildFolders(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/mailFolders/parent1/childFolders" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"value": []MailFolder{{ID: "child1", DisplayName: "Projects"}},
		})
	})
	folders, err := c.ListChildFolders(context.Background(), "parent1")
	if err != nil {
		t.Fatalf("ListChildFolders: %v", err)
	}
	if len(folders) != 1 || folders[0].DisplayName != "Projects" {
		t.Fatalf("folders = %+v", folders)
	}
}

func TestClient_DeltaMessages_InitialCall(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/mailFolders/inbox1/messages/delta" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(DeltaPage{
			Value:     []MessageStub{{ID: "m1", IsRead: true}, {ID: "m2", IsRead: false}},
			DeltaLink: "https://graph.microsoft.com/v1.0/me/mailFolders/inbox1/messages/delta?$deltatoken=abc",
		})
	})
	page, err := c.DeltaMessages(context.Background(), "inbox1", "")
	if err != nil {
		t.Fatalf("DeltaMessages: %v", err)
	}
	if len(page.Value) != 2 {
		t.Fatalf("Value = %+v", page.Value)
	}
	if page.DeltaLink == "" {
		t.Error("expected a DeltaLink on the final page")
	}
	if !page.Value[0].IsRead || page.Value[1].IsRead {
		t.Errorf("IsRead mismatch: %+v", page.Value)
	}
}

func TestClient_DeltaMessages_FollowUpCallUsesLinkVerbatim(t *testing.T) {
	var gotPath string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		json.NewEncoder(w).Encode(DeltaPage{DeltaLink: "next-cursor"})
	})
	link := c.BaseURL + "/me/mailFolders/inbox1/messages/delta?$deltatoken=xyz"
	if _, err := c.DeltaMessages(context.Background(), "ignored-folder-id", link); err != nil {
		t.Fatalf("DeltaMessages: %v", err)
	}
	if gotPath != "/me/mailFolders/inbox1/messages/delta?$deltatoken=xyz" {
		t.Errorf("server received path %q", gotPath)
	}
}

func TestClient_DeltaMessages_ReportsRemovedEntries(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{
				{"id": "m1", "@removed": map[string]any{"reason": "deleted"}},
			},
		})
	})
	page, err := c.DeltaMessages(context.Background(), "inbox1", "")
	if err != nil {
		t.Fatalf("DeltaMessages: %v", err)
	}
	if len(page.Value) != 1 || page.Value[0].Removed == nil || page.Value[0].Removed.Reason != "deleted" {
		t.Fatalf("Value = %+v", page.Value)
	}
}

func TestClient_GetMessageRaw_ReturnsBodyVerbatim(t *testing.T) {
	rawRFC822 := "From: a@example.com\r\nSubject: hi\r\n\r\nbody"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/messages/m1/$value" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "message/rfc822")
		w.Write([]byte(rawRFC822))
	})
	got, err := c.GetMessageRaw(context.Background(), "m1")
	if err != nil {
		t.Fatalf("GetMessageRaw: %v", err)
	}
	if string(got) != rawRFC822 {
		t.Errorf("GetMessageRaw = %q, want %q", got, rawRFC822)
	}
}

func TestClient_GetMessageRaw_ErrorResponse(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "ErrorItemNotFound", "message": "message not found"},
		})
	})
	_, err := c.GetMessageRaw(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "message not found") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "message not found")
	}
}

func TestClient_MarkRead(t *testing.T) {
	var gotBody struct {
		IsRead bool `json:"isRead"`
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/me/messages/m1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Write([]byte("{}"))
	})
	if err := c.MarkRead(context.Background(), "m1"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if !gotBody.IsRead {
		t.Error("expected isRead=true in the request body")
	}
}

func TestClient_Delete(t *testing.T) {
	called := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/me/messages/m1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Delete(context.Background(), "m1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !called {
		t.Error("server was never called")
	}
}
