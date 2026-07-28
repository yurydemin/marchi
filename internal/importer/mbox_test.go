package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkMbox_SplitsMessages(t *testing.T) {
	content := "From alice@example.com Mon Jan  1 00:00:00 2024\r\n" +
		"From: alice@example.com\r\n" +
		"Subject: One\r\n" +
		"\r\n" +
		"Body one.\r\n" +
		"\r\n" +
		"From bob@example.com Tue Jan  2 00:00:00 2024\r\n" +
		"From: bob@example.com\r\n" +
		"Subject: Two\r\n" +
		"\r\n" +
		"Body two.\r\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "test.mbox")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	if err := WalkMbox(path, func(raw []byte) error {
		got = append(got, string(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2: %q", len(got), got)
	}
	if !strings.Contains(got[0], "Subject: One") || !strings.Contains(got[0], "Body one.") {
		t.Errorf("message 1 = %q, missing expected content", got[0])
	}
	if strings.Contains(got[0], "Subject: Two") {
		t.Errorf("message 1 = %q, bled into message 2's content", got[0])
	}
	if !strings.Contains(got[1], "Subject: Two") || !strings.Contains(got[1], "Body two.") {
		t.Errorf("message 2 = %q, missing expected content", got[1])
	}
}

// TestWalkMbox_UnescapesFromLines guards the exact reason WalkMbox can't
// just split on any line starting with "From ": a message whose own body
// happens to start a line with "From " (a quoted forwarded message is a
// realistic way this happens) would otherwise be silently truncated
// mid-body, mistaking that line for the next message's envelope.
func TestWalkMbox_UnescapesFromLines(t *testing.T) {
	content := "From alice@example.com Mon Jan  1 00:00:00 2024\r\n" +
		"From: alice@example.com\r\n" +
		"Subject: Quoted\r\n" +
		"\r\n" +
		">From the desk of Bob:\r\n" +
		"Hello.\r\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "test.mbox")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	if err := WalkMbox(path, func(raw []byte) error {
		got = append(got, string(raw))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1 (the >From line was mistaken for a new envelope): %q", len(got), got)
	}
	if !strings.Contains(got[0], "From the desk of Bob:") {
		t.Errorf("message = %q, want unescaped \"From the desk of Bob:\" line", got[0])
	}
	if strings.Contains(got[0], ">From the desk of Bob:") {
		t.Errorf("message = %q, the leading \">\" should have been stripped", got[0])
	}
}

func TestWalkMbox_StopsOnCallbackError(t *testing.T) {
	content := "From a@x.com Mon Jan  1 00:00:00 2024\r\n\r\nOne\r\n" +
		"From b@x.com Mon Jan  1 00:00:00 2024\r\n\r\nTwo\r\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "test.mbox")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := 0
	wantErr := os.ErrClosed // any sentinel
	err := WalkMbox(path, func(raw []byte) error {
		calls++
		return wantErr
	})
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (should stop at the first error)", calls)
	}
}
