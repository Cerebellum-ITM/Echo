package cmd

import (
	"context"
	"os"

	"github.com/charmbracelet/huh"
)

// updateBuildTarget is where a built `update` will run and the stage to tint
// the picker with. Exactly one of {local, named, linked} holds:
//   - local:  remote == false
//   - named:  remote == true, fromName != "" (bake --from=<name>)
//   - linked: remote == true, linked == true (bake --remote)
type updateBuildTarget struct {
	remote   bool
	linked   bool
	fromName string
	rsc      remoteShellContext
	stage    string
}

// runUpdateBuild is the remote/source-aware builder for `update --build`. It
// resolves WHERE the update runs and WHICH source the module list comes from
// before the picker — so those choices govern what it offers, instead of the
// generic positional-first flow that always lists local addons. Composes
// `update <mod...> [--from=<t>|--remote] [--i18n] [--level=<lvl>]`; --installed
// is never baked (explicit module names make it a runtime no-op).
func runUpdateBuild(ctx context.Context, opts BuildOpts) (BuildResult, error) {
	tgt, err := resolveUpdateBuildTarget(ctx, opts)
	if err != nil {
		return BuildResult{}, err
	}

	installed, err := pickUpdateSource(opts)
	if err != nil {
		return BuildResult{}, err
	}

	mods, err := updateBuildModules(ctx, opts, tgt, installed)
	if err != nil {
		return BuildResult{}, err
	}
	if len(mods) == 0 {
		return BuildResult{}, ErrNoModulesAvailable
	}
	picked, _, canceled, err := runFuzzyPickerCore("Modules to update", mods, nil, nil, nil, opts.Palette, tgt.stage)
	if err != nil {
		return BuildResult{}, err
	}
	if canceled || len(picked) == 0 {
		return BuildResult{}, ErrCancelled
	}

	// Only --i18n / --level compose with a hand-picked module set; --all/--last
	// would ignore it and --installed/--remote/--from are already decided.
	extras, err := gatherFlags(opts, []string{"--i18n", "--level"}, tgt.stage)
	if err != nil {
		return BuildResult{}, err
	}

	flags := append(updateBuildRemoteFlag(tgt), extras...)
	args := composeArgs(picked, flags)
	if opts.SkipDecide {
		return BuildResult{Args: args, Action: BuildRun}, nil
	}
	action, err := decideAction(opts, args)
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Args: args, Action: action}, nil
}

// updateBuildRemoteFlag bakes the target selector into the composed line:
// `--from=<name>` for a named target, `--remote` for this directory's link,
// nothing for a local update. `--installed` is intentionally never baked (an
// explicit module set makes it a runtime no-op — it only sourced the picker).
func updateBuildRemoteFlag(tgt updateBuildTarget) []chosenFlag {
	switch {
	case tgt.fromName != "":
		return []chosenFlag{{name: "--from", value: tgt.fromName, sep: "="}}
	case tgt.linked:
		return []chosenFlag{{name: "--remote"}}
	}
	return nil
}

// resolveUpdateBuildTarget picks where the update runs. A sequence's
// pre-selected opts.From skips the prompt. Otherwise a "Where to update?"
// select lists `local`, each named connect target, and this directory's
// `link` (when set); with no remote option the step is skipped (local). A
// remote choice resolves the profile via resolveRemoteShell so the picker can
// list its modules and be tinted by its stage.
func resolveUpdateBuildTarget(ctx context.Context, opts BuildOpts) (updateBuildTarget, error) {
	if opts.From != "" {
		rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, opts.From, updateBuildLog(opts))
		if err != nil {
			return updateBuildTarget{}, err
		}
		return updateBuildTarget{remote: true, fromName: opts.From, rsc: rsc, stage: rsc.target.stage}, nil
	}

	type choice struct {
		label, kind, name string
	}
	choices := []choice{{label: "local", kind: "local"}}
	for _, t := range opts.Cfg.ConnectTargets {
		if t.SSHHost != "" && t.RemotePath != "" {
			choices = append(choices, choice{label: t.Name + "  (remote)", kind: "named", name: t.Name})
		}
	}
	if opts.Cfg.ConnectSSHHost != "" && opts.Cfg.ConnectRemotePath != "" {
		choices = append(choices, choice{label: "this directory's link  (remote)", kind: "linked"})
	}
	if len(choices) == 1 { // local only — no prompt
		return updateBuildTarget{stage: opts.Cfg.Stage}, nil
	}

	hopts := make([]huh.Option[int], len(choices))
	for i, c := range choices {
		hopts[i] = huh.NewOption(c.label, i)
	}
	var sel int
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[int]().Title("Where to update?").Options(hopts...).Value(&sel),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return updateBuildTarget{}, err
	}

	c := choices[sel]
	switch c.kind {
	case "named":
		rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, c.name, updateBuildLog(opts))
		if err != nil {
			return updateBuildTarget{}, err
		}
		return updateBuildTarget{remote: true, fromName: c.name, rsc: rsc, stage: rsc.target.stage}, nil
	case "linked":
		rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, "", updateBuildLog(opts))
		if err != nil {
			return updateBuildTarget{}, err
		}
		return updateBuildTarget{remote: true, linked: true, rsc: rsc, stage: rsc.target.stage}, nil
	default: // local
		return updateBuildTarget{stage: opts.Cfg.Stage}, nil
	}
}

// pickUpdateSource asks whether the module picker draws from the project's
// addons or every module installed in the database (the --installed source).
func pickUpdateSource(opts BuildOpts) (installed bool, err error) {
	var src int
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[int]().Title("Module source").Options(
			huh.NewOption("project addons", 0),
			huh.NewOption("installed in the database (--installed)", 1),
		).Value(&src),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return false, err
	}
	return src == 1, nil
}

// updateBuildModules dispatches the 2×2 module-source matrix
// (local/remote × addons/installed) to the right provider.
func updateBuildModules(ctx context.Context, opts BuildOpts, tgt updateBuildTarget, installed bool) ([]string, error) {
	if tgt.remote {
		if installed {
			return listRemoteModules(ctx, tgt.rsc.sshHost, tgt.rsc.remotePath, tgt.rsc.target, tgt.rsc.conn.User, tgt.rsc.target.dbName)
		}
		return listRemoteConfModules(ctx, tgt.rsc.sshHost, tgt.rsc.remotePath, tgt.rsc.target, tgt.rsc.prof.ConfPath, tgt.rsc.prof.AddonsPaths)
	}
	mopts := ModulesOpts{Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette}
	if installed {
		return installedModules(ctx, mopts)
	}
	return resolveModules(ctx, mopts)
}

// updateBuildLog adapts the build progress callbacks to the log signature
// resolveRemoteShell expects, so the SSH round-trips aren't silent.
func updateBuildLog(opts BuildOpts) func(level, sub, msg, db string, fields ...[2]string) {
	return func(level, _, msg, _ string, fields ...[2]string) {
		for _, f := range fields {
			msg += " " + f[0] + "=" + f[1]
		}
		if level == "WARNING" {
			warn(opts, msg)
		} else {
			info(opts, msg)
		}
	}
}
