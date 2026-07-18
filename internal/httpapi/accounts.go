package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/maildir"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

// registerAccounts wires the Accounts REST API (FR-API-02): CRUD, a
// saved account's connection test, a manual sync trigger, its folder
// list, and its recent sync history.
func registerAccounts(app *fiber.App, vault *vaultState) {
	app.Get("/api/v1/accounts", handleListAccounts(vault))
	app.Post("/api/v1/accounts", handleCreateAccount(vault))
	app.Put("/api/v1/accounts/:id", handleUpdateAccount(vault))
	app.Delete("/api/v1/accounts/:id", handleDeleteAccount(vault))
	app.Post("/api/v1/accounts/:id/test", handleTestAccount(vault))
	app.Post("/api/v1/accounts/:id/sync", handleSyncAccount(vault))
	app.Get("/api/v1/accounts/:id/folders", handleListAccountFolders(vault))
	app.Get("/api/v1/accounts/:id/sync-status", handleAccountSyncStatus(vault))
}

type accountResponse struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"display_name"`
	IMAPHost     string    `json:"imap_host"`
	IMAPPort     int       `json:"imap_port"`
	IMAPTLS      string    `json:"imap_tls"`
	IMAPUsername string    `json:"imap_username"`
	IsActive     bool      `json:"is_active"`
	SyncCron     string    `json:"sync_cron,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func accountResponseFrom(a *domain.Account) accountResponse {
	return accountResponse{
		ID: a.ID, Email: a.Email, DisplayName: a.DisplayName,
		IMAPHost: a.IMAPHost, IMAPPort: a.IMAPPort, IMAPTLS: a.IMAPTLS.String(),
		IMAPUsername: a.IMAPUsername, IsActive: a.IsActive, SyncCron: a.SyncCron,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func handleListAccounts(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		accounts, err := b.accountsRepo.List(c.Context())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing accounts failed")
		}
		resp := make([]accountResponse, len(accounts))
		for i, a := range accounts {
			resp[i] = accountResponseFrom(a)
		}
		return c.JSON(resp)
	}
}

type createAccountRequest struct {
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	IMAPTLS      string `json:"imap_tls"`
	IMAPUsername string `json:"imap_username"`
	IMAPPassword string `json:"imap_password"`
}

func handleCreateAccount(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req createAccountRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		tlsMode, err := domain.ParseIMAPTLSMode(req.IMAPTLS)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		a, err := b.manager.AddAccount(c.Context(), account.AddAccountParams{
			Email: req.Email, DisplayName: req.DisplayName, IMAPHost: req.IMAPHost,
			IMAPPort: req.IMAPPort, IMAPTLS: tlsMode, IMAPUsername: req.IMAPUsername,
			IMAPPassword: req.IMAPPassword,
		})
		if err != nil {
			if errors.Is(err, repo.ErrDuplicateEmail) {
				return fiber.NewError(fiber.StatusConflict, "an account with this email already exists")
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.Status(fiber.StatusCreated).JSON(accountResponseFrom(a))
	}
}

type updateAccountRequest struct {
	DisplayName  string `json:"display_name"`
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	IMAPTLS      string `json:"imap_tls"`
	IMAPUsername string `json:"imap_username"`
	IMAPPassword string `json:"imap_password"` // "" keeps the existing password
	IsActive     *bool  `json:"is_active"`     // omitted keeps the existing value
	SyncCron     string `json:"sync_cron"`
}

func handleUpdateAccount(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := accountIDParam(c)
		if err != nil {
			return err
		}
		var req updateAccountRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		tlsMode, err := domain.ParseIMAPTLSMode(req.IMAPTLS)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}

		a, err := b.manager.UpdateAccount(c.Context(), id, account.UpdateAccountParams{
			DisplayName: req.DisplayName, IMAPHost: req.IMAPHost, IMAPPort: req.IMAPPort,
			IMAPTLS: tlsMode, IMAPUsername: req.IMAPUsername, IMAPPassword: req.IMAPPassword,
			IsActive: req.IsActive, SyncCron: req.SyncCron,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(accountResponseFrom(a))
	}
}

// handleDeleteAccount implements FR-AM-06's cascade: the search index and
// the on-disk Maildir archive don't have foreign keys pointing at
// accounts, so they're cleaned up here explicitly, before the SQL row
// (and everything that DOES cascade via ON DELETE CASCADE — folders,
// emails, attachments, sync_logs) is removed. Index cleanup is
// best-effort, same philosophy as indexing itself (see
// internal/sync/fetch.go's archiveOne): a failed Delete there leaves a
// stale document that a future reindex (FR-SR-04) clears up, not a
// reason to abort the account deletion.
//
// The user-confirmation FR-AM-06 requires happens in the Web UI (a
// confirm dialog before this request is even sent) — this endpoint itself
// has no separate confirmation step beyond the CSRF token every mutating
// request already needs.
func handleDeleteAccount(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := accountIDParam(c)
		if err != nil {
			return err
		}

		emails, err := b.emailsRepo.ListByAccount(c.Context(), id)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing account's emails failed")
		}
		for _, e := range emails {
			_ = b.index.Delete(e.ID)
		}

		if err := b.accountsRepo.Delete(c.Context(), id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fiber.NewError(fiber.StatusNotFound, "account not found")
			}
			return fiber.NewError(fiber.StatusInternalServerError, "deleting account failed")
		}

		_ = os.RemoveAll(maildir.AccountDir(b.maildirRoot, id))
		return c.SendStatus(fiber.StatusNoContent)
	}
}

