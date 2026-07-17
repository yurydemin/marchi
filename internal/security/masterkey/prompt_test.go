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

	pw, err := PromptPassword(in, &out, false)
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

	pw, err := PromptPassword(in, &out, true)
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

	_, err := PromptPassword(in, &out, true)
	if !errors.Is(err, ErrPasswordMismatch) {
		t.Errorf("got %v, want ErrPasswordMismatch", err)
	}
}

func TestPromptPassword_TooShort(t *testing.T) {
	in := strings.NewReader("short\n")
	var out bytes.Buffer

	_, err := PromptPassword(in, &out, false)
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

	if _, err := PromptPassword(in, &out, false); err != nil {
		t.Fatalf("PromptPassword: %v", err)
	}
	if strings.Contains(out.String(), "correct horse battery staple") {
		t.Errorf("password leaked into prompt output: %q", out.String())
	}
}
