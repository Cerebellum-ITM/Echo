package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runDBPush implements `db-push <dump> (--to <target> | --remote) …`. It has
// its own handler instead of runDB because usage errors must exit 2 and
// --json keeps stdout for the summary.
func (sess *session) runDBPush(ctx context.Context, args []string) {
	wantJSON := seqHasFlag(args, "--json")
	lc := &logColorer{}
	stats := &runStats{}

	logFn := sess.cmdOdooLogger("db_push")
	streamFn := stats.wrap(func(line string) { sess.emitStreamLine(lc, line) })
	if wantJSON {
		logFn = sess.stderrOdooLogger("db_push")
		streamFn = stats.wrap(func(line string) { os.Stderr.WriteString(line + "\n") })
	}

	res, err := cmd.RunDBPush(ctx, cmd.DBPushOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Palette:   sess.palette,
		Log:       logFn,
		StreamOut: streamFn,
	})
	sess.extras.dbPush = &dbPushFields{sourceDump: res.Dump, checkpoint: res.Checkpoint}
	if r := sess.extras.remote; r != nil && res.DB != "" {
		r.DB = res.DB
	}

	if wantJSON {
		b, merr := json.Marshal(res)
		if merr == nil {
			os.Stdout.Write(b)
			os.Stdout.Write([]byte("\n"))
		}
		if err != nil {
			emitOdooLogTo(os.Stderr, "ERROR", "echo.db_push", "db-push failed",
				[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		}
		sess.exitCode = dbPushExitCode(err)
		return
	}

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("db-push", stats.errors, stats.warnings, err)
		sess.exitCode = dbPushExitCode(err)
	case err != nil:
		sess.commandFailureLog("db-push", err, stats.errors, stats.warnings)
	default:
		sess.finalize("db-push", stats.errors, stats.warnings, nil)
	}
}

func dbPushExitCode(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, cmd.ErrUsage), errors.Is(err, cmd.ErrNonInteractive):
		return exitUsage
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		return exitCancelled
	}
	return exitError
}
