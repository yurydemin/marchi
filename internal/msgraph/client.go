// Package msgraph is a hand-rolled client for the handful of Microsoft
// Graph REST API v1.0 endpoints the MS Graph connector needs
// (internal/sync's SyncAccountMSGraph): listing mail folders, delta-syncing
// messages within a folder, fetching raw MIME content, and marking
// read/deleting for rule actions.
//
// Deliberately not Microsoft's official Graph SDK (kiota-generated,
// dozens of transitive modules — heavier even than
// google.golang.org/api/gmail/v1, which internal/gmailapi already
// avoided for the same reason; see that package's own doc comment). A
// handful of REST calls and one JSON envelope shape don't justify that
// weight here either.
package msgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// defaultBaseURL is Microsoft Graph v1.0's real endpoint. Tests override
// it via Client.BaseURL to point at an httptest.Server instead.
const defaultBaseURL = "https://graph.microsoft.com/v1.0"

// Client talks to Microsoft Graph as the signed-in user ("me" — Graph's
// own alias for the authenticated account). AccessToken must already be
// valid (refreshed if necessary) — Client does not refresh it itself,
// matching internal/gmailapi.Client's identical contract and the reason
// given there (every other OAuth2 call site already resolves/refreshes
// through internal/account.Manager before reaching the API client).
type Client struct {
	HTTPClient  *http.Client // nil uses http.DefaultClient
	BaseURL     string       // "" uses defaultBaseURL
	AccessToken string
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

// apiError is the shape Graph returns on failure:
// {"error": {"code": "...", "message": "..."}}.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// doJSON issues a request against a Graph-relative path (joined with
// baseURL) or, if rawURL is already absolute (an @odata.nextLink or
// @odata.deltaLink from a previous response), against that URL verbatim
// — Graph's pagination/delta links are complete, ready-to-call URLs that
// already carry their own query token, not something to re-derive from
// baseURL.
func (c *Client) doJSON(ctx context.Context, method, pathOrURL string, body any, out any) error {
	u := pathOrURL
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = c.baseURL() + pathOrURL
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("msgraph: encoding request body: %w", err)
		}
		reqBody = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("msgraph: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("msgraph: %s %s: %w", method, pathOrURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("msgraph: reading response body: %w", err)
	}

	if resp.StatusCode >= 300 {
		var apiErr apiError
		msg := string(respBody)
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		return fmt.Errorf("msgraph: %s %s: %d %s", method, pathOrURL, resp.StatusCode, msg)
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("msgraph: decoding response body: %w", err)
	}
	return nil
}

// MailFolder is one entry from /mailFolders or /mailFolders/{id}/childFolders.
type MailFolder struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	ChildFolderCount int    `json:"childFolderCount"`
}

type mailFolderList struct {
	Value    []MailFolder `json:"value"`
	NextLink string       `json:"@odata.nextLink"`
}

// ListMailFolders returns every top-level mail folder (Inbox, Sent Items,
// Drafts, Deleted Items, custom folders, ...) — every folder Graph
// exposes, deliberately with no Deleted-Items/Junk-style exclusion,
// matching how the IMAP connector's own SyncFolders doesn't special-case
// any folder either (see internal/sync.SyncAccountMSGraph's doc comment).
// Pagination (@odata.nextLink) is followed internally.
func (c *Client) ListMailFolders(ctx context.Context) ([]MailFolder, error) {
	return c.listFolders(ctx, "/me/mailFolders?$top=100")
}

// ListChildFolders returns folderID's immediate child folders — the
// caller (internal/sync) walks this recursively for nested folders,
// mirroring how IMAP folder names already flatten a server's hierarchy
// into a single string.
func (c *Client) ListChildFolders(ctx context.Context, folderID string) ([]MailFolder, error) {
	return c.listFolders(ctx, "/me/mailFolders/"+url.PathEscape(folderID)+"/childFolders?$top=100")
}

