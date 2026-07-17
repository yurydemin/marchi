package masterkey

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// SecretReader reads hidden input (echo disabled) from a real terminal, or
// falls back to plain line reads when in isn't a terminal — piped stdin, or
// a test double. There's no terminal to suppress echo on in that case
// anyway, which is what makes scripted/unattended input over a pipe work.
//
// State (the buffered reader for the non-TTY path) is kept across calls to
// Read, so asking twice in a row — e.g. password then confirmation — doesn't
// lose bytes that were already buffered past the first line.
type SecretReader struct {
	in    io.Reader
	out   io.Writer
	isTTY bool
	buf   *bufio.Reader // nil when isTTY
}

// NewSecretReader constructs a SecretReader over in/out. Not specific to
// Master Key passwords — reusable for any hidden CLI input (e.g. an IMAP
// account password), which is why it lives as its own type rather than
// being folded directly into PromptPassword's length/confirmation rules.
func NewSecretReader(in io.Reader, out io.Writer) *SecretReader {
	r := &SecretReader{in: in, out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		r.isTTY = true
	} else {
		r.buf = bufio.NewReader(in)
	}
	return r
}

// Read prints prompt, then reads one line of hidden input.
func (r *SecretReader) Read(prompt string) (string, error) {
	fmt.Fprint(r.out, prompt)
	if r.isTTY {
		b, err := term.ReadPassword(int(r.in.(*os.File).Fd()))
		fmt.Fprintln(r.out)
		if err != nil {
			return "", fmt.Errorf("masterkey: reading input: %w", err)
		}
		return string(b), nil
	}
	line, err := r.buf.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("masterkey: reading input: %w", err)
	}
	fmt.Fprintln(r.out)
	return strings.TrimRight(line, "\r\n"), nil
}

// PromptPassword reads a Master Key password via r, enforcing
// MinPasswordLength. If confirm is true (first-run / setting a brand new
// password, with no stored password yet to validate a typo against) it
// prompts a second time and returns ErrPasswordMismatch if the two don't
// match.
//
// r must be shared with any other prompt the same command invocation needs
// (e.g. an account password right after unlocking) — constructing a second
// SecretReader over the same underlying stdin would lose bytes the first
// one already buffered past its own line, on the non-TTY (piped) path.
func PromptPassword(r *SecretReader, confirm bool) (string, error) {
	password, err := r.Read("Master Key password: ")
	if err != nil {
		return "", err
	}
	if len(password) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	if !confirm {
		return password, nil
	}

	confirmed, err := r.Read("Confirm password: ")
	if err != nil {
		return "", err
	}
	if confirmed != password {
		return "", ErrPasswordMismatch
	}
	return password, nil
}
