package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runPush implements `push [<mod>...] [--from <t>|--remote] [--dirty]
// [--dry-run] [--delete] [--force]`: rsync the selected local modules to the
// remote target's addons dir over SSH. Progress renders as `echo.push` log
// lines; the rsync itemization streams through the shared renderer.
func (sess *session) runPush(ctx context.Context, args []string) {
	lc := &logColorer{}
	stats := &runStats{}
	err := cmd.RunPush(ctx, cmd.PushOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("push"),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
	})

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("push", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil, stats.errors > 0:
		sess.commandFailureLog("push", err, stats.errors, stats.warnings)
	default:
		sess.finalize("push", stats.errors, stats.warnings, nil)
	}
}
