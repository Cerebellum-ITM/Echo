package repl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"

	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/odoo"
)

type logErrorsJSON struct {
	Scanned int              `json:"scanned"`
	Errors  []*odoo.LogError `json:"errors"`
}

// runLogErrors implements `logs --errors [-n N] [-t N] [--json]`: the last
// errors of the Odoo service log, each with its traceback. It is an
// inspection, so finding errors still exits 0; with --json the object is
// the only thing on stdout.
func (sess *session) runLogErrors(ctx context.Context, args []string) {
	wantJSON := slices.Contains(args, "--json")
	logOut := io.Writer(os.Stdout)
	logFn := sess.cmdOdooLogger("logs")
	if wantJSON {
		logOut = os.Stderr
		logFn = sess.stderrOdooLogger("logs")
	}

	res, err := cmd.RunLogErrors(ctx, cmd.DockerOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     logFn,
	})
	if err != nil {
		if sess.handleQuit(err) {
			return
		}
		emitOdooLogTo(logOut, "ERROR", failureLogger("logs", nil), "logs failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = scriptExitCode(err, 0)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
		return
	}
	sess.exitCode = exitOK

	if wantJSON {
		out := logErrorsJSON{Scanned: res.Scanned, Errors: res.Errors}
		if out.Errors == nil {
			out.Errors = []*odoo.LogError{}
		}
		b, _ := json.Marshal(out)
		os.Stdout.Write(append(b, '\n'))
		return
	}

	if len(res.Errors) == 0 {
		emitOdooLog("INFO", "echo.logs", "no errors in the last "+strconv.Itoa(res.Scanned)+" lines",
			nil, sess.styles, sess.palette, sess.cfg.DBName)
		return
	}
	lc := &logColorer{}
	for _, e := range res.Errors {
		for _, line := range e.Lines {
			sess.emitStreamLine(lc, line)
		}
	}
	sess.print(Line{Kind: "out", Text: ""})
	emitOdooLog("INFO", "echo.logs", "errors listed",
		[]logField{{"errors", strconv.Itoa(len(res.Errors))}, {"scanned", strconv.Itoa(res.Scanned)}},
		sess.styles, sess.palette, sess.cfg.DBName)
}
