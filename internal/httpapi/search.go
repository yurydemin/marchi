package httpapi

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/search"
)

// registerSearch wires GET /api/v1/search (FR-API-02, FR-SR-03). It's a
// safe (GET) method, so it isn't CSRF-checked, but it still sits behind
// the lock gate like every other route — only /unlock is exempt.
func registerSearch(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/search", func(c *fiber.Ctx) error {
		b := vault.currentBackend()
		if b == nil {
			// Shouldn't happen: reaching this handler at all means the
			// gate already confirmed this session is unlocked, which only
			// happens after the backend (and its index) is built.
			return fiber.NewError(fiber.StatusServiceUnavailable, "vault is locked")
		}

		params, err := parseSearchParams(c)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		result, err := b.index.Search(c.Context(), params)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "search failed")
		}

		return c.JSON(searchResponseFrom(result))
	})
}

func parseSearchParams(c *fiber.Ctx) (search.Params, error) {
	var p search.Params
	p.Query = c.Query("q")
	p.Sender = c.Query("sender")
	p.Recipient = c.Query("recipient")

	var err error
	if v := c.Query("from"); v != "" {
		if p.DateFrom, err = parseSearchDate(v); err != nil {
			return p, badDateParam("from", v)
		}
	}
	if v := c.Query("to"); v != "" {
		if p.DateTo, err = parseSearchDate(v); err != nil {
			return p, badDateParam("to", v)
		}
	}
	if v := c.Query("account_id"); v != "" {
		if p.AccountID, err = strconv.ParseInt(v, 10, 64); err != nil {
			return p, badQueryParam("account_id", v)
		}
	}
	if v := c.Query("folder_id"); v != "" {
		if p.FolderID, err = strconv.ParseInt(v, 10, 64); err != nil {
			return p, badQueryParam("folder_id", v)
		}
	}
	if v := c.Query("has_attachments"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return p, badQueryParam("has_attachments", v)
		}
		p.HasAttachments = &b
	}
	switch c.Query("sort", "relevance") {
	case "relevance":
		p.Sort = search.SortRelevance
	case "date_asc":
		p.Sort = search.SortDateAsc
	case "date_desc":
		p.Sort = search.SortDateDesc
	default:
		return p, badQueryParam("sort", c.Query("sort"))
	}
	if v := c.Query("offset"); v != "" {
		if p.Offset, err = strconv.Atoi(v); err != nil {
			return p, badQueryParam("offset", v)
		}
	}
	if v := c.Query("limit"); v != "" {
		if p.Limit, err = strconv.Atoi(v); err != nil {
			return p, badQueryParam("limit", v)
		}
	}
	return p, nil
}

// parseSearchDate accepts a full RFC3339 timestamp or a bare date
// (2006-01-02) — the latter is what a plain HTML date input sends.
func parseSearchDate(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", v)
}

func badQueryParam(name, value string) error {
	return fmt.Errorf("invalid %s: %q", name, value)
}

func badDateParam(name, value string) error {
	return fmt.Errorf("invalid %s: %q (expected RFC3339 or YYYY-MM-DD)", name, value)
}

type searchHit struct {
	EmailID        int64     `json:"email_id"`
	MessageID      string    `json:"message_id"`
	Subject        string    `json:"subject"`
	From           string    `json:"from"`
	Date           time.Time `json:"date"`
	AccountID      int64     `json:"account_id"`
	FolderID       int64     `json:"folder_id"`
	HasAttachments bool      `json:"has_attachments"`
	Size           int64     `json:"size"`
	Score          float64   `json:"score"`
}

type searchResponse struct {
	Total   uint64      `json:"total"`
	Offset  int         `json:"offset"`
	Limit   int         `json:"limit"`
	Results []searchHit `json:"results"`
}

func searchResponseFrom(r search.Result) searchResponse {
	resp := searchResponse{Total: r.Total, Offset: r.Offset, Limit: r.Limit, Results: []searchHit{}}
	for _, h := range r.Hits {
		resp.Results = append(resp.Results, searchHit{
			EmailID: h.EmailID, MessageID: h.MessageID, Subject: h.Subject, From: h.From,
			Date: h.Date, AccountID: h.AccountID, FolderID: h.FolderID,
			HasAttachments: h.HasAttachments, Size: h.Size, Score: h.Score,
		})
	}
	return resp
}
