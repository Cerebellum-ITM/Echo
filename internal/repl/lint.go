package repl

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/odoolint"
)

// runLint implements `lint [<mod>...|<path>] [--json]`: check Odoo XML the
// way the data loader does, offline, and report findings as Echo log
// lines. Blocking findings (a defect in a manifest-listed file) are ERROR
// and set the exit code; everything else is WARNING and does not.
//
// The exit code is what makes this usable as a git hook, an editor
// PostToolUse hook and a CI step — which is the point of the command. The
// two failures it catches both landed in production despite a written
// note saying not to write them: a check that has to be remembered is not
// a check.
func (sess *session) runLint(args []string) {
	wantJSON := false
	for _, a := range args {
		if a == "--json" {
			wantJSON = true
			break
		}
	}

	res, err := cmd.RunLint(cmd.LintOpts{
		Cfg:  sess.cfg,
		Root: sess.projectDir,
		Args: args,
	})
	if err != nil {
		if wantJSON {
			emitOdooLogTo(os.Stderr, "ERROR", "echo.lint", "lint failed",
				[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitCodeFor(err)
			return
		}
		sess.finalize("lint", 1, 0, err)
		sess.exitCode = exitCodeFor(err)
		return
	}

	if wantJSON {
		sess.emitLintJSON(res)
		return
	}
	sess.emitLintLines(res)
}

// emitLintLines renders one log line per finding plus a summary, in the
// shape `compare` and `modinfo` already close with.
func (sess *session) emitLintLines(res cmd.LintResult) {
	root := sess.projectDir

	// Coverage caveats come first: they change what a clean run below
	// actually means, so they must not be read after the verdict.
	for _, pass := range res.SkippedPasses {
		emitOdooLog("WARNING", "echo.lint", "validator unavailable, pass skipped",
			[]logField{{"pass", pass}, {"needs", "xmllint"}},
			sess.styles, sess.palette, sess.cfg.DBName)
	}
	if !res.GrammarExact {
		emitOdooLog("WARNING", "echo.lint", "no grammar for the configured Odoo version",
			[]logField{{"configured", sess.cfg.OdooVersion}, {"used", res.GrammarMajor}},
			sess.styles, sess.palette, sess.cfg.DBName)
	}

	for _, f := range res.Findings {
		level := "ERROR"
		if f.Kind == odoolint.KindWarn {
			level = "WARNING"
		}
		emitOdooLog(level, "echo.lint", f.Message, []logField{
			{"file", cmd.RelPath(root, f.File)},
			{"line", strconv.Itoa(f.Line)},
			{"rule", f.Rule},
		}, sess.styles, sess.palette, sess.cfg.DBName)
	}

	fields := []logField{
		{"files", strconv.Itoa(res.Files)},
		{"errors", strconv.Itoa(res.Errors())},
		{"warnings", strconv.Itoa(res.Warnings())},
		{"grammar", res.GrammarMajor},
	}
	if len(res.Modules) > 0 {
		fields = append([]logField{{"modules", strings.Join(res.Modules, ",")}}, fields...)
	}

	level, msg := "INFO", "no problems found"
	if res.Errors() > 0 {
		level, msg = "ERROR", "problems found"
	} else if res.Warnings() > 0 {
		level, msg = "WARNING", "problems found outside the loaded set"
	}
	emitOdooLog(level, "echo.lint", msg, fields, sess.styles, sess.palette, sess.cfg.DBName)

	sess.exitCode = exitOK
	if res.Errors() > 0 {
		sess.exitCode = exitError
	}
}

// emitLintJSON writes one record per finding plus a summary object to
// stdout only, so `lint --json | jq` works and diagnostics stay on stderr.
func (sess *session) emitLintJSON(res cmd.LintResult) {
	root := sess.projectDir

	type findingJSON struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Kind    string `json:"kind"`
		Rule    string `json:"rule"`
		Message string `json:"message"`
	}
	type summaryJSON struct {
		Files         int      `json:"files"`
		Errors        int      `json:"errors"`
		Warnings      int      `json:"warnings"`
		Modules       []string `json:"modules"`
		Grammar       string   `json:"grammar"`
		GrammarExact  bool     `json:"grammar_exact"`
		SkippedPasses []string `json:"skipped_passes"`
	}
	type outJSON struct {
		Findings []findingJSON `json:"findings"`
		Summary  summaryJSON   `json:"summary"`
	}

	out := outJSON{
		Findings: make([]findingJSON, 0, len(res.Findings)),
		Summary: summaryJSON{
			Files:         res.Files,
			Errors:        res.Errors(),
			Warnings:      res.Warnings(),
			Modules:       res.Modules,
			Grammar:       res.GrammarMajor,
			GrammarExact:  res.GrammarExact,
			SkippedPasses: res.SkippedPasses,
		},
	}
	if out.Summary.Modules == nil {
		out.Summary.Modules = []string{}
	}
	if out.Summary.SkippedPasses == nil {
		out.Summary.SkippedPasses = []string{}
	}
	for _, f := range res.Findings {
		out.Findings = append(out.Findings, findingJSON{
			File:    cmd.RelPath(root, f.File),
			Line:    f.Line,
			Kind:    string(f.Kind),
			Rule:    f.Rule,
			Message: f.Message,
		})
	}

	b, err := json.Marshal(out)
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.lint", "encode failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))

	sess.exitCode = exitOK
	if res.Errors() > 0 {
		sess.exitCode = exitError
	}
}
