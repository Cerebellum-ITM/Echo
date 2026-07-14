package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/config"
)

// exec_path wizard preset labels.
const (
	execPresetRoot   = "Project root (default)"
	execPresetAddons = "Addons directory"
	execPresetPick   = "Pick a directory…"
	execPresetType   = "Type a path"
)

// actionWizard walks name → phase → where → exec_path → run as a sequence of
// huh forms (the picker can't live inside a form, so the steps run
// separately). existing != nil pre-fills the fields for an edit. The remote
// target is resolved lazily via resolveRemote, only if a remote picker or the
// remote addons preset is reached.
func actionWizard(ctx context.Context, opts ActionsOpts, existing *config.DeployAction, resolveRemote func() (remoteShellContext, error)) (config.DeployAction, error) {
	if err := requireTTY("actions add/edit need a terminal"); err != nil {
		return config.DeployAction{}, err
	}
	a := config.DeployAction{Phase: config.PhasePrePush, Where: config.WhereLocal}
	if existing != nil {
		a = *existing
	}

	name, phase, where := a.Name, a.Phase, a.Where
	form1 := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Name").
			Description("Unique identifier for this action").
			Value(&name).
			Validate(nonEmptyField("name")),
		huh.NewSelect[string]().
			Title("Phase").
			Description("When in the deploy lifecycle it runs").
			Options(
				huh.NewOption("pre_push — before pushing code", config.PhasePrePush),
				huh.NewOption("post_push — after push, before stop (e.g. rebuild image)", config.PhasePostPush),
				huh.NewOption("pre_deploy — right before the container stop", config.PhasePreDeploy),
				huh.NewOption("post_deploy — after the run verified green", config.PhasePostDeploy),
			).
			Value(&phase),
		huh.NewSelect[string]().
			Title("Where").
			Description("Run on the remote host or locally").
			Options(
				huh.NewOption("remote — on the deploy host over SSH", config.WhereRemote),
				huh.NewOption("local — on this machine", config.WhereLocal),
			).
			Value(&where),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout).WithShowHelp(true)
	if err := form1.Run(); err != nil {
		return config.DeployAction{}, err
	}
	a.Name, a.Phase, a.Where = strings.TrimSpace(name), phase, where

	execPath, err := pickExecPath(ctx, opts, a, existing, resolveRemote)
	if err != nil {
		return config.DeployAction{}, err
	}
	a.ExecPath = execPath

	run := a.Run
	form2 := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Command to run").
			Description("Executed with `sh -c` in the exec directory").
			Value(&run).
			Validate(nonEmptyField("run")),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout).WithShowHelp(true)
	if err := form2.Run(); err != nil {
		return config.DeployAction{}, err
	}
	a.Run = strings.TrimSpace(run)
	return a, nil
}

// pickExecPath runs the exec_path preset select and resolves the chosen
// directory: root ("" ), the addons preset (resolved literal), the directory
// picker matched to where, or a typed path. In edit mode the select
// pre-selects from the existing value.
func pickExecPath(ctx context.Context, opts ActionsOpts, a config.DeployAction, existing *config.DeployAction, resolveRemote func() (remoteShellContext, error)) (string, error) {
	choice := execPresetRoot
	if existing != nil && strings.TrimSpace(a.ExecPath) != "" {
		if a.ExecPath == addonsPresetPath(a.Where, opts, resolveRemote) {
			choice = execPresetAddons
		} else {
			choice = execPresetType
		}
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Execution directory").
			Description("Where the command runs (relative paths hang off the project dir)").
			Options(
				huh.NewOption(execPresetRoot, execPresetRoot),
				huh.NewOption(execPresetAddons, execPresetAddons),
				huh.NewOption(execPresetPick, execPresetPick),
				huh.NewOption(execPresetType, execPresetType),
			).
			Value(&choice),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return "", err
	}

	switch choice {
	case execPresetRoot:
		return "", nil
	case execPresetAddons:
		return addonsPresetPath(a.Where, opts, resolveRemote), nil
	case execPresetPick:
		return pickExecDir(ctx, opts, a, resolveRemote)
	default: // execPresetType
		return typeExecPath(opts, a.ExecPath)
	}
}

