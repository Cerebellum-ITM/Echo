package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// ActionsOpts configures an `actions` run.
type ActionsOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	Log     func(level, sub, msg, db string, fields ...[2]string)
	// StreamOut renders the list table (raw lines the REPL recolors).
	StreamOut func(string)
}

func (o ActionsOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

func (o ActionsOpts) stream(line string) {
	if o.StreamOut != nil {
		o.StreamOut(line)
	}
}

// actionsArgs is the parsed shape of the actions input.
type actionsArgs struct {
	sub     string // list | add | edit | rm
	name    string // edit/rm target (optional)
	from    string
	remote  bool
	jsonOut bool
	force   bool
}

// ActionsResult is the machine-readable `actions list --json` payload.
type ActionsResult struct {
	Sub     string                `json:"-"`
	Source  string                `json:"source"` // server | local
	Actions []config.DeployAction `json:"actions"`
	JSON    bool                  `json:"-"`
}

// parseActionsArgs parses the subcommand (default list) plus the optional
// name positional and --from/--remote/--json/--force.
func parseActionsArgs(args []string) (actionsArgs, error) {
	out := actionsArgs{sub: "list"}
	out.from, out.remote = remoteFlagsIn(args)
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			i++ // value consumed by remoteFlagsIn
		case strings.HasPrefix(a, "--from="), a == "--remote":
			// consumed by remoteFlagsIn
		case a == "--json":
			out.jsonOut = true
		case a == "--force":
			out.force = true
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) > 0 {
		out.sub = positionals[0]
	}
	switch out.sub {
	case "list", "add", "edit", "rm":
	default:
		return out, fmt.Errorf("%w: unknown actions subcommand %q (use list, add, edit, or rm)", ErrUsage, out.sub)
	}
	if (out.sub == "edit" || out.sub == "rm") && len(positionals) > 1 {
		out.name = positionals[1]
	}
	return out, nil
}

// RunActions dispatches the actions subcommands. list/add/edit/rm operate on
// the LOCAL project profile; the remote target is resolved lazily, only when
// a remote picker/preset or the upload offer needs it (so a bare `actions`
// never opens an SSH connection).
func RunActions(ctx context.Context, opts ActionsOpts) (ActionsResult, error) {
	p, err := parseActionsArgs(opts.Args)
	if err != nil {
		return ActionsResult{}, err
	}

	// Lazy, cached remote resolution — shared by the wizard's remote picker,
	// the remote addons preset, and the upload offer.
	var cached *remoteShellContext
	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	resolveRemote := func() (remoteShellContext, error) {
		if cached != nil {
			return *cached, nil
		}
		rsc, rerr := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, logFn)
		if rerr != nil {
			return remoteShellContext{}, rerr
		}
		cached = &rsc
		return rsc, nil
	}

	switch p.sub {
	case "add":
		return ActionsResult{Sub: "add"}, runActionsAdd(ctx, opts, resolveRemote)
	case "edit":
		return ActionsResult{Sub: "edit"}, runActionsEdit(ctx, opts, p, resolveRemote)
	case "rm":
		return ActionsResult{Sub: "rm"}, runActionsRm(ctx, opts, p, resolveRemote)
	default:
		return runActionsList(ctx, opts, p, resolveRemote)
	}
}

// runActionsList shows the effective action list. Without --from/--remote it
// stays local (no SSH); with an explicit remote it resolves the server
// profile and reports which side wins (the Unit 92 wholesale rule).
func runActionsList(ctx context.Context, opts ActionsOpts, p actionsArgs, resolveRemote func() (remoteShellContext, error)) (ActionsResult, error) {
	source := "local"
	actions := opts.Cfg.DeployActions
	if p.from != "" || p.remote {
		rsc, err := resolveRemote()
		if err != nil {
			return ActionsResult{}, err
		}
		var verr error
		actions, source, verr = resolveDeployActions(rsc.prof, opts.Cfg, false)
		if verr != nil {
			return ActionsResult{}, verr
		}
	} else if err := config.ValidateDeployActions(actions); err != nil {
		return ActionsResult{}, err
	}
	res := ActionsResult{Sub: "list", Source: source, Actions: actions, JSON: p.jsonOut}
	if res.Actions == nil {
		res.Actions = []config.DeployAction{}
	}
	if !p.jsonOut {
		renderActionsTable(opts, res)
	}
	return res, nil
}

