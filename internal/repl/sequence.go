package repl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
)

// sequenceCommands lists the commands offerable in a local sequence, in
// help order. Excludes interactive/PTY and meta commands that make no sense
// batched: shell, bash, psql, shell-run, connect, init, reset, clear, help,
// exit, quit, and sequence itself (no recursion in v1).
var sequenceCommands = []string{
	"up", "down", "stop", "restart", "ps", "logs",
	"install", "update", "uninstall", "test",
	"modules", "modinfo", "modstate", "view",
	"i18n-export", "i18n-update", "i18n-pull",
	"db-backup", "db-restore", "db-drop", "db-neutralize", "db-list", "db-use", "db-admin",
	"deploy", "report", "copy-last", "alias", "link",
}

// remoteSequenceCommands is the subset valid when the sequence targets a
// remote — only commands that accept --from/--remote, minus the interactive
// shells. Cross-checked against commandFlags in the test.
var remoteSequenceCommands = []string{"up", "stop", "restart", "logs", "i18n-pull", "deploy"}

// runSequence builds a sequence of commands interactively (tri-state picker
// → per-command builder → review) and runs them through the recipe step
// runner, or — with --last — repeats the project's last executed sequence.
// Local by default; --remote / --from <target> run the whole sequence
// against one remote target (the flag is baked into every step).
func (sess *session) runSequence(ctx context.Context, args []string) {
	from, remote := remoteRunFlags(args)
	last := seqHasFlag(args, "--last")
	continueOnError := seqHasFlag(args, "--continue-on-error")

	if last {
		prev, ok := config.LoadLastSequence(sess.cfg.ProjectKey)
		if !ok {
			sess.exitCode = exitUsage
			sess.seqLog("WARNING", "no previous sequence to repeat for this project")
			return
		}
		sess.seqLog("INFO", "repeating last sequence", logField{"steps", strconv.Itoa(len(prev.Steps))})
		sess.executeSequence(ctx, prev.Steps, continueOnError, prev.From, prev.Remote)
		return
	}

	remoteMode := remote || from != ""
	names := sequenceCommands
	title := "sequence — pick commands · order = pick order"
	if remoteMode {
		names = remoteSequenceCommands
		title = "sequence (remote) — pick commands · order = pick order"
	}
	desc := helpDescByName()
	items := make([]cmd.SeqItem, 0, len(names))
	for _, n := range names {
		items = append(items, cmd.SeqItem{Name: n, Desc: desc[n]})
	}

	picks, err := cmd.RunSequencePicker(title, items, sess.palette, sess.cfg.Stage)
	if err != nil {
		sess.finalize("sequence", 0, 0, err)
		return
	}

	steps, err := sess.buildSequenceSteps(ctx, picks, from, remote, remoteMode)
	if err != nil {
		sess.finalize("sequence", 0, 0, err)
		return
	}

	action, err := sess.sequenceReview(steps)
	if err != nil {
		sess.finalize("sequence", 0, 0, err)
		return
	}
	switch action {
	case seqActionRun:
		_ = config.SaveLastSequence(sess.cfg.ProjectKey, config.LastSequence{
			Steps: steps, Remote: remote, From: from, SavedAt: time.Now(),
		})
		sess.executeSequence(ctx, steps, continueOnError, from, remote)
	case seqActionSave:
		sess.saveSequenceRecipe(steps)
	case seqActionCopy:
		if err := clipboard.WriteAll(strings.Join(steps, "\n")); err != nil {
			sess.exitCode = exitError
			sess.seqLog("ERROR", "could not copy to clipboard", logField{"err", err.Error()})
			return
		}
		sess.exitCode = exitOK
		sess.seqLog("INFO", "sequence copied", logField{"steps", strconv.Itoa(len(steps))})
	default:
		sess.finalize("sequence", 0, 0, cmd.ErrCancelled)
	}
}

// mustBuildInSequence reports commands that are interactive at execution
// time and therefore MUST have their selection captured during the build
// phase, even if the user didn't mark them for the builder. `deploy` opens
// its own commit/dirty-module picker when run, which would block mid-
// sequence; its builder turns that into baked `--commits`/`--modules` flags
// so the choice is shown in the review and replayable by `--last`.
func mustBuildInSequence(command string) bool {
	switch command {
	case "deploy", "i18n-pull":
		// deploy: commit/dirty picker. i18n-pull: remote target + module
		// picker. Both must be resolved up front so nothing blocks mid-run.
		return true
	}
	return false
}

