package masterkey

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPromptPassword_NoConfirm(t *testing.T) {
	in := strings.NewReader("correct horse battery staple\n")
	var out bytes.Buffer

	pw, err := PromptPassword(NewSecretReader(in, &out), false)
	if err != nil {
		t.Fatalf("PromptPassword: %v", err)
	}
	if pw != "correct horse battery staple" {
		t.Errorf("got %q", pw)
	}
}

func TestPromptPassword_ConfirmMatches(t *testing.T) {
	in := strings.NewReader("correct horse battery staple\ncorrect horse battery staple\n")
	var out bytes.Buffer

	pw, err := PromptPassword(NewSecretReader(in, &out), true)
	if err != nil {
		t.Fatalf("PromptPassword: %v", err)
	}
	if pw != "correct horse battery staple" {
		t.Errorf("got %q", pw)
	}
}

func TestPromptPassword_ConfirmMismatch(t *testing.T) {
	in := strings.NewReader("correct horse battery staple\nsomething else entirely!!\n")
	var out bytes.Buffer

	_, err := PromptPassword(NewSecretReader(in, &out), true)
	if !errors.Is(err, ErrPasswordMismatch) {
		t.Errorf("got %v, want ErrPasswordMismatch", err)
	}
}

func TestPromptPassword_TooShort(t *testing.T) {
	in := strings.NewReader("short\n")
	var out bytes.Buffer

	_, err := PromptPassword(NewSecretReader(in, &out), false)
	if !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("got %v, want ErrPasswordTooShort", err)
	}
}

func TestPromptPassword_DoesNotEchoPasswordToOutputAsPlaintextTwice(t *testing.T) {
	// Sanity check that the prompt writer only carries the prompt labels,
	// not the password itself (non-TTY path reads from `in`, writes only
	// prompts + newlines to `out`).
	in := strings.NewReader("correct horse battery staple\n")
	var out bytes.Buffer

	if _, err := PromptPassword(NewSecretReader(in, &out), false); err != nil {
		t.Fatalf("PromptPassword: %v", err)
	}
	if strings.Contains(out.String(), "correct horse battery staple") {
		t.Errorf("password leaked into prompt output: %q", out.String())
	}
}

// TestSecretReader_SharedAcrossMultiplePrompts is the regression case for
// the bug this refactor fixes: unlockMasterKey's PromptPassword call and a
// following account-password prompt used to each construct their own
// SecretReader (and therefore their own bufio.Reader) over the same
// underlying stdin. bufio.Reader reads ahead into its internal buffer, so
// the second, independently-constructed reader would see EOF instead of
// the second line — already consumed into the first reader's buffer and
// discarded when it went out of scope. Sharing one SecretReader across
// both reads is the fix.
func TestSecretReader_SharedAcrossMultiplePrompts(t *testing.T) {
	in := strings.NewReader("master-key-password\nimap-account-password\n")
	var out bytes.Buffer
	r := NewSecretReader(in, &out)

	mk, err := r.Read("Master Key password: ")
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	if mk != "master-key-password" {
		t.Fatalf("first Read = %q, want %q", mk, "master-key-password")
	}

	imapPW, err := r.Read("IMAP password: ")
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if imapPW != "imap-account-password" {
		t.Errorf("second Read = %q, want %q (this is the bug: a fresh reader here would see EOF)", imapPW, "imap-account-password")
	}
}
