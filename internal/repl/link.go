package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/docker"
)

// runLink implements `link [<target>] [--show] [--rm]`: bind the current
// project directory to a named connect target (per-project [connect]),
// inspect the binding, or remove it. Progress is rendered as `echo.link`
// log lines; `--show` additionally streams the remote `compose ps`.
func (sess *session) runLink(ctx context.Context, args []string) {
	lc := &logColorer{}
	stats := &runStats{}
	err := cmd.RunLink(ctx, cmd.LinkOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("link"),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
		OnPS: func(rows []docker.PSContainer, db string) {
			sess.emitPSTableAs(rows, "echo.link.ps", db)
		},
	})

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive):
		sess.finalize("link", stats.errors, stats.warnings, err)
	case err != nil:
		sess.commandFailureLog("link", err, stats.errors, stats.warnings)
	default:
		sess.finalize("link", stats.errors, stats.warnings, nil)
	}
}

// cmdOdooLogger renders a command's progress events as Odoo-style log
// lines under `echo.<name>[.sub]`. sub == "system" routes to the shared
// `echo.system.status` logger (the cross-command system-status line); an
// empty db falls back to the session's configured database.
func (sess *session) cmdOdooLogger(name string) func(level, sub, msg, db string, fields ...[2]string) {
	return func(level, sub, msg, db string, fields ...[2]string) {
		logger := "echo." + name
		switch {
		case sub == "system":
			logger = "echo.system.status"
		case sub != "":
			logger += "." + sub
		}
		if db == "" {
			db = sess.cfg.DBName
		}
		lf := make([]logField, 0, len(fields))
		for _, f := range fields {
			lf = append(lf, logField{f[0], f[1]})
		}
		emitOdooLog(level, logger, msg, lf, sess.styles, sess.palette, db)
	}
}
