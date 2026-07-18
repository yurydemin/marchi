package httpapi

import (
	"fmt"
	"html/template"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/yurydemin/marchi/internal/mimeparse"
)

// registerArchivePage wires the server-rendered Archive screen (FR-WU-02):
// a folder tree, a search form doubling as a browse-by-folder filter, a
// results table, and an inline message viewer. Unlike Accounts UI's
// HTMX-fragment routes, every interaction here is a plain GET with the
// full view state (filters, pagination, which email is open) carried in
// the URL's query string — bookmarkable, back-button-friendly, and
// simple, which matters more for a browse/search/read screen than the
// in-place-mutation ergonomics HTMX bought Accounts UI.
func registerArchivePage(app *fiber.App, vault *vaultState, store *session.Store, pages map[string]*template.Template) {
	app.Get("/archive", handleArchivePage(vault, store, pages))
}

type archiveAccountNode struct {
	ID      int64
	Email   string
	Folders []archiveFolderNode
}

type archiveFolderNode struct {
	ID   int64
	Name string
	URL  string
}

type archiveResultRow struct {
	EmailID        int64
	Subject        string
	From           string
	Date           time.Time
	Size           int64
	HasAttachments bool
	AccountEmail   string
	FolderName     string
	ViewURL        string
	Selected       bool
}

type archiveAttachment struct {
	ID          int64
	Filename    string
	MIMEType    string
	Size        int64
	DownloadURL string
}

type archiveViewer struct {
	EmailID     int64
	Subject     string
	From        string
	To          []string
	Cc          []string
	Date        time.Time
	BodyHTML    template.HTML
	BodyText    string
	Attachments []archiveAttachment
	DownloadURL string
	PrevURL     string
	NextURL     string
}

type archivePageData struct {
	Unlocked bool

	Accounts []archiveAccountNode

	Query          string
	Sender         string
	Recipient      string
	AccountID      int64
	FolderID       int64
	DateFrom       string
	DateTo         string
	HasAttachments string
	Sort           string

	Results     []archiveResultRow
	Total       uint64
	Offset      int
	Limit       int
	PrevPageURL string
	NextPageURL string

	Viewer *archiveViewer
}

func handleArchivePage(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		b, ok := pageBackend(c, vault, store)
		if !ok {
			return renderLocked(c, pages)
		}

		accounts, err := b.accountsRepo.List(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing accounts failed")
		}
		accountEmailByID := make(map[int64]string, len(accounts))
		folderNameByID := make(map[int64]string)
		tree := make([]archiveAccountNode, len(accounts))
		for i, a := range accounts {
			accountEmailByID[a.ID] = a.Email
			folders, err := b.foldersRepo.ListByAccount(c.Context(), a.ID)
			if err != nil {
				return fiber.NewError(fiber.StatusInternalServerError, "listing folders failed")
			}
			node := archiveAccountNode{ID: a.ID, Email: a.Email, Folders: make([]archiveFolderNode, len(folders))}
			for j, f := range folders {
				folderNameByID[f.ID] = f.FolderName
				node.Folders[j] = archiveFolderNode{
					ID:   f.ID,
					Name: f.FolderName,
					URL:  "/archive?" + url.Values{"account_id": {strconv.FormatInt(a.ID, 10)}, "folder_id": {strconv.FormatInt(f.ID, 10)}}.Encode(),
				}
			}
			tree[i] = node
		}

		params, err := parseSearchParams(c)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		result, err := b.currentIndex().Search(c.Context(), params)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "search failed")
		}

		filterValues := archiveFilterValues(c)
		rows := make([]archiveResultRow, len(result.Hits))
		viewID, hasView := parseInt64Query(c, "view")
		for i, h := range result.Hits {
			rowValues := cloneValues(filterValues)
			rowValues.Set("offset", strconv.Itoa(result.Offset))
			rowValues.Set("limit", strconv.Itoa(result.Limit))
			rowValues.Set("view", strconv.FormatInt(h.EmailID, 10))
			rows[i] = archiveResultRow{
				EmailID: h.EmailID, Subject: h.Subject, From: h.From, Date: h.Date,
				Size: h.Size, HasAttachments: h.HasAttachments,
				AccountEmail: accountEmailByID[h.AccountID], FolderName: folderNameByID[h.FolderID],
				ViewURL:  "/archive?" + rowValues.Encode(),
				Selected: hasView && h.EmailID == viewID,
			}
		}

		data := archivePageData{
			Unlocked: true,
			Accounts: tree,
			Query:    params.Query, Sender: params.Sender, Recipient: params.Recipient,
			AccountID: params.AccountID, FolderID: params.FolderID,
			DateFrom: c.Query("from"), DateTo: c.Query("to"),
			HasAttachments: c.Query("has_attachments"),
			Sort:           c.Query("sort", "relevance"),
			Results:        rows, Total: result.Total, Offset: result.Offset, Limit: result.Limit,
		}
		if result.Offset > 0 {
			prevOffset := result.Offset - result.Limit
			if prevOffset < 0 {
				prevOffset = 0
			}
			prevValues := cloneValues(filterValues)
			prevValues.Set("offset", strconv.Itoa(prevOffset))
			prevValues.Set("limit", strconv.Itoa(result.Limit))
			data.PrevPageURL = "/archive?" + prevValues.Encode()
		}
		if uint64(result.Offset+result.Limit) < result.Total {
			nextValues := cloneValues(filterValues)
			nextValues.Set("offset", strconv.Itoa(result.Offset+result.Limit))
			nextValues.Set("limit", strconv.Itoa(result.Limit))
			data.NextPageURL = "/archive?" + nextValues.Encode()
		}

		if hasView {
			data.Viewer = buildArchiveViewer(c, b, viewID, rows)
		}

		return pages["archive"].ExecuteTemplate(c, "layout", data)
	}
}

