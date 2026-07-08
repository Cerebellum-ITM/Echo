package repl

import (
	"context"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// RunOnce dispatches a single command non-interactively and returns the
// process exit code (see the exit* constants: 0 success, 1 execution
// error, 2 usage / non-interactive, 3 cancelled). It backs the
// `echo <cmd> [args]` script mode: the command streams its output through
// the same Odoo-style render and start/finalize frame the REPL uses, then
// the process exits with the recorded code.
func RunOnce(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config, name string, args []string) int {
	sess, _ := newSession(s, p, project, id, stage, version, themeName, username, cwd, cfg)
	sess.pruneCmdLogs()
	sess.dispatchParsed(context.Background(), name, args)
	return sess.exitCode
}

// IsScriptCommand reports whether name can be run as a one-shot command
// (`echo <cmd>`). Every command routed by dispatch qualifies except the
// REPL-only meta tokens, which only make sense inside the prompt loop.
// `connect` is handled by its own projectless path in main before this is
// consulted, so it need not be special-cased here.
func IsScriptCommand(name string) bool {
	switch name {
	case "clear", "exit", "quit":
		return false
	}
	for _, n := range dispatchNames {
		if n == name {
			return true
		}
	}
	return false
}
