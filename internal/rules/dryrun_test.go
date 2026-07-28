package rules

import (
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
)

// TestBuildCandidateFromStored_BareAddressGotcha guards the exact nuance
// found while designing dry-run: domain.Email.FromAddr/ToAddrs/CcAddrs
// store the full RFC 5322 mailbox form ("Alice" <a@x.com>), not the bare
// address the field names suggest — see archiveOne (internal/sync/
// fetch.go), which populates them from mimeparse.Metadata.From/To/Cc, not
// .FromAddr/.ToAddrs/.CcAddrs. from_exact/from_domain/to_domain need the
// bare address, so a naive Candidate.FromAddr = e.FromAddr would compare
// against a value still carrying the display name and angle brackets,
// silently failing every exact/domain match a live sync would have
// caught correctly.
func TestBuildCandidateFromStored_BareAddressGotcha(t *testing.T) {
	e := &domain.Email{
		FromAddr: `"Billing" <billing@vendor.com>`,
		ToAddrs:  []string{`"Ops Team" <ops@example.com>`},
		CcAddrs:  []string{"plain@example.com"}, // no display name — must still parse
	}

	c := BuildCandidateFromStored(e, "INBOX", nil)

	if c.From != `"Billing" <billing@vendor.com>` {
		t.Errorf("From = %q, want the full stored form unchanged (for *_contains regex matches)", c.From)
	}
	if c.FromAddr != "billing@vendor.com" {
		t.Errorf("FromAddr = %q, want the bare address", c.FromAddr)
	}
	if len(c.ToAddrs) != 1 || c.ToAddrs[0] != "ops@example.com" {
		t.Errorf("ToAddrs = %v, want bare [ops@example.com]", c.ToAddrs)
	}
	if len(c.CcAddrs) != 1 || c.CcAddrs[0] != "plain@example.com" {
		t.Errorf("CcAddrs = %v, want [plain@example.com] unchanged (already bare)", c.CcAddrs)
	}

	// The whole point: from_exact/from_domain must actually match against
	// a candidate built this way, exactly as they would have at live
	// archive time.
	if !Evaluate(leaf(domain.ConditionFromExact, "billing@vendor.com"), c) {
		t.Error("from_exact did not match — bare-address extraction is broken")
	}
	if !Evaluate(leaf(domain.ConditionFromDomain, "vendor.com"), c) {
		t.Error("from_domain did not match — bare-address extraction is broken")
	}
	if !Evaluate(leaf(domain.ConditionToDomain, "example.com"), c) {
		t.Error("to_domain did not match — bare-address extraction is broken")
	}
}

func TestBuildCandidateFromStored_PlainMetadataPassesThrough(t *testing.T) {
	when := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	e := &domain.Email{
		AccountID: 42, Subject: "Invoice #123", HasAttachments: true, Size: 4096, Date: when,
	}
	c := BuildCandidateFromStored(e, "Archive/2026", []string{"application/pdf"})

	if c.Subject != "Invoice #123" || c.AccountID != 42 || c.Size != 4096 || !c.Date.Equal(when) {
		t.Errorf("c = %+v, unexpected plain-field pass-through", c)
	}
	if !c.HasAttachments || len(c.AttachmentTypes) != 1 || c.AttachmentTypes[0] != "application/pdf" {
		t.Errorf("attachment fields = %v/%v, unexpected", c.HasAttachments, c.AttachmentTypes)
	}
	if c.FolderName != "Archive/2026" {
		t.Errorf("FolderName = %q, want the value passed in (not derivable from domain.Email alone)", c.FolderName)
	}
}

func TestBuildCandidateFromStored_EmptyAddressesDoNotPanic(t *testing.T) {
	e := &domain.Email{} // no From/To/Cc at all — a malformed or headerless message
	c := BuildCandidateFromStored(e, "INBOX", nil)
	if c.FromAddr != "" || c.ToAddrs != nil || c.CcAddrs != nil {
		t.Errorf("c = %+v, want all-empty for an email with no address headers", c)
	}
}