// runActionsAdd runs the create wizard and appends the action to the local
// list, then offers to upload the set to the server.
func runActionsAdd(ctx context.Context, opts ActionsOpts, resolveRemote func() (remoteShellContext, error)) error {
	a, err := actionWizard(ctx, opts, nil, resolveRemote)
	if err != nil {
		return err
	}
	next := append(append([]config.DeployAction(nil), opts.Cfg.DeployActions...), a)
	if err := saveActions(opts, next); err != nil {
		return err
	}
	opts.log("INFO", "", "action added", opts.Cfg.DBName,
		[2]string{"name", a.Name}, [2]string{"phase", a.Phase}, [2]string{"where", a.Where})
	return offerUploadActions(ctx, opts, resolveRemote, next)
}

// runActionsEdit edits an existing action in place (order preserved).
func runActionsEdit(ctx context.Context, opts ActionsOpts, p actionsArgs, resolveRemote func() (remoteShellContext, error)) error {
	idx, err := pickActionIndex(opts, p.name, "Edit which action?")
	if err != nil {
		return err
	}
	existing := opts.Cfg.DeployActions[idx]
	a, err := actionWizard(ctx, opts, &existing, resolveRemote)
	if err != nil {
		return err
	}
	next := append([]config.DeployAction(nil), opts.Cfg.DeployActions...)
	next[idx] = a
	if err := saveActions(opts, next); err != nil {
		return err
	}
	opts.log("INFO", "", "action updated", opts.Cfg.DBName, [2]string{"name", a.Name})
	return offerUploadActions(ctx, opts, resolveRemote, next)
}

// runActionsRm deletes an action after a red confirm (--force skips it).
func runActionsRm(ctx context.Context, opts ActionsOpts, p actionsArgs, resolveRemote func() (remoteShellContext, error)) error {
	idx, err := pickActionIndex(opts, p.name, "Remove which action?")
	if err != nil {
		return err
	}
	target := opts.Cfg.DeployActions[idx]
	if !p.force {
		if err := requireTTY("pass --force to remove non-interactively"); err != nil {
			return err
		}
		red := lipgloss.NewStyle().Foreground(opts.Palette.Error).Bold(true).Render(target.Name)
		ok := false
		form := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Remove deploy action " + red + "?").
				Affirmative("Remove").Negative("Cancel").Value(&ok),
		)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
		if ferr := form.Run(); ferr != nil {
			return ferr
		}
		if !ok {
			return ErrCancelled
		}
	}
	next := append(append([]config.DeployAction(nil), opts.Cfg.DeployActions[:idx]...), opts.Cfg.DeployActions[idx+1:]...)
	if err := saveActions(opts, next); err != nil {
		return err
	}
	opts.log("INFO", "", "action removed", opts.Cfg.DBName, [2]string{"name", target.Name})
	return offerUploadActions(ctx, opts, resolveRemote, next)
}

// pickActionIndex resolves the target action index from a name positional or,
// when absent, a single-select picker over the local action names.
func pickActionIndex(opts ActionsOpts, name, title string) (int, error) {
	list := opts.Cfg.DeployActions
	if len(list) == 0 {
		return 0, fmt.Errorf("%w: no deploy actions declared locally", ErrUsage)
	}
	if name != "" {
		for i, a := range list {
			if a.Name == name {
				return i, nil
			}
		}
		return 0, fmt.Errorf("%w: no local action named %q", ErrUsage, name)
	}
	names := make([]string, len(list))
	for i, a := range list {
		names[i] = a.Name
	}
	picked, err := runSingleFuzzyPickerStaged(title, names, opts.Palette, opts.Cfg.Stage)
	if err != nil {
		return 0, err
	}
	for i, a := range list {
		if a.Name == picked {
			return i, nil
		}
	}
	return 0, ErrCancelled
}

