package imapclient

import (
	"fmt"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/utf7"
)

// Folder is a single IMAP mailbox.
type Folder struct {
	Name       string // decoded (UTF-8) — this is what FR-ST-03's folders.folder_name stores
	RawName    string // as sent on the wire (modified UTF-7, RFC 3501 §5.1.3)
	Delimiter  string
	Attributes []string
}

// ListFolders returns every mailbox visible to the logged-in account,
// folder names UTF-7 decoded.
func ListFolders(c *client.Client) ([]Folder, error) {
	mailboxes := make(chan *imap.MailboxInfo, 16)
	done := make(chan error, 1)
	go func() {
		done <- c.List("", "*", mailboxes)
	}()

	decoder := utf7.Encoding.NewDecoder()
	var folders []Folder
	for m := range mailboxes {
		name, err := decoder.String(m.Name)
		if err != nil {
			// One oddly-encoded folder shouldn't fail the whole listing —
			// fall back to the raw wire name.
			name = m.Name
		}
		folders = append(folders, Folder{
			Name:       name,
			RawName:    m.Name,
			Delimiter:  m.Delimiter,
			Attributes: m.Attributes,
		})
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("imapclient: listing folders: %w", err)
	}
	return folders, nil
}

// EncodeFolderName re-encodes a decoded (UTF-8) folder name back into
// modified UTF-7 for use in IMAP commands (SELECT, STATUS, ...). FR-ST-03's
// folders.folder_name column stores only the decoded form, so anything
// operating on a stored Folder needs this round trip back to wire format —
// there's no raw name persisted anywhere to reuse instead.
func EncodeFolderName(name string) (string, error) {
	encoded, err := utf7.Encoding.NewEncoder().String(name)
	if err != nil {
		return "", fmt.Errorf("imapclient: encoding folder name %q: %w", name, err)
	}
	return encoded, nil
}
