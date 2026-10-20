package repl

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/odoo"
)

// testTallyFields are the counts the closing line of `test` carries. The
// test error count is `errored` because `errors=` already counts the run's
// ERROR lines on the same line.
func testTallyFields(t odoo.TestTally) []logField {
	return []logField{
		{"run", strconv.Itoa(t.Run)},
		{"failed", strconv.Itoa(t.Failed)},
		{"errored", strconv.Itoa(t.Errors)},
	}
}

// emitTestFailures prints one line per failing test after the closing
// line, so the failures are readable without scrolling the run.
func (sess *session) emitTestFailures(failures []*odoo.TestFailure) {
	for _, f := range failures {
		fields := []logField{{"test", f.Test}, {"module", f.Module}}
		if f.File != "" {
			fields = append(fields, logField{"file", f.File}, logField{"line", strconv.Itoa(f.Line)})
		}
		emitOdooLog("ERROR", "echo.test.failure", f.Kind, fields,
			sess.styles, sess.palette, sess.cfg.DBName)
	}
}

type moduleTestsJSON struct {
	odoo.TestTally
	Failures []*odoo.TestFailure `json:"failures"`
}

type migrationJSON struct {
	Module  string   `json:"module"`
	Version string   `json:"version"`
	Phases  []string `json:"phases"`
}

type moduleRunJSON struct {
	Command           string            `json:"command"`
	Target            *string           `json:"target"`
	DB                string            `json:"db"`
	Modules           []string          `json:"modules"`
	Exit              int               `json:"exit"`
	Error             string            `json:"error,omitempty"`
	DurationMS        int64             `json:"duration_ms"`
	Errors            []*odoo.LogError  `json:"errors"`
	Warnings          []odoo.LogWarning `json:"warnings"`
	WarningsTruncated int               `json:"warnings_truncated"`
	Tests             *moduleTestsJSON  `json:"tests,omitempty"`
	Migrations        []migrationJSON   `json:"migrations"`
}

// finishModulesJSON closes a module command run with --json: the outcome
// object on stdout, the failure diagnostic on stderr. A run that never
// started (usage error, missing TTY, cancel) prints nothing on stdout. It
// never auto-copies: the object is the payload.
func (sess *session) finishModulesJSON(name string, resolved []string, err error, stats *runStats,
	outcome *odoo.Outcome, migs []migration, elapsed time.Duration) {
	if sess.handleQuit(err) {
		return
	}
	sess.lastErrors, sess.lastWarnings = stats.errors, stats.warnings
	if err != nil {
		fields := []logField{{"err", err.Error()}}
		var hinted interface{ Hint() string }
		if errors.As(err, &hinted) {
			fields = append(fields, logField{"hint", hinted.Hint()})
		}
		emitOdooLogTo(os.Stderr, "ERROR", failureLogger(name, resolved), name+" failed",
			fields, sess.styles, sess.palette, sess.cfg.DBName)
	}
	switch {
	case errors.Is(err, cmd.ErrUsage), errors.Is(err, cmd.ErrNonInteractive):
		sess.exitCode = exitUsage
		return
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
		sess.exitCode = exitCancelled
		return
	}
	sess.exitCode = scriptExitCode(err, stats.errors)

	out := moduleRunJSON{
		Command:           name,
		DB:                sess.cfg.DBName,
		Modules:           []string{},
		Exit:              sess.exitCode,
		DurationMS:        elapsed.Milliseconds(),
		Errors:            outcome.Errors,
		Warnings:          outcome.Warnings,
		WarningsTruncated: outcome.WarningsTruncated,
		Migrations:        []migrationJSON{},
	}
	if err != nil {
		out.Error = err.Error()
	}
	if r := sess.extras.remote; r != nil {
		out.DB = r.DB
		if r.Target != "" {
			out.Target = &r.Target
		}
	}
	for _, m := range resolved {
		if m == "--all" {
			m = "all"
		}
		out.Modules = append(out.Modules, m)
	}
	if out.Errors == nil {
		out.Errors = []*odoo.LogError{}
	}
	if out.Warnings == nil {
		out.Warnings = []odoo.LogWarning{}
	}
	if name == "test" || outcome.Tallied {
		out.Tests = &moduleTestsJSON{TestTally: outcome.Tests, Failures: outcome.Failures}
		if out.Tests.Failures == nil {
			out.Tests.Failures = []*odoo.TestFailure{}
		}
	}
	for _, mg := range migs {
		out.Migrations = append(out.Migrations, migrationJSON{Module: mg.module, Version: mg.version, Phases: mg.phases})
	}

	b, merr := json.Marshal(out)
	if merr != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo."+name, "encode failed",
			[]logField{{"err", merr.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(append(b, '\n'))
}