func (c *Client) listFolders(ctx context.Context, path string) ([]MailFolder, error) {
	var all []MailFolder
	next := path
	for next != "" {
		var page mailFolderList
		if err := c.doJSON(ctx, http.MethodGet, next, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		next = page.NextLink
	}
	return all, nil
}

// MessageStub is one message as returned by DeltaMessages — enough to
// know a message exists and its read/flagged state, without the raw MIME
// body (fetched separately via GetMessageRaw). Removed is set when this
// entry represents a deletion (Graph's delta "@removed" annotation) —
// the connector doesn't propagate source deletions to the archive (an
// archiver keeps mail the source has already gotten rid of; the IMAP and
// Gmail API connectors don't do so either), so callers should simply skip
// entries where Removed is non-nil.
type MessageStub struct {
	ID     string `json:"id"`
	IsRead bool   `json:"isRead"`
	Flag   *struct {
		FlagStatus string `json:"flagStatus"` // "notFlagged", "flagged", "complete"
	} `json:"flag,omitempty"`
	Removed *struct {
		Reason string `json:"reason"`
	} `json:"@removed,omitempty"`
}

// DeltaPage is one page of DeltaMessages' response. NextLink, if
// non-empty, means more pages follow (call DeltaMessages again with it).
// DeltaLink, only present on the final page, is the cursor to persist
// for the next sync's incremental call.
type DeltaPage struct {
	Value     []MessageStub `json:"value"`
	NextLink  string        `json:"@odata.nextLink"`
	DeltaLink string        `json:"@odata.deltaLink"`
}

// DeltaMessages drives Microsoft Graph's delta query for one mail
// folder — Graph's own incremental-sync mechanism (see the package doc
// comment's contrast with internal/gmailapi's separate
// getProfile-then-list dance: delta needs no such choreography, calling
// it with link="" for a never-synced folder simply returns every current
// message, paginated, ending in a deltaLink that already correctly
// covers everything from that point forward). link is either "" (start a
// fresh delta from scratch for folderID) or a previous response's
// NextLink/DeltaLink (a complete, ready-to-call URL — folderID is not
// used in that case, since the link already encodes it).
func (c *Client) DeltaMessages(ctx context.Context, folderID, link string) (DeltaPage, error) {
	target := link
	if target == "" {
		target = "/me/mailFolders/" + url.PathEscape(folderID) + "/messages/delta?$top=50"
	}
	var page DeltaPage
	if err := c.doJSON(ctx, http.MethodGet, target, nil, &page); err != nil {
		return DeltaPage{}, err
	}
	return page, nil
}

// GetMessageRaw fetches one message's full raw RFC822 content via
// Graph's $value endpoint — unlike every other call in this package,
// the response body itself *is* the raw bytes (Content-Type:
// message/rfc822), not a JSON envelope.
func (c *Client) GetMessageRaw(ctx context.Context, messageID string) ([]byte, error) {
	u := c.baseURL() + "/me/messages/" + url.PathEscape(messageID) + "/$value"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("msgraph: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("msgraph: GET %s: %w", u, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("msgraph: reading response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		var apiErr apiError
		msg := string(body)
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		return nil, fmt.Errorf("msgraph: GET %s: %d %s", u, resp.StatusCode, msg)
	}
	return body, nil
}

// MarkRead sets isRead=true on one message (FR-RE-03's
// archive_and_mark_read).
func (c *Client) MarkRead(ctx context.Context, messageID string) error {
	body := struct {
		IsRead bool `json:"isRead"`
	}{true}
	return c.doJSON(ctx, http.MethodPatch, "/me/messages/"+url.PathEscape(messageID), body, nil)
}

// Delete removes one message (FR-RE-03's archive_and_delete). Graph
// moves a message to the Deleted Items folder on DELETE rather than
// erasing it outright — unless it's already in Deleted Items, in which
// case DELETE is permanent — the same "recoverable, not instantly gone"
// spirit as the IMAP connector's \Deleted+expunge and the Gmail
// connector's Trash.
func (c *Client) Delete(ctx context.Context, messageID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/me/messages/"+url.PathEscape(messageID), nil, nil)
}