// buildSequenceSteps turns the picker's selections into composed recipe
// lines. A pick marked Build — or a command that is interactive at run time
// (mustBuildInSequence) — runs through the builder (return-only); the rest
// run as-is. When remoteMode is set, the remote flag is baked into each step
// so the command uses its own remote code path.
func (sess *session) buildSequenceSteps(ctx context.Context, picks []cmd.SeqPick, from string, remote, remoteMode bool) ([]string, error) {
	buildTotal := 0
	for _, pk := range picks {
		if pk.Build || mustBuildInSequence(pk.Command) {
			buildTotal++
		}
	}
	buildIdx := 0
	steps := make([]string, 0, len(picks))
	for _, pk := range picks {
		line := pk.Command
		forced := mustBuildInSequence(pk.Command)
		if pk.Build || forced {
			buildIdx++
			msg := "building step"
			if forced && !pk.Build {
				msg = "building step (interactive command, resolved up front)"
			}
			sess.seqLog("INFO", msg,
				logField{"step", fmt.Sprintf("%d/%d", buildIdx, buildTotal)},
				logField{"command", pk.Command})
			res, berr := cmd.RunBuild(ctx, cmd.BuildOpts{
				Cfg:        sess.cfg,
				Root:       sess.projectDir,
				Command:    pk.Command,
				Flags:      buildFlags(pk.Command),
				Palette:    sess.palette,
				SkipDecide: true,
				From:       from,
				Warnf: func(msg string) {
					emitOdooLog("WARNING", "echo.sequence.build", msg, nil, sess.styles, sess.palette, sess.cfg.DBName)
				},
				Infof: func(msg string) {
					emitOdooLog("INFO", "echo.sequence.build", msg, nil, sess.styles, sess.palette, sess.cfg.DBName)
				},
			})
			switch {
			case berr == nil:
				line = cmd.BuildLine(pk.Command, res.Args)
			case errors.Is(berr, cmd.ErrNothingToBuild):
				// Marked build but nothing to compose — run it as-is.
				emitOdooLog("DEBUG", "echo.sequence.build",
					fmt.Sprintf("nothing to build for %q, running as-is", pk.Command),
					nil, sess.styles, sess.palette, sess.cfg.DBName)
			case errors.Is(berr, cmd.ErrCancelled), errors.Is(berr, cmd.ErrQuit),
				errors.Is(berr, cmd.ErrNonInteractive), errors.Is(berr, huh.ErrUserAborted):
				// User intent / environment — abort the whole (not-yet-run)
				// sequence.
				return nil, berr
			case forced:
				// A forced (interactive) command can't safely run as-is — its
				// picker would block mid-sequence. Abort instead of degrading.
				return nil, fmt.Errorf("could not resolve %q for the sequence: %w", pk.Command, berr)
			default:
				// Operational failure building this step — e.g. its candidate
				// list needs a local docker stack but this is a projectless /
				// remote run. Warn and run it as-is rather than abort.
				emitOdooLog("WARNING", "echo.sequence.build",
					fmt.Sprintf("could not build %q (%v), running as-is", pk.Command, berr),
					nil, sess.styles, sess.palette, sess.cfg.DBName)
			}
		}
		if remoteMode {
			line = bakeRemote(pk.Command, line, from, remote)
		}
		steps = append(steps, line)
	}
	return steps, nil
}

// seqStepResult is one executed (or skipped) sequence step.
type seqStepResult struct {
	step   string
	out    stepOutcome
	status string
	silent string
}

