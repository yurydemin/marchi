package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/i18n"
	"github.com/yurydemin/marchi/internal/importer"
	"github.com/yurydemin/marchi/internal/maildir"
	"github.com/yurydemin/marchi/internal/mimeparse"
	"github.com/yurydemin/marchi/internal/search"
	syncengine "github.com/yurydemin/marchi/internal/sync"
)

// importedAccountUIDValidity is the constant UIDVALIDITY every import
// target folder is upserted with. A real UIDVALIDITY changing is the
// server's signal that its own UID numbering was invalidated (FR-SE-02);
// nothing analogous ever happens to a local mbox/Maildir/.eml source, so
// this never changes either — which is exactly what makes repeated
// `marchi import` runs against the same --as-account/--folder resume
// cleanly instead of resetting last_uid back to 0 each time (see
// FoldersRepo.UpsertFolder's own doc comment for that reset rule).
const importedAccountUIDValidity = 1

// importPlaceholderIMAPHost marks an account created by this command as
// obviously not a real, connectable mailbox — accounts.imap_host is
// NOT NULL, so this needs *some* value, and a value that fails DNS
// resolution loudly (rather than one that might coincidentally resolve)
// is the safest thing to put there for an account the Scheduler and
// `marchi sync` must never actually dial.
const importPlaceholderIMAPHost = "imported.invalid"

func newImportCmd(loc *i18n.Localizer) *cobra.Command {
	var (
		importType string
		path       string
		asAccount  string
		folderName string
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: loc.T("cli.import.short"),
		Long: "Import already-exported mail from an mbox file, a Maildir folder, or a directory\n" +
			"of .eml files into the archive (no live IMAP connection involved). Every message\n" +
			"unconditionally archives — rules aren't evaluated for imported mail. Safe to run\n" +
			"again against the same --as-account/--folder (e.g. after adding more messages to\n" +
			"the source): already-imported messages are recognized by Message-ID and skipped.",
		RunE: func(cmd *cobra.Command, args []string) error {
			walk, err := walkerFor(importType, path)
			if err != nil {
				return err
			}
			if strings.TrimSpace(asAccount) == "" {
				return fmt.Errorf("--as-account is required")
			}

			cfg := configFrom(cmd.Context())
			logger := loggerFrom(cmd.Context())
			ctx := cmd.Context()

			sqlDB, err := db.Open(cfg.Database.SQLite.Path)
			if err != nil {
				return err
			}
			defer closeDB(logger, sqlDB)

			w := writer.New(sqlDB)
			defer w.Close()

			accountsRepo := repo.NewAccountsRepo(sqlDB, w)
			a, err := resolveImportAccount(ctx, accountsRepo, asAccount)
			if err != nil {
				return err
			}

			folder := folderName
			if folder == "" {
				folder = deriveImportFolderName(importType, path)
			}
			foldersRepo := repo.NewFoldersRepo(sqlDB, w)
			f, err := foldersRepo.UpsertFolder(ctx, a.ID, folder, importedAccountUIDValidity)
			if err != nil {
				return fmt.Errorf("preparing target folder %q: %w", folder, err)
			}

			emailsRepo := repo.NewEmailsRepo(sqlDB, w)
			attachmentsRepo := repo.NewAttachmentsRepo(sqlDB, w)

			idx, err := search.Open(cfg.Search.IndexPath)
			if err != nil {
				return err
			}
			defer idx.Close()

			// Mirror to S3 exactly like a real sync would, if it's
			// configured and enabled — imported mail is otherwise
			// indistinguishable from any other archived mail once it's
			// in the database, so there's no reason it should bypass
			// the mirror. sql.ErrNoRows (never configured) just means
			// mirroring stays off, same as SyncAccount's own handling.
			var s3Queue *repo.S3UploadQueueRepo
			s3ConfigRepo := repo.NewS3ConfigRepo(sqlDB, w)
			if s3cfg, err := s3ConfigRepo.Get(ctx); err == nil && s3cfg.Enabled {
				s3Queue = repo.NewS3UploadQueueRepo(sqlDB, w)
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("loading S3 config: %w", err)
			}

			host, err := os.Hostname()
			if err != nil {
				host = "localhost"
			}
			dir := maildir.FolderDir(cfg.Storage.MaildirPath, a.ID, maildir.SafeFolderName(f.FolderName))
			layout, err := maildir.NewLayout(dir)
			if err != nil {
				return fmt.Errorf("preparing maildir for %q: %w", f.FolderName, err)
			}
			mw := maildir.NewWriter(layout, host)

			var processed, archived, duplicates, failed int
			nextUID := f.LastUID + 1
			walkErr := walk(func(raw []byte) error {
				processed++
				md := mimeparse.Parse(raw)
				attachments := mimeparse.ParseAttachments(raw)

				if md.MessageID != "" {
					dup, err := emailsRepo.ExistsByAccountMessageID(ctx, a.ID, md.MessageID)
					if err != nil {
						failed++
						logger.Error("import: checking duplicate failed", zap.Int("processed", processed), zap.Error(err))
						return nil
					}
					if dup {
						duplicates++
						return nil
					}
				}

				uid := nextUID
				if _, _, archErr := syncengine.ArchiveOne(ctx, raw, md, attachments, uid, nil, a.ID, f, mw, w, emailsRepo, foldersRepo, attachmentsRepo, idx, s3Queue); archErr != nil {
					// Unlike FetchNewMessages' stop-at-first-error contract
					// (which exists to protect a UID watermark that can only
					// move forward), a bad message here doesn't threaten
					// resumability — the Message-ID dedup check above is
					// what makes re-running this command safe, not UID
					// ordering — so one unreadable/unwritable message
					// shouldn't abort an import of thousands of others.
					failed++
					logger.Error("import: archiving message failed", zap.Int("processed", processed), zap.Error(archErr))
					return nil
				}
				nextUID++
				archived++
				return nil
			})
			if walkErr != nil {
				return walkErr
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Import complete: %d message(s) processed, %d archived, %d duplicate(s) skipped, %d failed. Account: %s, folder: %s.\n",
				processed, archived, duplicates, failed, a.Email, f.FolderName)
			return nil
		},
	}

	cmd.Flags().StringVar(&importType, "type", "", "mbox, maildir, or eml (required)")
	cmd.Flags().StringVar(&path, "path", "", "path to the mbox file, Maildir folder, or .eml file/directory (required)")
	cmd.Flags().StringVar(&asAccount, "as-account", "", "email to import into — created automatically if it doesn't exist (required)")
	cmd.Flags().StringVar(&folderName, "folder", "", "target folder name (default: derived from --path)")

	return cmd
}

