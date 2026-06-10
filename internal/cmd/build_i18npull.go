package cmd

import (
	"context"
	"errors"
	"fmt"
)

// runI18nPullBuild is the remote-aware build-mode flow for `i18n-pull`. Its
// module candidates live on the remote, so the target must be resolved
// first — unlike the local commands whose positional list is cheap. It
// resolves a connect target (baking `--from=<name>` so the composed line is
// reproducible), lists that remote's own modules for the picker, prompts
// for the language, and composes `i18n-pull <module> <lang> [--from=<name>]`.
//
// `--all` / `--installed` are deliberately not offered here: once a single
// module is picked they are meaningless (`--all` would ignore it, like
// `update <mods> --all`). Use them directly if you want the bulk flow.
func runI18nPullBuild(ctx context.Context, opts BuildOpts) (BuildResult, error) {
	fromName, sshHost, remotePath, err := resolvePullBuildTarget(opts)
	if err != nil {
		return BuildResult{}, err
	}

	pullOpts := i18nPullBuildOpts(opts)
	modules, err := remoteI18nModules(ctx, pullOpts, sshHost, remotePath)
	if err != nil {
		return BuildResult{}, err
	}
	if len(modules) == 0 {
		return BuildResult{}, ErrNoModulesAvailable
	}
	module, err := runSingleFuzzyPicker("Module to pull translations for", modules, opts.Palette)
	if err != nil {
		return BuildResult{}, err
	}

	lang, err := i18nLangInput(opts)
	if err != nil {
		return BuildResult{}, err
	}
	positionals := []string{module}
	if lang != "" {
		positionals = append(positionals, lang)
	}

	var flags []chosenFlag
	if fromName != "" {
		flags = append(flags, chosenFlag{name: "--from", value: fromName, sep: "="})
	}

	args := composeArgs(positionals, flags)
	action, err := decideAction(opts, args)
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{Args: args, Action: action}, nil
}

// resolvePullBuildTarget resolves which remote i18n-pull will read from,
// preferring a named connect target (so its name can be baked into
// `--from`); it falls back to the project's own `[connect]` config (no name
// to bake). Returns ErrNoPullRemote when neither is configured.
func resolvePullBuildTarget(opts BuildOpts) (fromName, sshHost, remotePath string, err error) {
	name, perr := pickPullTarget(i18nPullBuildOpts(opts))
	switch {
	case perr == nil:
		sshHost, remotePath, err = resolvePullRemote(opts.Cfg, name)
		return name, sshHost, remotePath, err
	case errors.Is(perr, ErrNoPullRemote):
		// No named targets — fall back to the project's own [connect].
		sshHost, remotePath, err = resolvePullRemote(opts.Cfg, "")
		return "", sshHost, remotePath, err
	default:
		return "", "", "", perr
	}
}

// i18nPullBuildOpts adapts a BuildOpts into the I18nPullOpts the remote
// helpers expect, routing their Odoo-style progress lines to the build
// callbacks (WARNING → Warnf, else → Infof) so the SSH waits aren't silent.
func i18nPullBuildOpts(opts BuildOpts) I18nPullOpts {
	return I18nPullOpts{
		Cfg:     opts.Cfg,
		Root:    opts.Root,
		Palette: opts.Palette,
		Log: func(level, _, msg, _ string, fields ...[2]string) {
			for _, f := range fields {
				msg += " " + f[0] + "=" + f[1]
			}
			if level == "WARNING" {
				warn(opts, msg)
			} else {
				info(opts, msg)
			}
		},
	}
}

// remoteI18nModules fetches the remote project's own module list (the
// default i18n-pull source: the modules under the remote addons paths),
// making the same SSH round-trips RunI18nPull does — surfaced through the
// adapter's Log so the waits aren't silent.
func remoteI18nModules(ctx context.Context, pullOpts I18nPullOpts, sshHost, remotePath string) ([]string, error) {
	pullOpts.log("INFO", "remote", "target resolved", "",
		[2]string{"host", sshHost}, [2]string{"path", remotePath})

	cfgRemote := *pullOpts.Cfg
	cfgRemote.ConnectSSHHost = sshHost
	cfgRemote.ConnectRemotePath = remotePath
	pullOpts.log("INFO", "remote", "reading remote profile", "", [2]string{"host", sshHost})
	prof, err := fetchRemoteProfile(ctx, ConnectOpts{Cfg: &cfgRemote, Root: pullOpts.Root})
	if err != nil {
		return nil, err
	}
	target := connectTarget{
		remote:        true,
		composeCmd:    prof.ComposeCmd,
		odooContainer: prof.OdooContainer,
		dbContainer:   prof.DBContainer,
		dbName:        prof.DBName,
		stage:         prof.Stage,
	}
	pullOpts.log("INFO", "remote", "listing modules", prof.DBName, [2]string{"source", "project addons"})
	mods, err := listRemoteConfModules(ctx, sshHost, remotePath, target, prof.ConfPath, prof.AddonsPaths)
	if err != nil {
		return nil, fmt.Errorf("list remote modules: %w", err)
	}
	pullOpts.log("INFO", "remote", fmt.Sprintf("%d module(s) found", len(mods)), prof.DBName)
	return mods, nil
}