// executeSequence runs the assembled steps with the same fail-fast (or
// continue-on-error) semantics as the recipe runner, emitting echo.sequence
// lines. A trailing follow-`logs` step is treated as terminal: the summary
// is emitted BEFORE entering the follow, so the Ctrl+C that ends it never
// looks like a sequence failure.
func (sess *session) executeSequence(ctx context.Context, steps []string, continueOnError bool, from string, remote bool) {
	nFollow := 0
	for _, s := range steps {
		if isFollowLogs(s) {
			nFollow++
		}
	}
	steps, follow := reorderLogsLast(steps)
	total := len(steps)
	if total == 0 {
		sess.exitCode = exitUsage
		sess.seqLog("WARNING", "no steps to run")
		return
	}
	if nFollow > 1 {
		sess.seqLog("WARNING", "multiple follow logs steps; only the last one follows")
	}

	modeFields := []logField{{"steps", strconv.Itoa(total)}}
	if remote || from != "" {
		modeFields = append(modeFields, logField{"mode", "remote"})
		if from != "" {
			modeFields = append(modeFields, logField{"target", from})
		}
	} else {
		modeFields = append(modeFields, logField{"mode", "local"})
	}
	sess.seqLog("INFO", "running", modeFields...)

	head := steps
	followLine := ""
	if follow {
		head = steps[:total-1]
		followLine = steps[total-1]
	}

	report := config.RunReport{Recipe: "sequence"}
	stepNum := 0
	runStep := func(name string, sargs []string, suppress int) stepOutcome {
		return sess.runStepCaptured(ctx, name, sargs, suppress, &report, &stepNum)
	}

	var results []seqStepResult
	failed, skipped := 0, 0
	lastCode := exitOK
	stopped := -1
	for i, step := range head {
		sess.seqStepLog("INFO", fmt.Sprintf("step %d/%d → %s", i+1, total, step))
		fields := strings.Fields(step)
		clean, suppress, label, bad := stripSilent(fields[1:])
		if bad != "" {
			sess.seqStepLog("WARNING", "ignoring invalid --silent="+bad+" — running without suppression")
		}
		out := runStep(fields[0], clean, suppress)
		results = append(results, seqStepResult{step: step, out: out, status: stepStatus(out.code), silent: label})
		if out.code != exitOK {
			lastCode = out.code
			failed++
			if !continueOnError {
				stopped = i
				break
			}
		}
	}
	// Steps that never ran under fail-fast — record as skipped, including the
	// terminal follow step (it won't be entered below).
	if stopped >= 0 {
		for j := stopped + 1; j < len(head); j++ {
			results = append(results, seqStepResult{step: head[j], status: "skipped"})
			skipped++
		}
		if follow {
			results = append(results, seqStepResult{step: followLine, status: "skipped"})
			skipped++
		}
	}

	// Per-step recap + running totals.
	var errTot, warnTot int
	var durTot time.Duration
	okN := 0
	for i, r := range results {
		errTot += r.out.errors
		warnTot += r.out.warnings
		durTot += r.out.duration
		if r.status == "ok" {
			okN++
		}
		fields := append([]logField{
			{"step", fmt.Sprintf("%d/%d", i+1, total)},
			{"status", r.status},
		}, stepFields(r.step, r.out, r.status, r.silent)...)
		sess.seqStepLog(recapLevel(r.status), "", fields...)
	}

	totFields := []logField{{"steps", strconv.Itoa(total)}, {"ok", strconv.Itoa(okN)}}
	if failed > 0 {
		totFields = append(totFields, logField{"failed", strconv.Itoa(failed)})
	}
	if skipped > 0 {
		totFields = append(totFields, logField{"skipped", strconv.Itoa(skipped)})
	}
	totFields = append(totFields,
		logField{"errors", strconv.Itoa(errTot)},
		logField{"warnings", strconv.Itoa(warnTot)},
		logField{"took", fmtDur(durTot)})
	totLevel := "INFO"
	if failed > 0 {
		totLevel = "ERROR"
	}
	// Emitted BEFORE entering a terminal follow step (its Ctrl+C must not
	// read as a failed sequence).
	sess.seqLog(totLevel, "sequence complete", totFields...)

	_ = config.SaveRunReport(report) // best-effort; powers `report`

	switch {
	case stopped >= 0:
		sess.exitCode = lastCode
	case failed > 0:
		sess.exitCode = exitError
	default:
		sess.exitCode = exitOK
	}

	if follow && stopped < 0 {
		fields := strings.Fields(followLine)
		sess.seqStepLog("WARNING", fmt.Sprintf("step %d/%d → %s (follow, ^c to stop)", total, total, followLine))
		sess.dispatchParsed(ctx, fields[0], fields[1:])
	}
}

// seqAction is the user's decision on a reviewed sequence.
type seqAction int

const (
	seqActionRun seqAction = iota
	seqActionSave
	seqActionCopy
	seqActionCancel
)

// sequenceReview shows the assembled steps and asks what to do with them.
func (sess *session) sequenceReview(steps []string) (seqAction, error) {
	var b strings.Builder
	b.WriteString("Run this sequence?\n")
	for i, s := range steps {
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, s))
	}
	var choice string
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(b.String()).
			Options(
				huh.NewOption("Run it now", "run"),
				huh.NewOption("Save as recipe (.echo)", "save"),
				huh.NewOption("Copy to clipboard", "copy"),
				huh.NewOption("Cancel", "cancel"),
			).
			Value(&choice),
	)).WithTheme(cmd.BuildHuhTheme(sess.palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return seqActionCancel, err
	}
	switch choice {
	case "run":
		return seqActionRun, nil
	case "save":
		return seqActionSave, nil
	case "copy":
		return seqActionCopy, nil
	default:
		return seqActionCancel, nil
	}
}

