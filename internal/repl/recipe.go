package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/cmd"
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
	path, continueOnError, logDest, logEnabled, pick, last, err := parseRecipeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "echo run: "+err.Error())
		return exitUsage
	}

	if pick {
		selected, perr := pickRecipeFile(cwd, p)
		if perr != nil {
			if errors.Is(perr, cmd.ErrQuit) {
				return exitOK // Ctrl+X: clean quit, no error noise.
			}
			fmt.Fprintln(os.Stderr, "echo run: "+perr.Error())
			if errors.Is(perr, cmd.ErrCancelled) {
				return exitCancelled
			}
			return exitUsage
		}
		path = selected
	}

	if last {
		names, lerr := echoRecipesIn(cwd)
		if lerr == nil && len(names) == 0 {
			lerr = fmt.Errorf("no .echo recipes found in %s", cwd)
		}
		if lerr != nil {
			fmt.Fprintln(os.Stderr, "echo run: "+lerr.Error())
			return exitUsage
		}
		path = filepath.Join(cwd, names[0])
	}

	steps, err := readRecipe(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "echo run: "+err.Error())
		return exitUsage
	}

	sess, _ := newSession(s, p, project, id, stage, version, themeName, username, cwd, cfg)
	sess.recipe = true // a `help` step prints flat; never opens the pager.
	sess.pruneCmdLogs()
	ctx := context.Background()

	// System-status line: emitted once at the start of the run (not per
	// step), so a headless run shows which Echo + Odoo environment it ran
	// against, mirroring connect / i18n-pull.
	sess.runStatusLog(cfg)

	// Make the transcript show which file --last resolved to.
	if last {
		sess.runLog("INFO", "latest recipe → "+recipeLabel(path))
	}

	// Capture the transcript to a file when --log is given. A failure to
	// open the log must not abort the run — warn and carry on without it.
	if logEnabled {
		if dest, closeLog, ok := sess.openRunLog(logDest, path); ok {
			defer closeLog()
			defer func() { sess.runLog("INFO", "log written", logField{"path", dest}) }()
		}
	}

	// Capture a structured record of the run so a later `report` can query
	// it by step and level. Persisted (best-effort) after the run.
	report := config.RunReport{Recipe: recipeLabel(path)}
	stepNum := 0

	runStep := func(name string, sargs []string, suppress int) stepOutcome {
		return sess.runStepCaptured(ctx, name, sargs, suppress, &report, &stepNum)
	}
	code := runRecipeSteps(steps, continueOnError, runStep, sess.runLog)
	_ = config.SaveRunReport(report) // best-effort; never fails the run
	return code
}

// resolveLogDest turns a `--log=` value into a concrete file path. An
// empty value yields "" (the caller falls back to the default location). A
// value that is an existing directory — e.g. `--log=.` for the current
// directory — becomes `<dir>/<recipe>.log`, named after the recipe, so you
// can drop a result file next to the recipe without spelling out the name.
// Any other value is taken as an explicit file path, unchanged.
func resolveLogDest(dest, recipePath string) string {
	if dest == "" {
		return ""
	}
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		return filepath.Join(dest, recipeLabel(recipePath)+".log")
	}
	return dest
}

