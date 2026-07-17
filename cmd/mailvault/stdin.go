package main

import (
	"os"

	"github.com/yurydemin/marchi/internal/security/masterkey"
)

// stdinSecrets is the single shared SecretReader over os.Stdin for the
// whole process. os.Stdin is a process-wide singleton, so constructing a
// fresh SecretReader (and therefore a fresh bufio.Reader) per prompt would
// lose bytes already buffered past the first line whenever a command needs
// more than one prompt in a row over a piped/non-TTY stdin — e.g.
// unlockMasterKey's password prompt followed by an account password
// prompt in the same `add-account` invocation.
var stdinSecrets = masterkey.NewSecretReader(os.Stdin, os.Stdout)
