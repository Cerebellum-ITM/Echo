package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runSideload implements `sideload [<module|path>…] [--from <target>]
// [--dry-run] [--allow-prod] [--resync] [--no-lint] [--json]`, the library
// picker when no module is named, and the read-only `--list`, `--available`
// and `--library*` forms: pack the addon(s), stream the changed ones into the remote
// `.echo/sideload/`, wire them through an Echo-managed compose override, then
// run the remote stop → up -d → install/update sequence over SSH. With --json
// the decorated stream goes to stderr and one result object to stdout.
func (sess *session) runSideload(ctx context.Context, args []string) {
	wantJSON := seqHasFlag(args, "--json")
	lc := &logColorer{}
	stats := &runStats{}

	logFn := sess.cmdOdooLogger("sideload")
	streamFn := stats.wrap(func(line string) { sess.emitStreamLine(lc, line) })
	if wantJSON {
		logFn = sess.stderrOdooLogger("sideload")
		streamFn = stats.wrap(func(line string) { os.Stderr.WriteString(line + "\n") })
	}

	res, err := cmd.RunSideload(ctx, cmd.SideloadOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Palette:   sess.palette,
		Log:       logFn,
		StreamOut: streamFn,
	})

	if wantJSON {
		sess.finishSideloadJSON(res, stats, err)
		return
	}

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("sideload", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil, stats.errors > 0:
		sess.commandFailureLog("sideload", err, stats.errors, stats.warnings)
	default:
		sess.finalize("sideload", stats.errors, stats.warnings, nil)
	}
}

// finishSideloadJSON writes the sideload result to stdout and any error
// diagnostic to stderr, then sets the script exit code, as finishDeployJSON.
func (sess *session) finishSideloadJSON(res cmd.SideloadResult, stats *runStats, err error) {
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.sideload", "sideload failed",
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

	if res.Addons == nil {
		res.Addons = []cmd.SideloadModule{}
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.sideload", "encode failed",
			[]logField{{"err", merr.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.WriteString("\n")
	if stats.errors > 0 {
		sess.exitCode = exitError
		return
	}
	sess.exitCode = exitOK
}
