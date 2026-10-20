package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/cmd"
)

// runProbe implements `probe`: a read-only question to the live database.
// Without --json it prints one echo.probe line and the result as indented
// JSON; with --json stdout carries only the payload and every log line goes
// to stderr. Read-only, so nothing is auto-copied.
func (sess *session) runProbe(ctx context.Context, args []string) {
	wantJSON := seqHasFlag(args, "--json")
	lc := &logColorer{}
	logFn := sess.cmdOdooLogger("probe")
	streamFn := func(line string) { sess.emitStreamLine(lc, line) }
	if wantJSON {
		logFn = sess.stderrOdooLogger("probe")
		streamFn = func(line string) { os.Stderr.WriteString(line + "\n") }
	}

	res, err := cmd.RunProbe(ctx, cmd.ProbeOpts{
		Cfg:       sess.cfg,
		Root:      sess.projectDir,
		Args:      args,
		Stdin:     os.Stdin,
		Palette:   sess.palette,
		Log:       logFn,
		StreamOut: streamFn,
	})
	if wantJSON {
		sess.finishProbeJSON(res, err)
		return
	}
	if res.Payload == nil {
		sess.finalize("probe", 0, 0, err)
		sess.exitCode = probeExitCode(err)
		return
	}
	sess.renderProbe(res)
}

// probeExitCode maps a probe error to the script exit code: a caller mistake
// is 2, anything finalize does not map otherwise is 1.
func probeExitCode(err error) int {
	if errors.Is(err, cmd.ErrUsage) || errors.Is(err, cmd.ErrNoDB) {
		return exitUsage
	}
	return scriptExitCode(err, 0)
}

func (sess *session) finishProbeJSON(res cmd.ProbeResult, err error) {
	db := res.DB
	if db == "" {
		db = sess.cfg.DBName
	}
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.probe", "probe failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, db)
	}
	sess.exitCode = probeExitCode(err)
	if res.Payload == nil {
		return
	}
	b, merr := json.Marshal(res.Payload)
	if merr != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.probe", "encode failed",
			[]logField{{"err", merr.Error()}}, sess.styles, sess.palette, db)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(append(b, '\n'))
}

// renderProbe prints a decoded probe: the result as indented JSON on success,
// the exception and its traceback on failure, then what the code printed.
func (sess *session) renderProbe(res cmd.ProbeResult) {
	duration := res.Duration.Round(time.Millisecond).String()
	if res.OK {
		emitOdooLog("INFO", "echo.probe", "probe result",
			[]logField{{"kind", res.Kind}, {"duration", duration}},
			sess.styles, sess.palette, res.DB)
		var pretty bytes.Buffer
		json.Indent(&pretty, res.Result, "", "  ")
		for _, line := range strings.Split(pretty.String(), "\n") {
			sess.print(Line{Kind: "out", Text: line})
		}
		sess.exitCode = exitOK
	} else {
		e := res.Error
		fields := []logField{{"type", e.Type}, {"kind", res.Kind}, {"duration", duration}}
		if e.File != "" {
			fields = append(fields, logField{"file", e.File}, logField{"line", strconv.Itoa(e.Line)})
		}
		emitOdooLog("ERROR", "echo.probe", e.Message, fields, sess.styles, sess.palette, res.DB)
		for _, line := range strings.Split(strings.TrimRight(e.Traceback, "\n"), "\n") {
			sess.print(Line{Kind: "err", Text: line})
		}
		sess.exitCode = exitError
	}
	if stdout := strings.TrimRight(res.Stdout, "\n"); stdout != "" {
		sess.print(Line{Kind: "dim", Text: "stdout"})
		for _, line := range strings.Split(stdout, "\n") {
			sess.print(Line{Kind: "out", Text: line})
		}
	}
}
