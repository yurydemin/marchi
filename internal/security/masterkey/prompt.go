package masterkey

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// PromptPassword reads a password from in, echo disabled if in is an actual
// terminal. If in isn't a terminal (piped stdin, or a test double) it falls
// back to reading a single line — there's no terminal to suppress echo on
// anyway, which is what makes scripted/unattended input over a pipe work.
//
// If confirm is true (first-run / setting a brand new password, with no
// stored password yet to validate a typo against) it prompts a second time
// and returns ErrPasswordMismatch if the two don't match.
func PromptPassword(in io.Reader, out io.Writer, confirm bool) (string, error) {
	var lineReader *bufio.Reader
	isTTY := false
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		isTTY = true
	} else {
		lineReader = bufio.NewReader(in)
	}

	read := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		if isTTY {
			b, err := term.ReadPassword(int(in.(*os.File).Fd()))
			fmt.Fprintln(out)
			if err != nil {
				return "", fmt.Errorf("masterkey: reading password: %w", err)
			}
			return string(b), nil
		}
		line, err := lineReader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("masterkey: reading password: %w", err)
		}
		fmt.Fprintln(out)
		return strings.TrimRight(line, "\r\n"), nil
	}

	password, err := read("Master Key password: ")
	if err != nil {
		return "", err
	}
	if len(password) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	if !confirm {
		return password, nil
	}

	confirmed, err := read("Confirm password: ")
	if err != nil {
		return "", err
	}
	if confirmed != password {
		return "", ErrPasswordMismatch
	}
	return password, nil
}
