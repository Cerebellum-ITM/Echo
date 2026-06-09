package repl

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
)

// stripFlag removes every occurrence of flag from args, reporting whether
// it was present. Used for session-only `--last` handling.
func stripFlag(args []string, flag string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == flag {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// runModinfo implements `modinfo [<mod>] [--copy] [--last]`: compare the
// version Odoo recorded as installed (ir_module_module.latest_version +
// state) against the module's manifest version and emit a one-line verdict
// as an `echo.modinfo` log line. The verdict drives the log level. `--last`
// replays the session's last modinfo target (in-memory only).
func (sess *session) runModinfo(ctx context.Context, args []string) {
	args, last := stripFlag(args, "--last")
	if last {
		if sess.lastModinfoModule == "" {
			emitOdooLog("WARNING", "echo.modinfo", "no previous modinfo this session",
				nil, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitUsage
			return
		}
		args = append([]string{sess.lastModinfoModule}, args...)
	}

	res, err := cmd.RunModinfo(ctx, cmd.ModinfoOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
	})
	if err != nil {
		switch {
		case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
			errors.Is(err, cmd.ErrNonInteractive):
			sess.finalize("modinfo", 0, 0, err)
		default:
			sess.finalize("modinfo", 1, 0, err)
		}
		return
	}
	sess.lastModinfoModule = res.Module

	level := "INFO"
	switch res.Status {
	case "update pending", "db ahead", "not installed", "no version":
		level = "WARNING"
	}

	dbField := res.DBVersion
	if dbField == "" {
		dbField = "-"
	}
	stateField := res.DBState
	if stateField == "" {
		stateField = "absent"
	}
	manField := res.Manifest
	if manField == "" {
		manField = "-"
	}

	fields := []logField{
		{"module", res.Module},
		{"db", dbField},
		{"state", stateField},
		{"manifest", manField},
		{"status", res.Status},
	}

	if res.Copy {
		plain := fmt.Sprintf("module=%s db=%s state=%s manifest=%s status=%s",
			res.Module, dbField, stateField, manField, res.Status)
		if err := clipboard.WriteAll(plain); err != nil {
			emitOdooLog("ERROR", "echo.modinfo", "copy failed: "+err.Error(),
				nil, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitError
			return
		}
		fields = append(fields, logField{"copied", "true"})
	}

	emitOdooLog(level, "echo.modinfo", "module inspected", fields,
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}
