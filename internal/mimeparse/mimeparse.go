// Package mimeparse extracts archival metadata — Message-ID, Subject,
// From, To, Cc, Date — from a raw RFC 5322 message, via emersion/go-message.
//
// Parsing never fails outright: FR-SE-04 requires the original .eml bytes
// to be preserved regardless of how malformed a message is, so Parse always
// returns a best-effort Metadata (possibly with empty fields for a message
// that can't be fully understood) rather than an error that would block
// archiving the raw content.
package mimeparse

import (
	"bytes"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// Metadata is what gets extracted for SQLite indexing (FR-ST-03's emails
// table columns) — not the full parsed message, just the header fields
// MailVault cares about for search/browsing.
type Metadata struct {
	MessageID string
	Subject   string
	From      string
	To        []string
	Cc        []string
	Date      time.Time
}

// Parse extracts Metadata from raw. A message with an unknown charset or
// transfer encoding still yields whatever headers could be read (go-message
// returns a usable Entity alongside IsUnknownCharset/IsUnknownEncoding
// errors); only a message whose header section itself can't be parsed at
// all yields an entirely empty Metadata.
func Parse(raw []byte) Metadata {
	var md Metadata

	entity, _ := message.Read(bytes.NewReader(raw))
	if entity == nil {
		return md
	}

	h := mail.Header{Header: entity.Header}

	if id, err := h.MessageID(); err == nil {
		md.MessageID = id
	}
	if subject, err := h.Subject(); err == nil {
		md.Subject = subject
	}
	if date, err := h.Date(); err == nil {
		md.Date = date
	}
	if from, err := h.AddressList("From"); err == nil && len(from) > 0 {
		md.From = from[0].String()
	}
	if to, err := h.AddressList("To"); err == nil {
		md.To = addressStrings(to)
	}
	if cc, err := h.AddressList("Cc"); err == nil {
		md.Cc = addressStrings(cc)
	}

	return md
}

func addressStrings(addrs []*mail.Address) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}
