package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runWatch implements `watch <branch> [--from <t>|--remote] [--interval <sec>]
// [--force]`: poll the branch's ref and, on each fast-forward advance,
// push+deploy the new commits. Blocks until Ctrl+C. Progress renders as
// `echo.watch` log lines; the push/deploy output streams through the shared
// renderer.
func (sess *session) runWatch(ctx context.Context, args []string) {
	lc := &logColorer{}
	stats := &runStats{}
	err := cmd.RunWatch(ctx, cmd.WatchOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("watch"),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
		OnSync: sess.renderSyncTree,
	})

	switch {
	case errors.Is(err, cmd.ErrQuit):
		sess.handleQuit(err)
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("watch", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil:
		sess.commandFailureLog("watch", err, stats.errors, stats.warnings)
	default:
		// Ctrl+C returns nil: RunWatch already emitted the `watch stopped`
		// summary, so this is a clean completion.
		sess.finalize("watch", stats.errors, stats.warnings, nil)
	}
}