// archiveFilterValues extracts just the filter fields (not pagination or
// which email is open) from the request, so every link this page renders
// (folder tree, page prev/next, row open, viewer prev/next) can start
// from the same base and override only what it means to change.
func archiveFilterValues(c *fiber.Ctx) url.Values {
	v := url.Values{}
	for _, name := range []string{"q", "sender", "recipient", "account_id", "folder_id", "from", "to", "has_attachments", "sort"} {
		if val := c.Query(name); val != "" {
			v.Set(name, val)
		}
	}
	return v
}

func cloneValues(v url.Values) url.Values {
	clone := make(url.Values, len(v))
	for k, vals := range v {
		clone[k] = append([]string(nil), vals...)
	}
	return clone
}

func parseInt64Query(c *fiber.Ctx, name string) (int64, bool) {
	s := c.Query(name)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// buildArchiveViewer loads and renders the email named by the "view"
// query param. A missing/unreadable email just means no viewer (the
// search results stay usable) rather than failing the whole page — same
// best-effort philosophy as handleGetEmail's body preview. Prev/Next are
// resolved from rows, the current page's already-fetched results, so
// opening an email never costs a second search call — this only moves
// within the currently displayed page, not the full result set.
func buildArchiveViewer(c *fiber.Ctx, b *backend, emailID int64, rows []archiveResultRow) *archiveViewer {
	e, err := b.emailsRepo.GetByID(c.Context(), emailID)
	if err != nil {
		return nil
	}
	attachments, err := b.attachmentsRepo.ListByEmail(c.Context(), emailID)
	if err != nil {
		attachments = nil
	}
	attResp := make([]archiveAttachment, len(attachments))
	for i, a := range attachments {
		attResp[i] = archiveAttachment{
			ID: a.ID, Filename: a.Filename, MIMEType: a.MIMEType, Size: a.Size,
			DownloadURL: fmt.Sprintf("/api/v1/emails/%d/attachments/%d/download", emailID, a.ID),
		}
	}

	v := &archiveViewer{
		EmailID: e.ID, Subject: e.Subject, From: e.FromAddr, To: e.ToAddrs, Cc: e.CcAddrs, Date: e.Date,
		Attachments: attResp,
		DownloadURL: fmt.Sprintf("/api/v1/emails/%d/download", e.ID),
	}
	if e.StorageLocation == "local" && e.LocalPath != "" {
		if raw, err := os.ReadFile(e.LocalPath); err == nil {
			parts := mimeparse.ParseBodyParts(raw)
			if html := sanitizeEmailHTML(parts.HTML); html != "" {
				v.BodyHTML = template.HTML(html)
			} else {
				v.BodyText = parts.Text
			}
		}
	}

	for i, r := range rows {
		if r.EmailID != emailID {
			continue
		}
		if i > 0 {
			v.PrevURL = rows[i-1].ViewURL
		}
		if i < len(rows)-1 {
			v.NextURL = rows[i+1].ViewURL
		}
		break
	}
	return v
}
