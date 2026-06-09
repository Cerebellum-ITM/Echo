package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// RunRecipe reads a recipe — one Echo command per line — and runs each
// step through the one-shot dispatch, stopping at the first step that
// exits non-zero (fail-fast) unless --continue-on-error is passed. The
// recipe path may be `-` or omitted to read from stdin. Returns the
// process exit code: the failing step's code under fail-fast, exitError
// if any step failed under --continue-on-error, else exitOK.
func RunRecipe(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config, args []string) int {
	path, continueOnError, logDest, logEnabled, err := parseRecipeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "echo run: "+err.Error())
		return exitUsage
	}

	steps, err := readRecipe(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "echo run: "+err.Error())
		return exitUsage
	}

	sess, _ := newSession(s, p, project, id, stage, version, themeName, username, cwd, cfg)
	ctx := context.Background()

	// Capture the transcript to a file when --log is given. A failure to
	// open the log must not abort the run — warn and carry on without it.
	if logEnabled {
		if dest, closeLog, ok := sess.openRunLog(logDest, path); ok {
			defer closeLog()
			defer func() { sess.runLog("INFO", "log written", logField{"path", dest}) }()
		}
	}

	runStep := func(name string, sargs []string) stepOutcome {
		start := time.Now()
		sess.dispatchParsed(ctx, name, sargs)
		return stepOutcome{
			code:     sess.exitCode,
			errors:   sess.lastErrors,
			warnings: sess.lastWarnings,
			duration: time.Since(start),
		}
	}
	return runRecipeSteps(steps, continueOnError, runStep, sess.runLog)
}

// openRunLog resolves the log destination, creates the file, sets the
// package-level run-log sink, and returns (dest, closer, ok). When ok is
// false the caller proceeds without logging. The closer flushes, closes,
// and clears the sink. `dest` empty means the default location under
// ~/.config/echo/run-logs/.
func (sess *session) openRunLog(dest, recipePath string) (string, func(), bool) {
	if dest == "" {
		dir, err := config.RunLogsDir()
		if err != nil {
			sess.runLog("WARNING", "could not resolve run-logs dir", logField{"err", err.Error()})
			return "", nil, false
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			sess.runLog("WARNING", "could not create run-logs dir", logField{"err", err.Error()})
			return "", nil, false
		}
		dest = filepath.Join(dir, runLogName(recipePath))
	}

	f, err := os.Create(dest)
	if err != nil {
		sess.runLog("WARNING", "could not open log file", logField{"path", dest}, logField{"err", err.Error()})
		return "", nil, false
	}
	w := bufio.NewWriter(f)
	io.WriteString(w, "# echo run "+recipeLabel(recipePath)+" — "+time.Now().Format(time.RFC3339)+"\n")
	runLogSink = w
	closer := func() {
		w.Flush()
		f.Close()
		runLogSink = nil
	}
	return dest, closer, true
}

// runLogName builds the default log filename: <timestamp>-<recipe>.log.
func runLogName(recipePath string) string {
	return time.Now().Format("20060102-150405") + "-" + recipeLabel(recipePath) + ".log"
}

