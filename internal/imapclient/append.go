package imapclient

import (
	"bytes"
	"fmt"
	"time"

	"github.com/emersion/go-imap/client"
)

// Append uploads raw (a full RFC 2822 message) into folder via IMAP
// APPEND (FR-RS-02's primary restore method), preserving date and flags.
//
// date should be the message's own Date: header — this project's Sync
// Engine (Phase 1) never captured the true IMAP INTERNALDATE via a
// separate FETCH INTERNALDATE call during archival, only the message's
// own parsed Date header (domain.Email.Date), so that's the closest
// available approximation to "original INTERNALDATE" FR-RS-02 asks for.
// For a message that was never manually moved between mailboxes with
// deliberate date-shifting, the two are typically identical in practice.
//
// flags carries over whatever IMAP flags were recorded at archive time
// (domain.Email.Flags) — e.g. \Seen, \Answered — so a restored message
// keeps its read/answered state rather than reverting to \Recent.
//
// bytes.NewReader satisfies imap.Literal directly (io.Reader + Len()),
// no wrapper type needed.
func Append(c *client.Client, folder string, flags []string, date time.Time, raw []byte) error {
	if err := c.Append(folder, flags, date, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("imapclient: APPEND to %q: %w", folder, err)
	}
	return nil
}
