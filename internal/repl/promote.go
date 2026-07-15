package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runPromote implements `promote` — the local worktree → accumulation-branch
// funnel. It moves the current worktree's dirty patch (by folder) or selected
// commits (from another branch) into the destination branch's worktree, never
// touching a remote. Dirty changes land uncommitted; commits land via
// cherry-pick. The file changes render as a colored tree via OnSync.
func (sess *session) runPromote(ctx context.Context, args []string) {
	err := cmd.RunPromote(ctx, cmd.PromoteOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("promote"),
		OnSync:  sess.renderSyncTree,
	})

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage),
		errors.Is(err, cmd.ErrQuit):
		sess.finalize("promote", 0, 0, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil:
		sess.commandFailureLog("promote", err, 0, 0)
	default:
		sess.finalize("promote", 0, 0, nil)
	}
}
