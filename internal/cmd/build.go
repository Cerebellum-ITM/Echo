package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// BuildAction is the user's final decision on a composed command line.
type BuildAction int

const (
	BuildRun    BuildAction = iota // dispatch the composed command now
	BuildCopy                      // copy the recipe line to the clipboard
	BuildCancel                    // discard it
)

// BuildOpts configures a build-mode run. Flags is the command's
// user-facing flag list, already alias-filtered by the repl.
type BuildOpts struct {
	Cfg     *config.Config
	Root    string
	Command string
	Flags   []string
	Palette theme.Palette
	// Warnf, when non-nil, receives a one-line warning during the build
	// (e.g. a flag dropped because its value prompt was cancelled). The
	// repl renders it as a WARNING echo.build line.
	Warnf func(msg string)
	// Infof, when non-nil, receives an informational progress line (e.g.
	// the remote round-trips i18n-pull makes while listing modules). The
	// repl renders it as an INFO echo.build line.
	Infof func(msg string)
	// SkipDecide skips the final Run/Copy/Cancel select and returns the
	// composed argv directly (Action = BuildRun). Used by `sequence`, which
	// collects each step's flags through the builder but decides what to do
	// with the whole assembled sequence afterwards, not per step.
	SkipDecide bool
}

// BuildResult is the outcome of RunBuild: the composed argv (positionals
// first, then flags in picker order) and the action to take with it.
type BuildResult struct {
	Args   []string
	Action BuildAction
}

// ErrNothingToBuild is returned when a command has neither a positional
// picker nor any known flags — there is simply nothing to compose. The
// repl maps it to a WARNING line + exit 2.
var ErrNothingToBuild = errors.New("nothing to build")

// chosenFlag is one flag the user selected, with its value (empty for a
// boolean flag) and the separator the command's parser expects.
type chosenFlag struct {
	name  string
	value string // empty → boolean flag, appended as-is
	sep   string // "=" or " "; only meaningful when value != ""
}

// positionalSpec describes the picker(s) a command runs before its flags.
type positionalSpec struct {
	title string
	multi bool
	list  func(ctx context.Context, o BuildOpts) ([]string, error)
	// extra, when set, appends a second positional gathered after the
	// picked one (the i18n lang input). Only i18n-export/i18n-update set it.
	extra func(o BuildOpts) (string, error)
}

// flagValueSpec describes how to gather the value for a flag that takes
// one. kind "pick" shows a single-select over options; "input" shows a
// text field. sep is how composeArgs joins the value ("=" or " ").
type flagValueSpec struct {
	kind    string // "pick" | "input"
	options func(o BuildOpts) []string
	prompt  string
	def     string
	sep     string
}

// buildPositionals maps a command to its positional picker spec. Commands
// absent here skip the positional step (they only assemble flags).
var buildPositionals = map[string]positionalSpec{
	"install":       {title: "Modules", multi: true, list: listModules},
	"update":        {title: "Modules", multi: true, list: listModules},
	"uninstall":     {title: "Modules", multi: true, list: listModules},
	"test":          {title: "Modules", multi: true, list: listModules},
	"modinfo":       {title: "Module", multi: false, list: listModules},
	"view":          {title: "Module", multi: false, list: listModules},
	"i18n-export":   {title: "Module", multi: false, list: listModules, extra: i18nLangInput},
	"i18n-update":   {title: "Module", multi: false, list: listModules, extra: i18nLangInput},
	"db-backup":     {title: "Database", multi: false, list: listDatabases},
	"db-drop":       {title: "Database", multi: false, list: listDatabases},
	"db-neutralize": {title: "Database", multi: false, list: listDatabases},
	"db-restore":    {title: "Backup file", multi: false, list: listBackups},
	"logs":          {title: "Service", multi: false, list: listServices},
	"restart":       {title: "Service", multi: false, list: listServices},
}