// saveActions validates and persists the local action list to the project
// profile.
func saveActions(opts ActionsOpts, actions []config.DeployAction) error {
	if err := config.ValidateDeployActions(actions); err != nil {
		return err
	}
	next := *opts.Cfg
	next.DeployActions = actions
	if err := config.SaveProject(&next); err != nil {
		return fmt.Errorf("save actions: %w", err)
	}
	opts.Cfg.DeployActions = actions
	return nil
}

// renderActionsTable prints the styled list (name·phase·where·exec_path·run)
// plus a source footer. run is middle-truncated so a long command doesn't
// wrap the row.
func renderActionsTable(opts ActionsOpts, res ActionsResult) {
	if len(res.Actions) == 0 {
		opts.stream("no deploy actions declared")
	} else {
		nameW, phaseW, whereW, pathW := len("NAME"), len("PHASE"), len("WHERE"), len("EXEC_PATH")
		runOf := func(a config.DeployAction) string { return truncateMiddle(a.Run, 48) }
		for _, a := range res.Actions {
			nameW = max2(nameW, len(a.Name))
			phaseW = max2(phaseW, len(a.Phase))
			whereW = max2(whereW, len(a.Where))
			pathW = max2(pathW, len(actionPathLabel(a.ExecPath)))
		}
		opts.stream(fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %s",
			nameW, "NAME", phaseW, "PHASE", whereW, "WHERE", pathW, "EXEC_PATH", "RUN"))
		for _, a := range res.Actions {
			opts.stream(fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %s",
				nameW, a.Name, phaseW, a.Phase, whereW, a.Where,
				pathW, actionPathLabel(a.ExecPath), runOf(a)))
		}
	}
	opts.log("INFO", "", "actions listed", opts.Cfg.DBName,
		[2]string{"count", fmt.Sprintf("%d", len(res.Actions))}, [2]string{"source", res.Source})
	if res.Source == "server" {
		opts.log("WARNING", "", "server actions win — local edits apply only if the server list is removed", opts.Cfg.DBName)
	}
}

// actionPathLabel renders an exec_path for the table (empty → "(root)").
func actionPathLabel(p string) string {
	if strings.TrimSpace(p) == "" {
		return "(root)"
	}
	return p
}

// truncateMiddle shortens s to width runes with a middle ellipsis.
func truncateMiddle(s string, width int) string {
	r := []rune(s)
	if len(r) <= width || width < 5 {
		return s
	}
	head := (width - 1) / 2
	tail := width - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// listLocalDirs returns the immediate subdirectory names of dir (dotdirs
// skipped), sorted — the local mirror of listRemoteDirs.
func listLocalDirs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// pickLocalDir browses the local filesystem level by level (the local mirror
// of pickRemoteDir), returning the selected absolute directory.
func pickLocalDir(start string, palette theme.Palette, stage string) (string, error) {
	if err := requireTTY("type the path instead"); err != nil {
		return "", err
	}
	cur, err := filepath.Abs(start)
	if err != nil {
		cur = start
	}
	for {
		dirs, lerr := listLocalDirs(cur)
		if lerr != nil {
			return "", fmt.Errorf("list %s: %w", cur, lerr)
		}
		choice, perr := runSingleFuzzyPickerStaged("Exec directory: "+cur,
			dirPickerEntries(filepath.ToSlash(cur), dirs), palette, stage)
		if perr != nil {
			return "", perr
		}
		switch choice {
		case dirPickerUse:
			return cur, nil
		case dirPickerUp:
			if parent := filepath.Dir(cur); parent != cur {
				cur = parent
			}
		default:
			cur = filepath.Join(cur, choice)
		}
	}
}