// recipeLabel is the recipe's basename without extension, or "stdin" when
// reading from stdin (`-` or empty path).
func recipeLabel(recipePath string) string {
	if recipePath == "" || recipePath == "-" {
		return "stdin"
	}
	base := filepath.Base(recipePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// runLog emits an `echo.run` orchestration line in Echo's Odoo log style.
func (sess *session) runLog(level, msg string, fields ...logField) {
	emitOdooLog(level, "echo.run", msg, fields, sess.styles, sess.palette, sess.cfg.DBName)
}

// stepOutcome is one recipe step's result, captured by the runStep
// closure after dispatchParsed: the exit code plus the command's runStats
// (surfaced on the session) and the wall-clock duration.
type stepOutcome struct {
	code     int
	errors   int
	warnings int
	duration time.Duration
}

// runRecipeSteps is the fail-fast (or continue-on-error) loop, decoupled
// from session/IO so it can be tested with a stubbed step runner. runStep
// returns each step's outcome; log emits the live progress lines, the
// per-step recap, and the totals line. The process exit code is unchanged
// from the bare-int era: all-pass → exitOK, fail-fast → the failing step's
// code, continue-on-error → exitError if any step failed.
func runRecipeSteps(steps []string, continueOnError bool, runStep func(name string, args []string) stepOutcome, log func(level, msg string, fields ...logField)) int {
	total := len(steps)

	type result struct {
		step   string
		out    stepOutcome
		status string
	}
	var results []result
	failed, skipped := 0, 0
	lastCode := exitOK
	stopped := -1

	for i, step := range steps {
		log("INFO", fmt.Sprintf("step %d/%d → %s", i+1, total, step))
		fields := strings.Fields(step)
		out := runStep(fields[0], fields[1:])
		results = append(results, result{step: step, out: out, status: stepStatus(out.code)})
		if out.code != exitOK {
			lastCode = out.code
			failed++
			if !continueOnError {
				stopped = i
				break
			}
		}
	}
	// Under fail-fast the steps after the failure never ran — record them
	// as skipped so the recap accounts for all N steps.
	if stopped >= 0 {
		for j := stopped + 1; j < total; j++ {
			results = append(results, result{step: steps[j], status: "skipped"})
			skipped++
		}
	}

	// Per-step recap + running totals.
	var warnTot int
	var durTot time.Duration
	for i, r := range results {
		warnTot += r.out.warnings
		durTot += r.out.duration
		log(recapLevel(r.status),
			fmt.Sprintf("step %d/%d %s", i+1, total, r.status),
			stepFields(r.step, r.out, r.status)...)
	}

	okN := total - failed - skipped
	totFields := []logField{{"steps", strconv.Itoa(total)}, {"ok", strconv.Itoa(okN)}}
	if failed > 0 {
		totFields = append(totFields, logField{"failed", strconv.Itoa(failed)})
	}
	if skipped > 0 {
		totFields = append(totFields, logField{"skipped", strconv.Itoa(skipped)})
	}
	if warnTot > 0 {
		totFields = append(totFields, logField{"warnings", strconv.Itoa(warnTot)})
	}
	totFields = append(totFields, logField{"took", fmtDur(durTot)})
	totLevel := "INFO"
	if failed > 0 {
		totLevel = "ERROR"
	}
	log(totLevel, "run summary", totFields...)

	switch {
	case stopped >= 0:
		return lastCode
	case failed > 0:
		return exitError
	default:
		return exitOK
	}
}

// stepStatus maps a step's exit code to its recap status word.
func stepStatus(code int) string {
	switch code {
	case exitOK:
		return "ok"
	case exitCancelled:
		return "cancelled"
	default:
		return "failed"
	}
}

// recapLevel maps a recap status to the log level its line is emitted at.
func recapLevel(status string) string {
	switch status {
	case "failed":
		return "ERROR"
	case "cancelled", "skipped":
		return "WARNING"
	default:
		return "INFO"
	}
}

// stepFields builds the recap fields for one step: always the cmd; the
// warning count when non-zero; on failure the error count + exit code; and
// the duration for any step that actually ran (skipped steps have none).
func stepFields(step string, out stepOutcome, status string) []logField {
	fields := []logField{{"cmd", step}}
	if out.warnings > 0 {
		fields = append(fields, logField{"warnings", strconv.Itoa(out.warnings)})
	}
	if status == "failed" {
		if out.errors > 0 {
			fields = append(fields, logField{"errors", strconv.Itoa(out.errors)})
		}
		fields = append(fields, logField{"exit", strconv.Itoa(out.code)})
	}
	if status != "skipped" {
		fields = append(fields, logField{"took", fmtDur(out.duration)})
	}
	return fields
}

// fmtDur renders a step/total duration compactly (e.g. "1.23s", "180ms").
func fmtDur(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// parseRecipeArgs extracts the recipe path (first positional; empty or `-`
// means stdin), the --continue-on-error flag, and the --log destination.
// `--log` (bare) enables logging to the default location (logDest == "");
// `--log=<path>` enables it to an explicit path. The space form
// `--log <path>` is intentionally NOT supported so it can't be confused
// with the recipe positional. Unknown flags error.
func parseRecipeArgs(args []string) (path string, continueOnError bool, logDest string, logEnabled bool, err error) {
	for _, a := range args {
		switch {
		case a == "--continue-on-error":
			continueOnError = true
		case a == "--log":
			logEnabled = true
		case strings.HasPrefix(a, "--log="):
			logEnabled = true
			logDest = strings.TrimPrefix(a, "--log=")
		case a == "-":
			path = a
		case strings.HasPrefix(a, "-"):
			return "", false, "", false, fmt.Errorf("unknown flag: %s", a)
		default:
			if path != "" && path != "-" {
				return "", false, "", false, fmt.Errorf("multiple recipe files given")
			}
			path = a
		}
	}
	return path, continueOnError, logDest, logEnabled, nil
}

// readRecipe opens the recipe (stdin when path is empty or `-`) and parses
// its lines.
func readRecipe(path string) ([]string, error) {
	if path == "" || path == "-" {
		return parseRecipeLines(os.Stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseRecipeLines(f)
}

// parseRecipeLines returns the executable steps: each line with its
// comment stripped and trimmed, dropping the now-empty ones. Both
// full-line comments (`# …`) and trailing comments (`update x  # …`) are
// supported. Pure over an io.Reader for testability.
func parseRecipeLines(r io.Reader) ([]string, error) {
	var steps []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(stripComment(sc.Text()))
		if line == "" {
			continue
		}
		steps = append(steps, line)
	}
	return steps, sc.Err()
}

// stripComment removes a `#` comment from a recipe line. A `#` starts a
// comment only at the start of the line or when preceded by whitespace,
// so a `#` embedded in a token (none occur in Echo args today, but the
// rule stays safe) is left intact. Returns the line up to the comment.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}