// buildFlagValues maps cmd → flag → how to gather its value. Every flag
// not present here is boolean (selected = appended as-is). The sep column
// matches each command's real parser (verified against the parser source).
var buildFlagValues = map[string]map[string]flagValueSpec{
	"install":   {"--level": {kind: "pick", options: logLevels, sep: "="}},
	"update":    {"--level": {kind: "pick", options: logLevels, sep: "="}},
	"uninstall": {"--level": {kind: "pick", options: logLevels, sep: "="}},
	// `tests` parser accepts both `--tags x` and `--tags=x`; use `=` so the
	// comma/colon spec stays a single token.
	"test": {"--tags": {kind: "input", prompt: ":TestX.test_y,-external", sep: "="}},
	// RunLogs parses `-t <n>` (space form only); emit two tokens.
	"logs":        {"-t": {kind: "input", prompt: "tail lines", def: "100", sep: " "}},
	"i18n-export": {"--out": {kind: "input", prompt: "path/to/file.po", sep: "="}},
	// i18n-pull is NOT here: its candidates (modules) live on a remote that
	// must be resolved first, so it has a dedicated builder (runI18nPullBuild)
	// that also bakes --from=<target>.
	"report": {
		"--step":      {kind: "input", prompt: "step number", sep: "="},
		"--level":     {kind: "pick", options: reportLevels, sep: "="},
		"--min-level": {kind: "pick", options: reportLevels, sep: "="},
	},
	// parseDBArgs accepts both `--as name` and `--as=name`; use `=`.
	"db-restore": {"--as": {kind: "input", prompt: "destination DB name", sep: "="}},
}

// --- data providers (thin wrappers over existing helpers) ---

func listModules(ctx context.Context, o BuildOpts) ([]string, error) {
	return resolveModules(ctx, ModulesOpts{Cfg: o.Cfg, Root: o.Root, Palette: o.Palette})
}

func listDatabases(ctx context.Context, o BuildOpts) ([]string, error) {
	return docker.ListDatabases(ctx, o.Cfg.ComposeCmd, o.Root, o.Cfg.DBContainer,
		env.Load(o.Root)["POSTGRES_USER"])
}

func listBackups(_ context.Context, o BuildOpts) ([]string, error) {
	return listBackupFiles(o.Root)
}

func listServices(ctx context.Context, o BuildOpts) ([]string, error) {
	containers, err := docker.ListContainers(ctx, o.Cfg.ComposeCmd, o.Root)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(containers))
	for i, c := range containers {
		out[i] = c.Service
	}
	return out, nil
}

func logLevels(BuildOpts) []string { return odoo.LogLevels }
func reportLevels(BuildOpts) []string {
	return []string{"debug", "info", "warn", "error", "critical"}
}

// i18nLangInput prompts for the target language, prefilled with es_MX.
func i18nLangInput(o BuildOpts) (string, error) {
	lang := defaultI18nLang
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Language").
			Description("PO language code to export (e.g. es_MX, fr_FR)").
			Value(&lang),
	)).WithTheme(BuildHuhTheme(o.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(lang), nil
}

// RunBuild walks the user through the command's pickers and flags, shows
// the composed line, and returns the chosen action + argv. Interactive by
// definition: a non-TTY caller fails closed with ErrNonInteractive.
func RunBuild(ctx context.Context, opts BuildOpts) (BuildResult, error) {
	if err := requireTTY("build mode is interactive; run it from a terminal"); err != nil {
		return BuildResult{}, err
	}

	// Remote-aware builders: commands whose candidates live on a remote
	// that must be resolved before the picker can be populated.
	if opts.Command == "i18n-pull" {
		return runI18nPullBuild(ctx, opts)
	}
	// deploy has a bespoke builder: its commit / dirty-module picker IS the
	// selection, captured up front into --commits/--modules.
	if opts.Command == "deploy" {
		return runDeployBuild(ctx, opts)
	}

	spec, hasPos := buildPositionals[opts.Command]
	if !hasPos && len(opts.Flags) == 0 {
		return BuildResult{}, fmt.Errorf("%w for %q — it takes no picker or flags",
			ErrNothingToBuild, opts.Command)
	}

	// Step 1 — positionals.
	var positionals []string
	if hasPos {
		picked, err := pickPositionals(ctx, spec, opts)
		if err != nil {
			return BuildResult{}, err
		}
		positionals = append(positionals, picked...)
		if spec.extra != nil {
			v, err := spec.extra(opts)
			if err != nil {
				return BuildResult{}, err
			}
			if v != "" {
				positionals = append(positionals, v)
			}
		}
	}

	// Step 2 — flag multi-select, then Step 3 — value per selected flag.
	var flags []chosenFlag
	if len(opts.Flags) > 0 {
		picked, canceled, err := runFuzzyPickerCore(
			"Flags for "+opts.Command+" (Tab to toggle, Enter to confirm)",
			opts.Flags, nil, nil, opts.Palette, opts.Cfg.Stage)
		if err != nil {
			return BuildResult{}, err
		}
		if canceled {
			return BuildResult{}, ErrCancelled
		}
		chosen := make(map[string]bool, len(picked))
		for _, f := range picked {
			chosen[f] = true
		}
		// Iterate opts.Flags to preserve help order regardless of pick order.
		for _, f := range opts.Flags {
			if !chosen[f] {
				continue
			}
			vspec, takesValue := buildFlagValues[opts.Command][f]
			if !takesValue {
				flags = append(flags, chosenFlag{name: f})
				continue
			}
			val, ok, err := promptFlagValue(f, vspec, opts)
			if err != nil {
				return BuildResult{}, err
			}
			if !ok {
				warn(opts, "flag "+f+" skipped (no value)")
				continue
			}
			flags = append(flags, chosenFlag{name: f, value: val, sep: vspec.sep})
		}
	}

	// Step 4 — compose, show, decide.
	args := composeArgs(positionals, flags)
	if opts.SkipDecide {
		return BuildResult{Args: args, Action: BuildRun}, nil
	}
	action, err := decideAction(opts, args)
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Args: args, Action: action}, nil
}