// walkerFor validates --type/--path and returns the matching
// importer.WalkX function, already bound to path, so the RunE body
// doesn't need its own type switch.
func walkerFor(importType, path string) (func(fn func(raw []byte) error) error, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("--path is required")
	}
	switch importType {
	case "mbox":
		return func(fn func(raw []byte) error) error { return importer.WalkMbox(path, fn) }, nil
	case "maildir":
		return func(fn func(raw []byte) error) error { return importer.WalkMaildir(path, fn) }, nil
	case "eml":
		return func(fn func(raw []byte) error) error { return importer.WalkEML(path, fn) }, nil
	case "":
		return nil, fmt.Errorf("--type is required (mbox, maildir, or eml)")
	default:
		return nil, fmt.Errorf("invalid --type %q (want mbox, maildir, or eml)", importType)
	}
}

// resolveImportAccount finds asAccount by email, or creates it if
// absent — this command's own "создаёт аккаунт при отсутствии". An
// existing account found by email must already be an import account:
// silently importing into a real, credentialed IMAP account by email
// coincidence would be surprising and hard to notice (nothing about a
// successful `marchi import` run would hint that it happened), so this
// refuses rather than guessing what the caller meant.
func resolveImportAccount(ctx context.Context, accountsRepo *repo.AccountsRepo, email string) (*domain.Account, error) {
	existing, err := accountsRepo.GetByEmail(ctx, email)
	switch {
	case err == nil:
		if !existing.IsImported {
			return nil, fmt.Errorf("account %s already exists and is not an import account — refusing to import into it", email)
		}
		return existing, nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through to Create below
	default:
		return nil, fmt.Errorf("looking up account %s: %w", email, err)
	}

	id, err := accountsRepo.Create(ctx, &domain.Account{
		Email:      email,
		IMAPHost:   importPlaceholderIMAPHost,
		IsActive:   false,
		IsImported: true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating account %s: %w", email, err)
	}
	return accountsRepo.GetByID(ctx, id)
}

// deriveImportFolderName picks a target folder name from --path when
// --folder isn't given: an mbox file's or a single .eml file's name with
// its extension stripped, or a Maildir/.eml directory's own name as-is.
func deriveImportFolderName(importType, path string) string {
	base := filepath.Base(filepath.Clean(path))
	if importType == "maildir" {
		return base
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return base // an --type eml directory
	}
	if ext := filepath.Ext(base); ext != "" {
		return strings.TrimSuffix(base, ext)
	}
	return base
}
