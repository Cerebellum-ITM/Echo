package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runCheckpoint implements `checkpoint [list|create|rm] [--from <t>]
// [--method db|dump] [--all] [--force] [--json]`: manage the DB checkpoints
// of a remote deploy target. `list` (default) prints a table of the target's
// checkpoints plus the live DB size and free disk; `create` takes a manual
// checkpoint; `rm` deletes one or all. With `list --json` the decorated
// stream is suppressed and a single machine-readable object is written to
// stdout, so the output can be piped into jq.
func (sess *session) runCheckpoint(ctx context.Context, args []string) {
	wantJSON := seqHasFlag(args, "--json")
	lc := &logColorer{}
	stats := &runStats{}

	logFn := sess.cmdOdooLogger("checkpoint")
	streamFn := stats.wrap(func(line string) { sess.emitStreamLine(lc, line) })
	if wantJSON {
		logFn = sess.stderrOdooLogger("checkpoint")
		streamFn = stats.wrap(func(line string) { os.Stderr.WriteString(line + "\n") })
	}

	res, err := cmd.RunCheckpoint(ctx, cmd.CheckpointOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Palette:   sess.palette,
		Log:       logFn,
		StreamOut: streamFn,
	})

	if wantJSON && res.Sub == "list" {
		sess.finishCheckpointJSON(res, stats, err)
		return
	}

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("checkpoint", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil, stats.errors > 0:
		sess.commandFailureLog("checkpoint", err, stats.errors, stats.warnings)
	default:
		sess.finalize("checkpoint", stats.errors, stats.warnings, nil)
	}
}

// finishCheckpointJSON writes the `checkpoint list` summary to stdout (JSON)
// and any error diagnostic to stderr, then sets the script exit code — the
// same stdout/stderr split as the deploy and modstate JSON paths.
func (sess *session) finishCheckpointJSON(res cmd.CheckpointResult, stats *runStats, err error) {
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.checkpoint", "checkpoint failed",
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

	if res.Rows == nil {
		res.Rows = []cmd.CheckpointRow{}
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.checkpoint", "encode failed",
			[]logField{{"err", merr.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
	sess.exitCode = exitOK
}
