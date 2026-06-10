package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runI18nPull implements `i18n-pull [<mod>] [<lang>] [--from <target>]
// [--all]`: export a module's translations from a remote Odoo instance
// (over SSH, via the project's connect config or a named target) and write
// the .po into the local repo at <addons>/<mod>/i18n/<lang>.po.
func (sess *session) runI18nPull(ctx context.Context, args []string) {
	sess.startLog("i18n-pull", args)

	lc := &logColorer{}
	stats := &runStats{}
	err := cmd.RunI18nPull(ctx, cmd.I18nPullOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		StreamOut: stats.wrap(func(line string) {
			sess.print(Line{Kind: lc.classify(line), Text: line})
		}),
	})

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
		sess.finalize("i18n-pull", stats.errors, stats.warnings, err)
	case err != nil, stats.errors > 0:
		sess.commandFailureLog("i18n-pull", err, stats.errors, stats.warnings)
	default:
		sess.finalize("i18n-pull", stats.errors, stats.warnings, err)
	}
}