// pickPositionals runs the multi- or single-select picker for the spec.
func pickPositionals(ctx context.Context, spec positionalSpec, opts BuildOpts) ([]string, error) {
	list, err := spec.list(ctx, opts)
	if err != nil {
		return nil, err
	}
	if spec.multi {
		return runFuzzyPicker(spec.title, list, opts.Palette)
	}
	one, err := runSingleFuzzyPicker(spec.title, list, opts.Palette)
	if err != nil {
		return nil, err
	}
	return []string{one}, nil
}

// promptFlagValue gathers a flag's value. It returns ok=false (not an
// error) when the user cancels the value prompt, signalling "drop this
// flag" rather than aborting the whole build.
func promptFlagValue(flag string, spec flagValueSpec, opts BuildOpts) (value string, ok bool, err error) {
	switch spec.kind {
	case "pick":
		v, err := runSingleFuzzyPicker("Value for "+flag, spec.options(opts), opts.Palette)
		if errors.Is(err, ErrCancelled) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return v, true, nil
	default: // "input"
		v := spec.def
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("Value for " + flag).
				Placeholder(spec.prompt).
				Value(&v),
		)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
		if err := form.Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return "", false, nil
			}
			return "", false, err
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return "", false, nil
		}
		return v, true, nil
	}
}

// decideAction shows the composed line and the Run/Copy/Cancel select.
func decideAction(opts BuildOpts, args []string) (BuildAction, error) {
	var choice string
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(BuildLine(opts.Command, args)).
			Options(
				huh.NewOption("Run it now", "run"),
				huh.NewOption("Copy to clipboard", "copy"),
				huh.NewOption("Cancel", "cancel"),
			).
			Value(&choice),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return BuildCancel, err
	}
	switch choice {
	case "run":
		return BuildRun, nil
	case "copy":
		return BuildCopy, nil
	default:
		return BuildCancel, nil
	}
}

// composeArgs assembles the argv: positionals first, then flags in the
// order given. A boolean flag is one token; a value flag is `name=value`
// or two tokens (`name`, `value`) depending on sep.
func composeArgs(positionals []string, flags []chosenFlag) []string {
	out := make([]string, 0, len(positionals)+len(flags))
	out = append(out, positionals...)
	for _, f := range flags {
		switch {
		case f.value == "":
			out = append(out, f.name)
		case f.sep == " ":
			out = append(out, f.name, f.value)
		default:
			out = append(out, f.name+"="+f.value)
		}
	}
	return out
}

// BuildLine renders the recipe-style command line (no `echo ` prefix).
func BuildLine(command string, args []string) string {
	if len(args) == 0 {
		return command
	}
	return command + " " + strings.Join(args, " ")
}

func warn(opts BuildOpts, msg string) {
	if opts.Warnf != nil {
		opts.Warnf(msg)
	}
}

func info(opts BuildOpts, msg string) {
	if opts.Infof != nil {
		opts.Infof(msg)
	}
}

// BuildCommands returns, for tests, every command that has a build-mode
// positional or flag-value spec — so the repl/cmd guards can assert each
// key is a real command.
func BuildCommands() []string {
	set := map[string]bool{}
	for c := range buildPositionals {
		set[c] = true
	}
	for c := range buildFlagValues {
		set[c] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// BuildValueFlags returns, for tests, the flags of command that have a
// build-mode value spec — the repl guards them against commandFlags.
func BuildValueFlags(command string) []string {
	var out []string
	for f := range buildFlagValues[command] {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
