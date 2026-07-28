// Package importer reads already-exported mail out of three common
// offline formats — mbox, Maildir, and a directory of loose .eml files —
// and hands each message's raw RFC 5322 bytes to a callback. It knows
// nothing about accounts, folders, or SQLite: internal/sync.ArchiveOne
// (already IMAP-agnostic — see its doc comment) is what actually
// persists what these Walk* functions find, exactly the same way it
// persists a message FetchNewMessages just pulled over IMAP.
package importer

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
)

// WalkMbox reads path as a single mbox file and calls fn once per message,
// in the file's own order, stopping at the first error either reading the
// file or fn returns.
//
// Messages are separated by a line that starts with "From " (the mbox
// envelope line — its own content, typically "From <sender> <date>", is
// discarded; ArchiveOne/mimeparse.Parse only care about the RFC 5322
// headers that follow it, not this legacy Unix-mailbox artifact). Per the
// widely-used "mboxrd" convention, a body line that begins with one or
// more ">" followed by "From " is unescaped by stripping exactly one
// leading ">" — that's how a real "From " line occurring inside a
// message's own body gets round-tripped through mbox without being
// mistaken for the next envelope line. Only a single level of escaping is
// undone (">From " -> "From ", ">>From " -> ">From "), matching what
// every mbox-writing tool this size needs to interoperate with actually
// produces.
func WalkMbox(path string, fn func(raw []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("importer: opening mbox %q: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024) // a single message up to 64MB

	var current bytes.Buffer
	started := false

	flush := func() error {
		if !started {
			return nil
		}
		// Mbox convention appends one blank line before the next "From "
		// separator (or EOF) as a delimiter between messages — not part
		// of the message itself.
		raw := bytes.TrimSuffix(current.Bytes(), []byte("\n"))
		current.Reset()
		return fn(raw)
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if isMboxFromLine(line) {
			if err := flush(); err != nil {
				return err
			}
			started = true
			continue
		}
		if started {
			if unescaped, ok := unescapeMboxFromLine(line); ok {
				current.Write(unescaped)
			} else {
				current.Write(line)
			}
			current.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("importer: reading mbox %q: %w", path, err)
	}
	return flush()
}

// isMboxFromLine reports whether line is an mbox envelope separator: it
// starts with "From " (not ">From " — that's an escaped in-body line, see
// unescapeMboxFromLine). Real mbox envelope lines also carry a sender and
// timestamp after "From ", but this doesn't validate that shape further —
// any line honestly starting with "From " at column 0 is treated as a
// separator, matching the format every mbox-writing tool actually
// produces (a raw, unescaped "From " can only legitimately appear there).
func isMboxFromLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("From "))
}

// unescapeMboxFromLine strips exactly one leading ">" from a line that
// starts with ">" followed by (possibly more ">"s and then) "From ",
// reversing the mboxrd escaping WalkMbox's own doc comment describes. ok
// is false for any line that isn't such an escaped line, in which case
// the caller should use the line unmodified.
func unescapeMboxFromLine(line []byte) (unescaped []byte, ok bool) {
	if len(line) == 0 || line[0] != '>' {
		return nil, false
	}
	rest := line[1:]
	for len(rest) > 0 && rest[0] == '>' {
		rest = rest[1:]
	}
	if !bytes.HasPrefix(rest, []byte("From ")) {
		return nil, false
	}
	return line[1:], true
}
