package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runI18nPull implements `i18n-pull [<mod>] [<lang>] [--from <target>]
// [--all] [--installed]`: export a module's translations from a remote Odoo
// instance (over SSH) and write the .po into the local repo. Progress is
// rendered as `echo.i18n-pull` log lines, matching connect's style.
func (sess *session) runI18nPull(ctx context.Context, args []string) {
	// No generic `.start` line here: the bare `i18n-pull` echo carries no
	// information. The first meaningful line comes from RunI18nPull itself
	// ("selecting remote target" before the picker, or "target resolved"
	// when a target is given), followed by the system-status headline.
	err := cmd.RunI18nPull(ctx, cmd.I18nPullOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.i18nPullLogger(),
	})

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
		sess.finalize("i18n-pull", 0, 0, err)
	case err != nil:
		sess.commandFailureLog("i18n-pull", err, 0, 0)
	default:
		sess.finalize("i18n-pull", 0, 0, nil)
	}
}

// i18nPullLogger renders i18n-pull progress events as Odoo-style log lines
// under `echo.i18n-pull[.sub]` via the shared per-command logger.
func (sess *session) i18nPullLogger() func(level, sub, msg, db string, fields ...[2]string) {
	return sess.cmdOdooLogger("i18n-pull")
}