// saveSequenceRecipe writes the steps to a <name>.echo file in the project
// directory, so `echo run <name>.echo` can replay it.
func (sess *session) saveSequenceRecipe(steps []string) {
	name := "sequence"
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Recipe file name").Placeholder("my-flow").Value(&name),
	)).WithTheme(cmd.BuildHuhTheme(sess.palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		sess.finalize("sequence", 0, 0, err)
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		sess.exitCode = exitUsage
		sess.seqLog("WARNING", "no file name given; recipe not saved")
		return
	}
	if !strings.HasSuffix(name, ".echo") {
		name += ".echo"
	}
	path := filepath.Join(sess.projectDir, name)
	content := strings.Join(steps, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		sess.exitCode = exitError
		sess.seqLog("ERROR", "could not write recipe", logField{"path", path}, logField{"err", err.Error()})
		return
	}
	sess.exitCode = exitOK
	sess.seqLog("INFO", "recipe saved", logField{"file", name})
}

// seqLog / seqStepLog emit the sequence orchestration lines in Echo's Odoo
// log style (echo.sequence / echo.sequence.step).
func (sess *session) seqLog(level, msg string, fields ...logField) {
	emitOdooLog(level, "echo.sequence", msg, fields, sess.styles, sess.palette, sess.cfg.DBName)
}

func (sess *session) seqStepLog(level, msg string, fields ...logField) {
	emitOdooLog(level, "echo.sequence.step", msg, fields, sess.styles, sess.palette, sess.cfg.DBName)
}

// isFollowLogs reports whether a step is a `logs` command in follow mode
// (the default): it blocks until Ctrl+C, so it can only be the terminal
// step. `--no-follow` / `--copy` / `-c` make logs bounded, not following.
func isFollowLogs(step string) bool {
	fields := strings.Fields(step)
	if len(fields) == 0 || fields[0] != "logs" {
		return false
	}
	for _, a := range fields[1:] {
		if a == "--no-follow" || a == "--copy" || a == "-c" {
			return false
		}
	}
	return true
}

// reorderLogsLast moves any follow-`logs` step(s) to the end, preserving the
// relative order of the rest, and reports whether at least one was found.
func reorderLogsLast(steps []string) (out []string, followLogs bool) {
	var follow []string
	for _, s := range steps {
		if isFollowLogs(s) {
			follow = append(follow, s)
			continue
		}
		out = append(out, s)
	}
	out = append(out, follow...)
	return out, len(follow) > 0
}

// bakeRemote appends the remote selector to a step line for command, unless
// it already carries one. A named target (`--from=<name>`) is accepted by
// every remote-capable command. The bare `--remote` (this dir's link) only
// goes on commands that declare it; `deploy`/`i18n-pull` don't take
// `--remote` — with no `--from` they default to the project's [connect]
// binding, so they get no flag.
func bakeRemote(command, line, from string, remote bool) string {
	if strings.Contains(line, "--from") || strings.Contains(line, "--remote") {
		return line
	}
	switch {
	case from != "":
		return line + " --from=" + from
	case remote && commandAcceptsFlag(command, "--remote"):
		return line + " --remote"
	}
	return line
}

// commandAcceptsFlag reports whether command's flag set includes flag.
func commandAcceptsFlag(command, flag string) bool {
	for _, f := range commandFlags[command] {
		if f == flag {
			return true
		}
	}
	return false
}

// seqHasFlag reports whether flag appears verbatim in args.
func seqHasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// helpDescByName maps each top-level command to its one-line help
// description, derived from helpSections so the sequence picker reuses the
// existing copy. Flag rows and the first token's arg placeholders are
// stripped; the first description wins on duplicates.
func helpDescByName() map[string]string {
	out := map[string]string{}
	for _, sec := range helpSections() {
		for _, it := range sec.items {
			if strings.HasPrefix(it.cmd, " ") || strings.HasPrefix(it.cmd, "-") {
				continue
			}
			fields := strings.Fields(it.cmd)
			if len(fields) == 0 {
				continue
			}
			name := strings.TrimRight(fields[0], ",")
			if _, ok := out[name]; !ok {
				out[name] = it.desc
			}
		}
	}
	return out
}
