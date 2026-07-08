package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runDeploy implements `deploy [--from <target>] [--limit N] [--dry-run]
// [--force] [--auto|--modules m1,m2] [--json]`: resolve the deploy targets
// (interactive commit picker by default, or headless via --auto/--modules),
// then run the remote stop → up -d → install/update sequence over SSH.
// Progress renders as `echo.deploy` log lines; the remote steps stream
// through the shared Odoo-style renderer. With --json the decorated stream is
// routed to stderr and a single machine-readable summary object is written to
// stdout, so the output can be piped into jq.
func (sess *session) runDeploy(ctx context.Context, args []string) {
	wantJSON := seqHasFlag(args, "--json")
	lc := &logColorer{}
	stats := &runStats{}

	logFn := sess.cmdOdooLogger("deploy")
	streamFn := stats.wrap(func(line string) { sess.emitStreamLine(lc, line) })
	if wantJSON {
		// Keep stdout clean for the JSON object: progress + streamed remote
		// lines go to stderr instead of the session's stdout renderer.
		logFn = sess.stderrOdooLogger("deploy")
		streamFn = stats.wrap(func(line string) { os.Stderr.WriteString(line + "\n") })
	}

	// The --push change tree renders to stdout; suppress it under --json so
	// the machine-readable object stays the only thing on stdout.
	var onSync func([]cmd.FileChange)
	if !wantJSON {
		onSync = sess.renderSyncTree
	}
	res, err := cmd.RunDeploy(ctx, cmd.DeployOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Palette:   sess.palette,
		Log:       logFn,
		StreamOut: streamFn,
		OnSync:    onSync,
	})

	if wantJSON {
		sess.finishDeployJSON(res, stats, err)
		return
	}

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("deploy", stats.errors, stats.warnings, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage // finalize maps a plain err to 1; usage is 2
		}
	case err != nil, stats.errors > 0:
		sess.commandFailureLog("deploy", err, stats.errors, stats.warnings)
	default:
		sess.finalize("deploy", stats.errors, stats.warnings, nil)
	}
}

// finishDeployJSON writes the deploy summary to stdout (JSON) and any error
// diagnostic to stderr, then sets the script exit code. Mirrors the
// modstate --json split: stdout carries only the machine-readable object.
func (sess *session) finishDeployJSON(res cmd.DeployResult, stats *runStats, err error) {
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.deploy", "deploy failed",
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

	type deployJSON struct {
		Target   string             `json:"target"`
		DB       string             `json:"db"`
		Modules  []cmd.DeployModule `json:"modules"`
		Skipped  int                `json:"skipped"`
		Errors   int                `json:"errors"`
		Warnings int                `json:"warnings"`
		Planned  bool               `json:"planned,omitempty"`
	}
	out := deployJSON{
		Target:   res.Target,
		DB:       res.DB,
		Modules:  res.Modules,
		Skipped:  res.Skipped,
		Errors:   stats.errors,
		Warnings: stats.warnings,
		Planned:  res.Planned,
	}
	if out.Modules == nil {
		out.Modules = []cmd.DeployModule{}
	}
	b, merr := json.Marshal(out)
	if merr != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.deploy", "encode failed",
			[]logField{{"err", merr.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
	if stats.errors > 0 {
		sess.exitCode = exitError
		return
	}
	sess.exitCode = exitOK
}
