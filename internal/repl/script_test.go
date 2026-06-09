package repl

import (
	"errors"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// TestIsScriptCommand: every dispatchable command is one-shot eligible
// except the REPL-only meta tokens.
func TestIsScriptCommand(t *testing.T) {
	for _, n := range dispatchNames {
		want := n != "clear" // clear is REPL-only; the rest are script-eligible
		if got := IsScriptCommand(n); got != want {
			t.Errorf("IsScriptCommand(%q) = %v, want %v", n, got, want)
		}
	}
	for _, n := range []string{"clear", "exit", "quit", "bogus"} {
		if IsScriptCommand(n) {
			t.Errorf("IsScriptCommand(%q) = true, want false", n)
		}
	}
}

func TestScriptExitCode(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		errCount int
		want     int
	}{
		{"success", nil, 0, exitOK},
		{"error lines counted", nil, 3, exitError},
		{"generic error", errors.New("boom"), 0, exitError},
		{"cancelled", cmd.ErrCancelled, 0, exitCancelled},
		{"user aborted", huh.ErrUserAborted, 0, exitCancelled},
		{"non-interactive", cmd.ErrNonInteractive, 0, exitUsage},
		{"non-interactive over err count", cmd.ErrNonInteractive, 5, exitUsage},
	}
	for _, c := range cases {
		if got := scriptExitCode(c.err, c.errCount); got != c.want {
			t.Errorf("%s: scriptExitCode(%v, %d) = %d, want %d", c.name, c.err, c.errCount, got, c.want)
		}
	}
}
