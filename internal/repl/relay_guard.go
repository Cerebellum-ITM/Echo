package repl

import "github.com/pascualchavez/echo/internal/cmd"

// refusedOnRelay ends a command that cannot run on the Relay env it would
// reach, before it touches the host; see cmd.RelayGuard.
func (sess *session) refusedOnRelay(name string, args []string) bool {
	err := cmd.RelayGuard(sess.cfg, name, args)
	if err == nil {
		return false
	}
	sess.finalize(name, 0, 0, err)
	sess.exitCode = exitUsage
	return true
}
