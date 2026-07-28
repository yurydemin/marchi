package httpapi

import (
	"github.com/gofiber/fiber/v2"

	"github.com/yurydemin/marchi/internal/domain"
	rulesengine "github.com/yurydemin/marchi/internal/rules"
)

// maxDryRunScan bounds how many already-archived emails a single dry-run
// request can scan, so an unscoped request against a large archive can't
// turn into an unbounded table scan.
const maxDryRunScan = 5000

// maxDryRunSamples caps how many matched emails are echoed back — the
// point is a representative preview, not a full result dump.
const maxDryRunSamples = 20

type dryRunRequest struct {
	Conditions domain.RuleNode `json:"conditions"`
	AccountID  *int64          `json:"account_id"`
	Limit      int             `json:"limit"`
}

type dryRunSample struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Date    string `json:"date"`
}

type dryRunResponse struct {
	TotalScanned int            `json:"total_scanned"`
	MatchedCount int            `json:"matched_count"`
	Samples      []dryRunSample `json:"samples"`
}

// handleDryRunRules tests an unsaved rule condition tree against
// already-archived emails, so a rule (especially one with
// archive_and_delete) can be sanity-checked before it's ever saved and
// takes effect on live sync. Reconstructs each candidate from persisted
// emails/attachments rows via rulesengine.BuildCandidateFromStored — see
// that function's docs for why the from/to address reconstruction matters.
func handleDryRunRules(vault *vaultState) fiber.Handler {
	return func(c *fiber.Ctx) error {
		b, err := currentBackendOrLocked(vault)
		if err != nil {
			return err
		}
		var req dryRunRequest
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
		}
		if err := rulesengine.Validate(req.Conditions); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		limit := req.Limit
		if limit <= 0 || limit > maxDryRunScan {
			limit = maxDryRunScan
		}

		emails, err := b.emailsRepo.ListForDryRun(c.Context(), req.AccountID, limit)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "scanning archive failed")
		}

		folderNames := make(map[int64]string)
		resp := dryRunResponse{TotalScanned: len(emails), Samples: []dryRunSample{}}
		for _, e := range emails {
			folderName, ok := folderNames[e.FolderID]
			if !ok {
				if f, err := b.foldersRepo.GetByID(c.Context(), e.FolderID); err == nil {
					folderName = f.FolderName
				}
				folderNames[e.FolderID] = folderName
			}

			var attachmentTypes []string
			if e.HasAttachments {
				if atts, err := b.attachmentsRepo.ListByEmail(c.Context(), e.ID); err == nil {
					attachmentTypes = make([]string, len(atts))
					for i, a := range atts {
						attachmentTypes[i] = a.MIMEType
					}
				}
			}

			candidate := rulesengine.BuildCandidateFromStored(e, folderName, attachmentTypes)
			if !rulesengine.Evaluate(req.Conditions, candidate) {
				continue
			}
			resp.MatchedCount++
			if len(resp.Samples) < maxDryRunSamples {
				resp.Samples = append(resp.Samples, dryRunSample{
					ID: e.ID, Subject: e.Subject, From: e.FromAddr, Date: e.Date.Format("2006-01-02 15:04"),
				})
			}
		}

		return c.JSON(resp)
	}
}