func handleTestAccount(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := accountIDParam(c)
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

		conn, err := imapclient.Connect(imapCtx, imapclient.ConnectOptions{
			Host: a.IMAPHost, Port: a.IMAPPort, TLS: a.IMAPTLS,
			Username: a.IMAPUsername, Password: password,
		})
		if err != nil {
			return c.Status(fiber.StatusOK).JSON(fiber.Map{"ok": false, "error": err.Error()})
		}
		defer conn.Logout()

		folders, err := imapclient.ListFolders(conn)
		if err != nil {
			return c.Status(fiber.StatusOK).JSON(fiber.Map{"ok": false, "error": err.Error()})
		}
		return c.JSON(fiber.Map{"ok": true, "folder_count": len(folders)})
	}
}

func handleSyncAccount(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := accountIDParam(c)
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

		host, err := os.Hostname()
		if err != nil {
			host = "localhost"
		}

		// Synchronous for now: this blocks until the sync finishes, same
		// as the CLI's own `mailvault sync`. A later step adds WebSocket
		// progress and can move this to run in the background instead.
		results, syncErr := syncengine.SyncAccount(c.Context(), a, password, b.maildirRoot, host,
			b.w, b.foldersRepo, b.emailsRepo, b.attachmentsRepo, b.syncLogsRepo, b.index)

		archived := 0
		folderResults := make([]fiber.Map, len(results))
		for i, r := range results {
			archived += r.Fetched
			folderResults[i] = fiber.Map{
				"folder_id": r.Folder.ID, "folder_name": r.Folder.FolderName,
				"uidvalidity": r.Folder.UIDValidity, "last_uid": r.Folder.LastUID, "fetched": r.Fetched,
			}
		}
		resp := fiber.Map{"folders": folderResults, "archived": archived}
		if syncErr != nil {
			resp["error"] = syncErr.Error()
		}
		return c.JSON(resp)
	}
}

type folderResponse struct {
	ID          int64  `json:"id"`
	FolderName  string `json:"folder_name"`
	UIDValidity uint32 `json:"uidvalidity"`
	LastUID     uint32 `json:"last_uid"`
	SyncEnabled bool   `json:"sync_enabled"`
}

func handleListAccountFolders(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := accountIDParam(c)
		if err != nil {
			return err
		}
		folders, err := b.foldersRepo.ListByAccount(c.Context(), id)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "listing folders failed")
		}
		resp := make([]folderResponse, len(folders))
		for i, f := range folders {
			resp[i] = folderResponse{
				ID: f.ID, FolderName: f.FolderName, UIDValidity: f.UIDValidity,
				LastUID: f.LastUID, SyncEnabled: f.SyncEnabled,
			}
		}
		return c.JSON(resp)
	}
}

type syncLogResponse struct {
	ID              int64     `json:"id"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at,omitempty"`
	EmailsProcessed int       `json:"emails_processed"`
	EmailsArchived  int       `json:"emails_archived"`
	BytesDownloaded int64     `json:"bytes_downloaded"`
	Errors          int       `json:"errors"`
	Status          string    `json:"status"`
	ErrorMsg        string    `json:"error_msg,omitempty"`
}

func handleAccountSyncStatus(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		id, err := accountIDParam(c)
		if err != nil {
			return err
		}
		limit, err := strconv.Atoi(c.Query("limit", "20"))
		if err != nil || limit <= 0 {
			limit = 20
		}
		logs, err := b.syncLogsRepo.ListByAccount(c.Context(), id, limit)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "loading sync history failed")
		}
		resp := make([]syncLogResponse, len(logs))
		for i, l := range logs {
			resp[i] = syncLogResponse{
				ID: l.ID, StartedAt: l.StartedAt, EndedAt: l.EndedAt,
				EmailsProcessed: l.EmailsProcessed, EmailsArchived: l.EmailsArchived,
				BytesDownloaded: l.BytesDownloaded, Errors: l.Errors,
				Status: string(l.Status), ErrorMsg: l.ErrorMsg,
			}
		}
		return c.JSON(resp)
	}
}

// accountIDParam parses the :id route param, returning a 400 fiber.Error
// (not a plain error) if it isn't a valid integer.
func accountIDParam(c *fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return 0, fiber.NewError(fiber.StatusBadRequest, "invalid account id")
	}
	return id, nil
}

// currentBackendOrLocked returns the backend, or a 503 fiber.Error.
// Reaching any of these handlers at all already implies the gate
// confirmed this session is unlocked (which only happens once the
// backend is built), so a nil backend here would indicate a bug, not a
// normal locked-vault request.
func currentBackendOrLocked(vault *vaultState) (*backend, error) {
	b := vault.currentBackend()
	if b == nil {
		return nil, fiber.NewError(fiber.StatusServiceUnavailable, "vault is locked")
	}
	return b, nil
}