// pickExecDir opens the directory picker matched to the action's where,
// normalizing the absolute selection to a path relative to the corresponding
// root when it falls under it (portable across hosts).
func pickExecDir(ctx context.Context, opts ActionsOpts, a config.DeployAction, resolveRemote func() (remoteShellContext, error)) (string, error) {
	if a.Where == config.WhereRemote {
		rsc, err := resolveRemote()
		if err != nil {
			opts.log("WARNING", "", "no remote target — type the path instead", opts.Cfg.DBName,
				[2]string{"err", err.Error()})
			return typeExecPath(opts, a.ExecPath)
		}
		picked, perr := pickRemoteDir(ctx, rsc,
			PushOpts{Cfg: opts.Cfg, Palette: opts.Palette, Log: opts.Log}, rsc.remotePath)
		if perr != nil {
			return "", perr
		}
		if rel, ok := underPath(rsc.remotePath, picked); ok {
			return rel, nil
		}
		return picked, nil
	}
	picked, err := pickLocalDir(opts.Root, opts.Palette, opts.Cfg.Stage)
	if err != nil {
		return "", err
	}
	if rel, ok := underPath(opts.Root, picked); ok {
		return rel, nil
	}
	return picked, nil
}

// typeExecPath prompts for a free-text exec path (empty allowed → root).
func typeExecPath(opts ActionsOpts, current string) (string, error) {
	val := current
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Exec path").
			Description("Relative (under the project dir) or absolute; blank = root").
			Value(&val),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(val), nil
}

// addonsPresetPath resolves the "addons directory" preset: the first relative
// addons path of the remote profile (remote) or the local config (local),
// falling back to "addons". Stored as the resolved literal (relative).
func addonsPresetPath(where string, opts ActionsOpts, resolveRemote func() (remoteShellContext, error)) string {
	if where == config.WhereRemote {
		if rsc, err := resolveRemote(); err == nil {
			return firstRelAddons(rsc.prof.AddonsPaths)
		}
		return "addons"
	}
	return firstRelAddons(opts.Cfg.AddonsPaths)
}

// firstRelAddons returns the first usable relative addons subpath, else
// "addons". Absolute (container) paths and the root are skipped.
func firstRelAddons(paths []string) string {
	for _, p := range paths {
		if p != "" && p != "." && !path.IsAbs(p) {
			return p
		}
	}
	return "addons"
}

// nonEmptyField returns a huh validator rejecting a blank value.
func nonEmptyField(field string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
}

// offerUploadActions asks (default No) whether to upload the local action set
// to the server's project profile, and on yes rewrites its [[deploy.actions]]
// over SSH, preserving every other key. Skipped silently when no remote
// target resolves. A prod target gets the red confirm first.
func offerUploadActions(ctx context.Context, opts ActionsOpts, resolveRemote func() (remoteShellContext, error), actions []config.DeployAction) error {
	if !stdinIsTTY() {
		return nil
	}
	rsc, err := resolveRemote()
	if err != nil {
		return nil // no target to upload to — local save already succeeded
	}
	up := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Upload these actions to the server profile?").
			Description("Rewrites [[deploy.actions]] on " + rsc.sshHost + " (other keys preserved).").
			Affirmative("Upload").Negative("No").Value(&up),
	)).WithTheme(BuildHuhTheme(opts.Palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
	if ferr := form.Run(); ferr != nil {
		return nil // declining the offer is not a command failure
	}
	if !up {
		return nil
	}
	if cerr := confirmRemoteProd(opts.Palette, "upload actions", rsc, opts.Args); cerr != nil {
		return cerr
	}
	return uploadActionsToServer(ctx, rsc, actions, opts)
}

// uploadActionsToServer reads the server's projects/<key>.toml, splices in the
// new [[deploy.actions]] (config.WithDeployActions preserves the rest), and
// writes it back over SSH via stdin.
func uploadActionsToServer(ctx context.Context, rsc remoteShellContext, actions []config.DeployAction, opts ActionsOpts) error {
	key := config.ProjectKey(rsc.remotePath)
	remoteFile := "~/.config/echo/projects/" + key + ".toml"
	existing, _ := runSSH(ctx, rsc.sshHost, "cat "+remoteFile, nil) // absent → empty
	updated, err := config.WithDeployActions(existing, actions)
	if err != nil {
		return fmt.Errorf("compose remote profile: %w", err)
	}
	// Ensure the projects dir exists, then write the file from stdin.
	if _, derr := runSSH(ctx, rsc.sshHost, "mkdir -p ~/.config/echo/projects", nil); derr != nil {
		return fmt.Errorf("prepare remote profile dir: %w", derr)
	}
	if _, werr := runSSH(ctx, rsc.sshHost, "cat > "+remoteFile, updated); werr != nil {
		return fmt.Errorf("write remote profile: %w", werr)
	}
	opts.log("INFO", "", "uploaded actions to server", rsc.prof.DBName,
		[2]string{"host", rsc.sshHost}, [2]string{"profile", "projects/" + key + ".toml"},
		[2]string{"count", fmt.Sprintf("%d", len(actions))})
	return nil
}
