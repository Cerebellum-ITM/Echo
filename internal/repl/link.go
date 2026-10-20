package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/docker"
)

// runLink implements `link [<target>] [--show] [--rm]`: bind the current
// project directory to a named connect target (per-project [connect]),
// inspect the binding, or remove it. Progress is rendered as `echo.link`
// log lines; `--show` additionally streams the remote `compose ps`. With
// `--json` the log lines go to stderr and stdout carries only the JSON.
func (sess *session) runLink(ctx context.Context, args []string) {
	lc := &logColorer{}
	stats := &runStats{}
	wantJSON := slices.Contains(args, "--json")
	logFn := sess.cmdOdooLogger("link")
	if wantJSON {
		logFn = sess.stderrOdooLogger("link")
	}
	var show *cmd.LinkShowResult
	err := cmd.RunLink(ctx, cmd.LinkOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     logFn,
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
		OnPS: func(rows []docker.PSContainer, db string) {
			sess.emitPSTableAs(rows, "echo.link.ps", db)
		},
		OnShow: func(res cmd.LinkShowResult) { show = &res },
	})

	// `--list --json` is machine output already written to stdout; the
	// blank line and the completion line of finalize would corrupt it.
	if err == nil && slices.Contains(args, "--list") && wantJSON {
		sess.exitCode = exitOK
		return
	}
	if slices.Contains(args, "--show") && wantJSON {
		sess.finishLinkShowJSON(show, err)
		return
	}

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("link", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil:
		sess.commandFailureLog("link", err, stats.errors, stats.warnings)
	default:
		sess.finalize("link", stats.errors, stats.warnings, nil)
	}
}

// finishLinkShowJSON writes the `link --show --json` report to stdout and any
// error to stderr: usage and non-interactive exit 2, cancelled 3, any other
// failure 1.
func (sess *session) finishLinkShowJSON(res *cmd.LinkShowResult, err error) {
	if err == nil {
		var b []byte
		if b, err = json.Marshal(res); err == nil {
			os.Stdout.Write(append(b, '\n'))
			sess.exitCode = exitOK
			return
		}
	}
	emitOdooLogTo(os.Stderr, "ERROR", "echo.link.error", "link failed",
		[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
	switch {
	case errors.Is(err, cmd.ErrUsage), errors.Is(err, cmd.ErrNonInteractive):
		sess.exitCode = exitUsage
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.exitCode = exitCancelled
	default:
		sess.exitCode = exitError
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

// stderrOdooLogger is cmdOdooLogger routed to stderr — used by `--json`
// command modes so progress lines don't pollute the JSON on stdout.
func (sess *session) stderrOdooLogger(name string) func(level, sub, msg, db string, fields ...[2]string) {
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
		emitOdooLogTo(os.Stderr, level, logger, msg, lf, sess.styles, sess.palette, db)
	}
}