// openRunLog resolves the log destination, creates the file, sets the
// package-level run-log sink, and returns (dest, closer, ok). When ok is
// false the caller proceeds without logging. The closer flushes, closes,
// and clears the sink. `dest` empty means the default location under
// ~/.config/echo/run-logs/; a directory (e.g. `.`) means a `<recipe>.log`
// in that directory.
func (sess *session) openRunLog(dest, recipePath string) (string, func(), bool) {
	dest = resolveLogDest(dest, recipePath)
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

// runStatusLog emits the one-shot system-status line at the start of a run:
// the Echo CLI version (with build metadata / dirty marker) and the local
// Odoo environment (version, project, db). Empty values render loud
// ("unknown"/"-") so a mis-config is visible.
func (sess *session) runStatusLog(cfg *config.Config) {
	odoo := cfg.OdooVersion
	if odoo == "" {
		odoo = "unknown"
	}
	project := resolveComposeProject(cfg)
	if project == "" {
		project = "-"
	}
	db := cfg.DBName
	if db == "" {
		db = "-"
	}
	env := cfg.Stage
	if env == "" {
		env = "unknown"
	}
	cli := FullVersion()
	if cli == "" {
		cli = "unknown"
	}
	emitOdooLog("INFO", "echo.system.status", "system",
		[]logField{{"cli", cli}, {"odoo", odoo}, {"env", env}, {"project", project}, {"db", db}},
		sess.styles, sess.palette, sess.cfg.DBName)
}

// runStepCaptured dispatches one step under the given output suppression,
// captures its outcome (exit code, error/warning counts, duration) and its
// output lines into the run report, then isolates the buffer for the next
// step. Shared by the recipe runner (`echo run`) and the sequence runner so
// both record steps identically. stepNum is advanced in place.
func (sess *session) runStepCaptured(ctx context.Context, name string, sargs []string, suppress int, report *config.RunReport, stepNum *int) stepOutcome {
	*stepNum++
	start := time.Now()
	suppressLevel = suppress
	sess.dispatchParsed(ctx, name, sargs)
	suppressLevel = -1
	out := stepOutcome{
		code:     sess.exitCode,
		errors:   sess.lastErrors,
		warnings: sess.lastWarnings,
		duration: time.Since(start),
	}
	rls := captureReportLines(sess.lastOutput.Filtered(nil))
	// Isolate steps: meta commands (help/clear) don't reset the buffer,
	// so clear it here to keep each step's capture to its own lines.
	sess.lastOutput.Reset()
	report.Steps = append(report.Steps, config.StepReport{
		Index:  *stepNum,
		Cmd:    strings.TrimSpace(name + " " + strings.Join(sargs, " ")),
		Status: stepStatus(out.code),
		Lines:  rls,
	})
	return out
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
func runRecipeSteps(steps []string, continueOnError bool, runStep func(name string, args []string, suppress int) stepOutcome, log func(level, msg string, fields ...logField)) int {
	total := len(steps)

	type result struct {
		step   string
		out    stepOutcome
		status string
		silent string // suppression label ("all"/level) when --silent was used
	}
	var results []result
	failed, skipped := 0, 0
	lastCode := exitOK
	stopped := -1

	for i, step := range steps {
		log("INFO", fmt.Sprintf("step %d/%d → %s", i+1, total, step))
		fields := strings.Fields(step)
		clean, suppress, label, bad := stripSilent(fields[1:])
		if bad != "" {
			log("WARNING", "ignoring invalid --silent="+bad+" — running without suppression")
		}
		out := runStep(fields[0], clean, suppress)
		results = append(results, result{step: step, out: out, status: stepStatus(out.code), silent: label})
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
	var errTot, warnTot int
	var durTot time.Duration
	for i, r := range results {
		errTot += r.out.errors
		warnTot += r.out.warnings
		durTot += r.out.duration
		fields := append([]logField{
			{"step", fmt.Sprintf("%d/%d", i+1, total)},
			{"status", r.status},
		}, stepFields(r.step, r.out, r.status, r.silent)...)
		log(recapLevel(r.status), "", fields...)
	}

	okN := total - failed - skipped
	totFields := []logField{{"steps", strconv.Itoa(total)}, {"ok", strconv.Itoa(okN)}}
	if failed > 0 {
		totFields = append(totFields, logField{"failed", strconv.Itoa(failed)})
	}
	if skipped > 0 {
		totFields = append(totFields, logField{"skipped", strconv.Itoa(skipped)})
	}
	// Always report the error/warning totals so the summary states the
	// counts even when they're zero.
	totFields = append(totFields,
		logField{"errors", strconv.Itoa(errTot)},
		logField{"warnings", strconv.Itoa(warnTot)})
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
func stepFields(step string, out stepOutcome, status, silent string) []logField {
	fields := []logField{{"cmd", step}}
	if silent != "" {
		fields = append(fields, logField{"silent", silent})
	}
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
// means stdin), the --continue-on-error flag, the --log destination,
// --pick (open a picker of *.echo recipes instead of taking a path), and
// --last (run the most recently created *.echo recipe directly).
// `--log` (bare) enables logging to the default location (logDest == "");
// `--log=<path>` enables it to an explicit path, or — when the value is a
// directory like `.` — to a `<recipe>.log` file in it (resolved later in
// openRunLog). The space form `--log <path>` is intentionally NOT
// supported so it can't be confused with the recipe positional. Unknown
// flags error. `--pick` and `--last` are mutually exclusive with each
// other and with a positional path / stdin.
func parseRecipeArgs(args []string) (path string, continueOnError bool, logDest string, logEnabled bool, pick, last bool, err error) {
	for _, a := range args {
		switch {
		case a == "--continue-on-error":
			continueOnError = true
		case a == "--pick":
			pick = true
		case a == "--last":
			last = true
		case a == "--log":
			logEnabled = true
		case strings.HasPrefix(a, "--log="):
			logEnabled = true
			logDest = strings.TrimPrefix(a, "--log=")
		case a == "-":
			path = a
		case strings.HasPrefix(a, "-"):
			return "", false, "", false, false, false, fmt.Errorf("unknown flag: %s", a)
		default:
			if path != "" && path != "-" {
				return "", false, "", false, false, false, fmt.Errorf("multiple recipe files given")
			}
			path = a
		}
	}
	if pick && path != "" {
		return "", false, "", false, false, false, fmt.Errorf("--pick takes no recipe path")
	}
	if last && path != "" {
		return "", false, "", false, false, false, fmt.Errorf("--last takes no recipe path")
	}
	if last && pick {
		return "", false, "", false, false, false, fmt.Errorf("--last and --pick are mutually exclusive")
	}
	return path, continueOnError, logDest, logEnabled, pick, last, nil
}

// recipeEntry pairs a recipe filename with its creation time, for the
// newest-first ordering of the picker and --last.
type recipeEntry struct {
	name    string
	created time.Time
}

// sortRecipesByCreation orders entries newest-first, breaking ties
// alphabetically so the result is deterministic.
func sortRecipesByCreation(entries []recipeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].created.Equal(entries[j].created) {
			return entries[i].created.After(entries[j].created)
		}
		return entries[i].name < entries[j].name
	})
}

// echoRecipesIn returns the names of the *.echo recipe files directly in
// dir (top-level, no recursion), sorted by creation time, newest first
// (birth time on Darwin, ModTime elsewhere). Subdirectories and non-.echo
// files are skipped. Pure over the filesystem so it's unit-testable.
func echoRecipesIn(dir string) ([]string, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var entries []recipeEntry
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".echo") {
			continue
		}
		// An entry whose Info() fails keeps the zero time and sinks to
		// the end of the list rather than aborting the scan.
		var created time.Time
		if info, ierr := e.Info(); ierr == nil {
			created = fileCreated(info)
		}
		entries = append(entries, recipeEntry{name: e.Name(), created: created})
	}
	sortRecipesByCreation(entries)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
	}
	return names, nil
}

// pickRecipeFile lists the *.echo recipes in dir and opens a single-select
// picker, returning the absolute path of the chosen recipe. Returns a
// clear error when none are found, or ErrCancelled on Esc.
func pickRecipeFile(dir string, p theme.Palette) (string, error) {
	names, err := echoRecipesIn(dir)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no .echo recipes found in %s", dir)
	}
	name, err := cmd.PickOne("Recipe to run", names, p)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
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
