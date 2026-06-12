package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runDeploy implements `deploy [--from <target>] [--limit N] [--dry-run]
// [--force]`: pick local commits, resolve their modules, and run the
// remote stop → up -d → install/update sequence over SSH. Progress is
// rendered as `echo.deploy` log lines; the remote steps stream through
// the shared Odoo-style line renderer.
func (sess *session) runDeploy(ctx context.Context, args []string) {
	lc := &logColorer{}
	stats := &runStats{}
	err := cmd.RunDeploy(ctx, cmd.DeployOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("deploy"),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
	})

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
		sess.finalize("deploy", stats.errors, stats.warnings, err)
	case err != nil, stats.errors > 0:
		sess.commandFailureLog("deploy", err, stats.errors, stats.warnings)
	default:
		sess.finalize("deploy", stats.errors, stats.warnings, nil)
	}
}
