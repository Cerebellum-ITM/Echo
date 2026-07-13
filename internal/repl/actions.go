package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runActions implements `actions [list|add|edit|rm] [<name>] [--from <t>]
// [--remote] [--json] [--force]`: manage the project's `[[deploy.actions]]`
// interactively. `list` (default) prints the effective action table; `add`/
// `edit` open the wizard; `rm` deletes one. Mutations persist to the local
// project profile and optionally upload to the server.
func (sess *session) runActions(ctx context.Context, args []string) {
	wantJSON := seqHasFlag(args, "--json")
	lc := &logColorer{}
	stats := &runStats{}

	logFn := sess.cmdOdooLogger("actions")
	streamFn := stats.wrap(func(line string) { sess.emitStreamLine(lc, line) })
	if wantJSON {
		logFn = sess.stderrOdooLogger("actions")
		streamFn = stats.wrap(func(line string) { os.Stderr.WriteString(line + "\n") })
	}

	res, err := cmd.RunActions(ctx, cmd.ActionsOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Palette:   sess.palette,
		Log:       logFn,
		StreamOut: streamFn,
	})

	if wantJSON && res.Sub == "list" {
		sess.finishActionsJSON(res, err)
		return
	}

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("actions", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil, stats.errors > 0:
		sess.commandFailureLog("actions", err, stats.errors, stats.warnings)
	default:
		sess.finalize("actions", stats.errors, stats.warnings, nil)
	}
}

// finishActionsJSON writes the `actions list --json` payload to stdout and any
// error to stderr, then sets the script exit code.
func (sess *session) finishActionsJSON(res cmd.ActionsResult, err error) {
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.actions", "actions failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		switch {
		case errors.Is(err, cmd.ErrUsage), errors.Is(err, cmd.ErrNonInteractive):
			sess.exitCode = exitUsage
		case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
			sess.exitCode = exitCancelled
		default:
			sess.exitCode = exitError
		}
		return
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.actions", "encode failed",
			[]logField{{"err", merr.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
	sess.exitCode = exitOK
}
