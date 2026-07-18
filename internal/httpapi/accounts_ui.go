package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/maildir"
)

// accountsPageData is "accounts" page's top-level template data.
type accountsPageData struct {
	Unlocked bool
	Accounts []accountResponse
}

// testResultData renders the "test-result" fragment htmx swaps into a
// row's test-result span after POST /accounts/:id/test.
type testResultData struct {
	OK          bool
	FolderCount int
	Error       string
}

// registerAccountsPage wires the server-rendered Accounts screen
// (FR-WU-02): a full page (list + add form) plus a set of HTMX-fragment
// routes under the same /accounts/* path space that each return just the
// HTML they change — a row, an inline edit form, a test-result badge —
// rather than a full page reload. These routes sit outside newLockGate's
// scope (see its doc comment), so every one of them calls
// requireUnlockedSession itself instead of relying on middleware.
func registerAccountsPage(app *fiber.App, vault *vaultState, store *session.Store, pages map[string]*template.Template) {
	app.Get("/accounts", handleAccountsPage(vault, store, pages))
	app.Post("/accounts", handleAccountsCreate(vault, store, pages))
	app.Get("/accounts/:id", handleAccountRowView(vault, store, pages))
	app.Get("/accounts/:id/edit", handleAccountRowEdit(vault, store, pages))
	app.Put("/accounts/:id", handleAccountsUpdate(vault, store, pages))
	app.Delete("/accounts/:id", handleAccountsDelete(vault, store, pages))
	app.Put("/accounts/:id/toggle", handleAccountsToggle(vault, store, pages))
	app.Post("/accounts/:id/test", handleAccountsTest(vault, store, pages))
}

func handleAccountsPage(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		b, ok := pageBackend(c, vault, store)
		if !ok {
			return renderLocked(c, pages)
		}
		resp, err := listAccountResponses(c, b)
		if err != nil {
			return err
		}
		return pages["accounts"].ExecuteTemplate(c, "layout", accountsPageData{Unlocked: true, Accounts: resp})
	}
}

// listAccountResponses loads every account and shapes it the way both the
// full page and every fragment route that re-renders the list need it.
func listAccountResponses(c *fiber.Ctx, b *backend) ([]accountResponse, error) {
	accounts, err := b.accountsRepo.List(c.Context())
	if err != nil {
		return nil, fiber.NewError(fiber.StatusInternalServerError, "listing accounts failed")
	}
	resp := make([]accountResponse, len(accounts))
	for i, a := range accounts {
		resp[i] = accountResponseFrom(a)
	}
	return resp, nil
}

// renderAccountRows re-lists every account and renders the "account-rows"
// fragment (see accounts.html's doc comment for why every list mutation —
// create/update/delete/toggle — re-renders the whole <tbody> rather than
// patching a single row: the list can only render as a proper <table> at
// all once there's at least one account, so a single-row append/swap
// can't handle the empty-to-one-account transition).
func renderAccountRows(c *fiber.Ctx, b *backend, pages map[string]*template.Template) error {
	resp, err := listAccountResponses(c, b)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return pages["accounts"].ExecuteTemplate(c, "account-rows", resp)
}

// handleAccountsCreate mirrors handleCreateAccount (accounts.go) but
// speaks HTML fragments instead of JSON: on success it returns the whole
// refreshed row list (see renderAccountRows) plus an out-of-band swap
// that clears the form; on failure it retargets the response to the
// form's error slot via HX-Retarget/HX-Reswap instead of rendering an
// error message as if it were a table row.
func handleAccountsCreate(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		var req createAccountRequest
		if err := c.BodyParser(&req); err != nil {
			return fragmentError(c, pages, fiber.StatusBadRequest, "invalid form submission")
		}
		tlsMode, err := domain.ParseIMAPTLSMode(req.IMAPTLS)
		if err != nil {
			return fragmentError(c, pages, fiber.StatusBadRequest, err.Error())
		}
		if _, err := b.manager.AddAccount(c.Context(), account.AddAccountParams{
			Email: req.Email, DisplayName: req.DisplayName, IMAPHost: req.IMAPHost,
			IMAPPort: req.IMAPPort, IMAPTLS: tlsMode, IMAPUsername: req.IMAPUsername,
			IMAPPassword: req.IMAPPassword,
		}); err != nil {
			if errors.Is(err, repo.ErrDuplicateEmail) {
				return fragmentError(c, pages, fiber.StatusConflict, "an account with this email already exists")
			}
			return fragmentError(c, pages, fiber.StatusBadRequest, err.Error())
		}

		if err := renderAccountRows(c, b, pages); err != nil {
			return err
		}
		return pages["accounts"].ExecuteTemplate(c, "add-account-form-oob", nil)
	}
}

func handleAccountRowView(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		a, err := b.accountsRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading account failed")
		}
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return pages["accounts"].ExecuteTemplate(c, "account-row", accountResponseFrom(a))
	}
}

func handleAccountRowEdit(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		a, err := b.accountsRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading account failed")
		}
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return pages["accounts"].ExecuteTemplate(c, "account-edit-row", accountResponseFrom(a))
	}
}

