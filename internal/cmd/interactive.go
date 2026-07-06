package cmd

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

// ErrNonInteractive is returned when a command needs a terminal — a fuzzy
// picker or a confirmation prompt — but stdin is not a TTY, e.g. when Echo
// is driven from a script or CI. The one-shot dispatcher maps it to exit
// code 2 (usage error): the caller must be explicit (pass the missing
// argument, or pass --force) instead of relying on an interactive prompt.
var ErrNonInteractive = errors.New("requires a terminal")

// ErrUsage marks a caller mistake (bad flag combination, an argument that
// doesn't resolve) that should map to the script exit code 2 (usage), the
// same bucket as ErrNonInteractive. Wrap validation errors with it so the
// REPL/one-shot layer can distinguish a usage error from an execution
// failure (exit 1).
var ErrUsage = errors.New("usage error")

// stdinIsTTY reports whether stdin is connected to a terminal. It is a
// package-level seam so tests can force the non-interactive path without
// a real TTY (mirrors dockerInspectFn elsewhere).
var stdinIsTTY = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// StdinPiped reports whether stdin is NOT a terminal — i.e. Echo is being
// fed through a pipe or redirection. The REPL uses it to switch `shell`
// into its headless pipe mode (`cat fix.py | echo shell`).
func StdinPiped() bool { return !stdinIsTTY() }

// requireTTY returns a wrapped ErrNonInteractive (with a caller-specific
// hint) when stdin is not a terminal, and nil otherwise. Interactive call
// sites guard on it before showing a picker or a confirm so a TTY-less run
// fails closed instead of blocking forever on input that will never come.
func requireTTY(hint string) error {
	if stdinIsTTY() {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNonInteractive, hint)
}
