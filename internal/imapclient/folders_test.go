package imapclient

import (
	"testing"

	"github.com/emersion/go-imap/utf7"
)

func TestEncodeFolderName_RoundTripsWithListFoldersDecoding(t *testing.T) {
	cases := []string{
		"INBOX",
		"Заметки",
		"[Gmail]/Sent Mail",
		"Père",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			encoded, err := EncodeFolderName(name)
			if err != nil {
				t.Fatalf("EncodeFolderName(%q): %v", name, err)
			}

			decoded, err := utf7.Encoding.NewDecoder().String(encoded)
			if err != nil {
				t.Fatalf("decoding back: %v", err)
			}
			if decoded != name {
				t.Errorf("round trip: %q -> %q -> %q", name, encoded, decoded)
			}
		})
	}
}

func TestEncodeFolderName_ASCIIUnchanged(t *testing.T) {
	encoded, err := EncodeFolderName("INBOX.Sent")
	if err != nil {
		t.Fatalf("EncodeFolderName: %v", err)
	}
	if encoded != "INBOX.Sent" {
		t.Errorf("ASCII name should encode to itself unchanged, got %q", encoded)
	}
}