// handleAccountsUpdate is the fragment counterpart of handleUpdateAccount
// (accounts.go). It deliberately doesn't accept is_active from the form —
// see updateAccountRequest's IsActive doc comment — the Save button in
// the edit row only ever touches connection settings; pausing/resuming
// stays the dedicated toggle button's job (handleAccountsToggle).
func handleAccountsUpdate(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		var req updateAccountRequest
		if err := c.BodyParser(&req); err != nil {
			return fragmentError(c, pages, fiber.StatusBadRequest, "invalid form submission")
		}
		tlsMode, err := domain.ParseIMAPTLSMode(req.IMAPTLS)
		if err != nil {
			return fragmentError(c, pages, fiber.StatusBadRequest, err.Error())
		}
		if _, err := b.manager.UpdateAccount(c.Context(), id, account.UpdateAccountParams{
			DisplayName: req.DisplayName, IMAPHost: req.IMAPHost, IMAPPort: req.IMAPPort,
			IMAPTLS: tlsMode, IMAPUsername: req.IMAPUsername, IMAPPassword: req.IMAPPassword,
			SyncCron: req.SyncCron,
		}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fragmentError(c, pages, fiber.StatusBadRequest, err.Error())
		}
		return renderAccountRows(c, b, pages)
	}
}

// handleAccountsDelete mirrors handleDeleteAccount's cascade (see that
// doc comment for why index/Maildir cleanup happens here explicitly),
// then re-renders the row list (renderAccountRows) so deleting the last
// account correctly falls back to the "no accounts yet" placeholder
// instead of leaving an empty, header-only table.
func handleAccountsDelete(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}

		emails, err := b.emailsRepo.ListByAccount(c.Context(), id)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing account's emails failed")
		}
		for _, e := range emails {
			_ = b.currentIndex().Delete(e.ID)
		}
		if err := b.accountsRepo.Delete(c.Context(), id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "deleting account failed")
		}
		_ = os.RemoveAll(maildir.AccountDir(b.maildirRoot, id))

		return renderAccountRows(c, b, pages)
	}
}

// handleAccountsToggle flips is_active without requiring the browser to
// resubmit every other field: it reads the account's current values and
// writes them back unchanged alongside the flipped flag, since
// account.UpdateAccountParams overwrites DisplayName/IMAPHost/IMAPPort/
// IMAPTLS/IMAPUsername/SyncCron unconditionally (only IMAPPassword and
// IsActive have "empty/nil keeps existing" semantics).
func handleAccountsToggle(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		current, err := b.accountsRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading account failed")
		}
		flipped := !current.IsActive
		if _, err := b.manager.UpdateAccount(c.Context(), id, account.UpdateAccountParams{
			DisplayName: current.DisplayName, IMAPHost: current.IMAPHost, IMAPPort: current.IMAPPort,
			IMAPTLS: current.IMAPTLS, IMAPUsername: current.IMAPUsername, SyncCron: current.SyncCron,
			IsActive: &flipped,
		}); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "toggling account failed")
		}
		return renderAccountRows(c, b, pages)
	}
}

// handleAccountsTest mirrors handleTestAccount (accounts.go) but renders
// the "test-result" fragment instead of JSON. Connection failures are a
// normal, expected outcome here (wrong password, unreachable host), not
// a server error — same 200-with-ok:false shape handleTestAccount uses.
func handleAccountsTest(vault *vaultState, store *session.Store, pages map[string]*template.Template) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := requireUnlockedSession(c, vault, store)
		if err != nil {
			return err
		}
		id, err := idParam(c, "id")
		if err != nil {
			return err
		}
		a, err := b.accountsRepo.GetByID(c.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "loading account failed")
		}
		password, err := b.manager.DecryptPassword(a)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "decrypting password failed")
		}

		imapCtx, imapCancel := context.WithTimeout(c.Context(), imapclient.DefaultDialTimeout)
		defer imapCancel()

		result := testResultData{}
		conn, err := imapclient.Connect(imapCtx, imapclient.ConnectOptions{
			Host: a.IMAPHost, Port: a.IMAPPort, TLS: a.IMAPTLS,
			Username: a.IMAPUsername, Password: password,
		})
		if err != nil {
			result.Error = err.Error()
		} else {
			defer conn.Logout()
			folders, err := imapclient.ListFolders(conn)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.OK = true
				result.FolderCount = len(folders)
			}
		}

		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return pages["accounts"].ExecuteTemplate(c, "test-result", result)
	}
}

// fragmentError writes msg to the add/edit form's shared error slot via
// HX-Retarget/HX-Reswap, so it lands in a dedicated message element
// instead of wherever the triggering element's own hx-target points
// (normally a table row or the tbody, not an error message's home).
func fragmentError(c *fiber.Ctx, pages map[string]*template.Template, status int, msg string) error {
	c.Status(status)
	c.Set("HX-Retarget", "#account-form-error")
	c.Set("HX-Reswap", "innerHTML")
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	return c.SendString(template.HTMLEscapeString(msg))
}
