// Package gmailapi is a hand-rolled client for the handful of Gmail REST
// API v1 endpoints the Gmail connector needs (internal/sync's
// SyncAccountGmailAPI): listing/fetching messages, delta sync via the
// History API, and label/trash mutations for rule actions.
//
// Deliberately not google.golang.org/api/gmail/v1 — that SDK drags in
// the whole Google Cloud auth/opentelemetry/grpc dependency tree (dozens
// of transitive modules) for what amounts to six REST calls.
// internal/oauth2 already made the same call about golang.org/x/oauth2/google
// for the same reason (see its own doc comment); five REST endpoints and
// one JSON envelope don't justify that weight here either.
package gmailapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// defaultBaseURL is Gmail API v1's real endpoint. Tests override it via
// Client.BaseURL to point at an httptest.Server instead.
const defaultBaseURL = "https://gmail.googleapis.com/gmail/v1"

// ErrHistoryExpired is returned by ListHistory when startHistoryID is
// older than Gmail's retention window for history records (about a
// week) — Gmail responds 404 in that case. The caller's only recourse is
// falling back to a full ListMessages resync, the same thing a brand new
// account without any cursor yet does.
var ErrHistoryExpired = errors.New("gmailapi: history id too old, full resync required")

// Client talks to the Gmail API as a single user ("me" — Gmail's own
// alias for the authenticated account, avoiding a separate profile
// lookup just to learn the address). AccessToken must already be valid
// (refreshed if necessary) — Client does not refresh it itself; see
// internal/account.Manager's OAuth2 resolution, which every other OAuth2
// call site (IMAP, SMTP) already goes through the same way.
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

// apiError is the shape Gmail (and Google APIs generally) return on
// failure: {"error": {"code": 404, "message": "..."}}.
type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	u := c.baseURL() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gmailapi: encoding request body: %w", err)
		}
		reqBody = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("gmailapi: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("gmailapi: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("gmailapi: reading response body: %w", err)
	}

	if resp.StatusCode >= 300 {
		var apiErr apiError
		msg := string(respBody)
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		if resp.StatusCode == http.StatusNotFound && strings.Contains(path, "/history") {
			return ErrHistoryExpired
		}
		return fmt.Errorf("gmailapi: %s %s: %d %s", method, path, resp.StatusCode, msg)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("gmailapi: decoding response body: %w", err)
	}
	return nil
}

// Profile is the subset of users.getProfile's response the connector
// needs — mainly HistoryID, the mailbox-state cursor a fresh account's
// first sync anchors its subsequent incremental syncs to (see
// internal/sync.SyncAccountGmailAPI's doc comment for the exact
// getProfile-before-listing sequencing that makes this safe).
type Profile struct {
	EmailAddress string `json:"emailAddress"`
	HistoryID    string `json:"historyId"`
}

func (c *Client) GetProfile(ctx context.Context) (Profile, error) {
	var p Profile
	err := c.do(ctx, http.MethodGet, "/users/me/profile", nil, nil, &p)
	return p, err
}

// MessageRef is a bare message identifier as returned by both
// ListMessages and ListHistory — the caller fetches the full message
// separately via GetMessageRaw, matching how a two-step
// list-then-fetch is unavoidable with this API (list/history never
// include the message body).
type MessageRef struct {
	ID string `json:"id"`
}

// MessageList is users.messages.list's response.
type MessageList struct {
	Messages      []MessageRef `json:"messages"`
	NextPageToken string       `json:"nextPageToken"`
}

// ListMessages returns one page of every message in the mailbox except
// Spam/Trash (Gmail's default when no labelIds filter is given —
// documented as the "All Mail" equivalent the connector's single
// synthetic folder is built around; see internal/sync's folder-mapping
// doc comment). pageToken is "" for the first page.
func (c *Client) ListMessages(ctx context.Context, pageToken string) (MessageList, error) {
	q := url.Values{"maxResults": {"500"}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	var list MessageList
	err := c.do(ctx, http.MethodGet, "/users/me/messages", q, nil, &list)
	return list, err
}

// Message is users.messages.get's response with format=raw: the full
// RFC 822 message, base64url-encoded, plus the labels currently applied
// to it (used to derive Maildir/IMAP-style flags — see
// internal/sync.gmailFlagsFromLabels).
type Message struct {
	ID       string   `json:"id"`
	LabelIDs []string `json:"labelIds"`
	Raw      string   `json:"raw"`
}

// RawBytes decodes Raw from Gmail's base64url encoding. Gmail's own docs
// don't commit to padded vs unpadded output, so both are accepted.
func (m Message) RawBytes() ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(m.Raw); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(m.Raw)
}

// GetMessageRaw fetches one message's full raw RFC822 content.
func (c *Client) GetMessageRaw(ctx context.Context, id string) (Message, error) {
	q := url.Values{"format": {"raw"}}
	var msg Message
	err := c.do(ctx, http.MethodGet, "/users/me/messages/"+url.PathEscape(id), q, nil, &msg)
	return msg, err
}

// HistoryRecord is one entry in users.history.list's response —
// messagesAdded is the only change type the connector acts on (new mail
// since the last sync); other types (labelsAdded, labelsRemoved,
// messagesDeleted) exist in the API but aren't archived-mail-relevant
// for a mirror/archive tool the way they would be for a full Gmail
// client, and are intentionally not modeled here.
type HistoryRecord struct {
	MessagesAdded []struct {
		Message MessageRef `json:"message"`
	} `json:"messagesAdded"`
}

// HistoryList is users.history.list's response.
type HistoryList struct {
	History       []HistoryRecord `json:"history"`
	NextPageToken string          `json:"nextPageToken"`
	HistoryID     string          `json:"historyId"`
}

// ListHistory returns changes since startHistoryID (Gmail's delta-sync
// mechanism — see the package doc comment). Returns ErrHistoryExpired if
// startHistoryID has fallen outside Gmail's retention window; the caller
// must fall back to a full ListMessages resync in that case.
func (c *Client) ListHistory(ctx context.Context, startHistoryID, pageToken string) (HistoryList, error) {
	q := url.Values{"startHistoryId": {startHistoryID}, "historyTypes": {"messageAdded"}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	var list HistoryList
	err := c.do(ctx, http.MethodGet, "/users/me/history", q, nil, &list)
	return list, err
}

// ModifyLabels adds/removes labels on one message (FR-RE-03's
// archive_and_mark_read maps to removing the UNREAD label — Gmail has
// no separate "read" flag, just the absence of that label).
func (c *Client) ModifyLabels(ctx context.Context, id string, addLabelIDs, removeLabelIDs []string) error {
	body := struct {
		AddLabelIDs    []string `json:"addLabelIds,omitempty"`
		RemoveLabelIDs []string `json:"removeLabelIds,omitempty"`
	}{addLabelIDs, removeLabelIDs}
	return c.do(ctx, http.MethodPost, "/users/me/messages/"+url.PathEscape(id)+"/modify", nil, body, nil)
}

// Trash moves one message to Gmail's Trash (FR-RE-03's
// archive_and_delete — recoverable for 30 days, matching the IMAP
// connector's own \Deleted+expunge being a deliberate, but not
// instantly-unrecoverable-on-a-typical-server, deletion).
func (c *Client) Trash(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/users/me/messages/"+url.PathEscape(id)+"/trash", nil, nil, nil)
}
