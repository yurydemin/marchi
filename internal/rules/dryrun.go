package rules

import (
	"github.com/emersion/go-message/mail"

	"github.com/yurydemin/marchi/internal/domain"
)

// BuildCandidateFromStored reconstructs the Candidate an already-archived
// email would have produced at sync time, using only what's persisted in
// the emails/attachments tables — no raw .eml re-read needed, since none
// of the 15 condition types (see evaluateLeaf) inspect body content. This
// is what powers dry-run: testing a rule's condition tree against emails
// already in the archive, before the rule is ever saved.
//
// domain.Email.FromAddr/ToAddrs/CcAddrs are, despite the field names, the
// full RFC 5322 mailbox form — the same value archiveOne originally took
// from mimeparse.Metadata.From/To/Cc (see internal/sync/fetch.go), not
// the bare address the field names suggest. from_exact/from_domain/
// to_domain need the bare address instead, so this re-parses each stored
// string back into its address, mirroring what mimeparse.Parse did live
// at archive time — no different from parsing any other RFC 5322 mailbox
// string, since that's exactly what these already are.
func BuildCandidateFromStored(e *domain.Email, folderName string, attachmentMIMETypes []string) Candidate {
	return Candidate{
		From: e.FromAddr, FromAddr: bareAddress(e.FromAddr),
		To: e.ToAddrs, ToAddrs: bareAddresses(e.ToAddrs),
		Cc: e.CcAddrs, CcAddrs: bareAddresses(e.CcAddrs),
		Subject:         e.Subject,
		HasAttachments:  e.HasAttachments,
		AttachmentTypes: attachmentMIMETypes,
		Size:            e.Size,
		Date:            e.Date,
		FolderName:      folderName,
		AccountID:       e.AccountID,
	}
}

// bareAddress extracts the address out of a single stored RFC 5322
// mailbox string (e.g. `"Alice" <a@x.com>` -> `a@x.com`). A stored value
// that somehow doesn't parse (empty, or a malformed edge case that
// predates stricter validation) falls back to the original string as-is
// — the same "don't panic, just compare against something" spirit
// evaluateLeaf's own parse failures already have.
func bareAddress(full string) string {
	if full == "" {
		return ""
	}
	a, err := mail.ParseAddress(full)
	if err != nil {
		return full
	}
	return a.Address
}

func bareAddresses(full []string) []string {
	if len(full) == 0 {
		return nil
	}
	out := make([]string, len(full))
	for i, f := range full {
		out[i] = bareAddress(f)
	}
	return out
}
