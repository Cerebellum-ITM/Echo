package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runInitRemote implements `init --remote|--from`: write a remote host's
// Echo profile over SSH, register the target and optionally link it.
func (sess *session) runInitRemote(ctx context.Context, args []string) {
	stats := &runStats{}
	_, err := cmd.RunInitRemote(ctx, cmd.InitRemoteOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("init"),
	})
	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("init", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil:
		sess.commandFailureLog("init", err, stats.errors, stats.warnings)
	default:
		sess.finalize("init", stats.errors, stats.warnings, nil)
	}
}
