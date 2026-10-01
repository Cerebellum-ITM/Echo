package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runDoctor implements `doctor [--from <t> | --remote] [--json]`: one
// read-only readiness report of a remote target, one `echo.doctor.<check>`
// line per check. Any failed check makes the run finish with errors (exit 1);
// warnings alone exit 0. With --json the lines go to stderr and one object
// to stdout.
func (sess *session) runDoctor(ctx context.Context, args []string) {
	wantJSON := seqHasFlag(args, "--json")
	logFn := sess.cmdOdooLogger("doctor")
	if wantJSON {
		logFn = sess.stderrOdooLogger("doctor")
	}
	res, err := cmd.RunDoctor(ctx, cmd.DoctorOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     logFn,
	})
	if wantJSON {
		sess.finishDoctorJSON(res, err)
		return
	}
	sess.finalize("doctor", res.Counts.Failed, res.Counts.Warn, err)
	if errors.Is(err, cmd.ErrUsage) {
		sess.exitCode = exitUsage
	}
}

// finishDoctorJSON writes the report to stdout and any error to stderr, then
// sets the exit code: usage and non-interactive 2, cancelled 3, an error or a
// failed check 1.
func (sess *session) finishDoctorJSON(res cmd.DoctorResult, err error) {
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.doctor", "doctor failed",
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
	if res.Checks == nil {
		res.Checks = []cmd.DoctorCheck{}
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.doctor", "encode failed",
			[]logField{{"err", merr.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
	sess.exitCode = exitOK
	if res.Counts.Failed > 0 {
		sess.exitCode = exitError
	}
}
