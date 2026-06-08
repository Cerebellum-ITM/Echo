package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

const addTargetSentinel = "➕  Register a new target…"

// RunDirectConnect is the projectless entry point used by `echo connect
// [<name>] [<login>] [--add] [--all] [--force]`. It resolves (or
// registers) a named remote target from the global config and runs the
// normal connect flow against it, so no local Odoo project is required.
func RunDirectConnect(ctx context.Context, args []string) error {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	palette := theme.PaletteByName(cfg.Theme)

	name, login, add, passthrough := parseDirectArgs(args)

	target, err := resolveDirectTarget(ctx, cfg, palette, name, add)
	if err != nil {
		return err
	}

	connectCfg := &config.Config{
		Theme:             cfg.Theme,
		ConnectSSHHost:    target.SSHHost,
		ConnectRemotePath: target.RemotePath,
		ConnectChromePath: target.ChromePath,
	}
	connectArgs := passthrough
	if login != "" {
		connectArgs = append([]string{login}, connectArgs...)
	}

	res, err := RunConnect(ctx, ConnectOpts{
		Cfg:     connectCfg,
		Args:    connectArgs,
		Palette: palette,
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ Session minted for %q (uid=%d) on target %q\n", res.Login, res.UID, target.Name)
	fmt.Printf("  Opening Chrome at %s/odoo (logged in)\n", res.BaseURL)
	return nil
}

func parseDirectArgs(args []string) (name, login string, add bool, passthrough []string) {
	var positionals []string
	for _, a := range args {
		switch {
		case a == "--add":
			add = true
		case a == "--all", a == "--force":
			passthrough = append(passthrough, a)
		case strings.HasPrefix(a, "-"):
			// unknown flag, ignore for forward-compat
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) > 0 {
		name = positionals[0]
	}
	if len(positionals) > 1 {
		login = positionals[1]
	}
	return
}

// resolveDirectTarget returns the target to connect to: by explicit
// name, by interactive pick among registered targets, or by registering
// a new one (when --add is passed, no targets exist, or the user picks
// the "register" entry).
func resolveDirectTarget(ctx context.Context, cfg *config.Config, palette theme.Palette, name string, add bool) (config.ConnectTarget, error) {
	if add {
		return registerTarget(ctx, palette)
	}
	if name != "" {
		for _, t := range cfg.ConnectTargets {
			if t.Name == name {
				return t, nil
			}
		}
		return config.ConnectTarget{}, fmt.Errorf("no connect target %q (have: %s)",
			name, targetNames(cfg.ConnectTargets))
	}
	if len(cfg.ConnectTargets) == 0 {
		return registerTarget(ctx, palette)
	}

	labels := make([]string, 0, len(cfg.ConnectTargets)+1)
	for _, t := range cfg.ConnectTargets {
		labels = append(labels, fmt.Sprintf("%-16s  %s:%s", t.Name, t.SSHHost, t.RemotePath))
	}
	labels = append(labels, addTargetSentinel)

	chosen, err := runSingleFuzzyPicker("Select connect target", labels, palette)
	if err != nil {
		return config.ConnectTarget{}, err
	}
	if chosen == addTargetSentinel {
		return registerTarget(ctx, palette)
	}
	for i, lbl := range labels[:len(cfg.ConnectTargets)] {
		if lbl == chosen {
			return cfg.ConnectTargets[i], nil
		}
	}
	return config.ConnectTarget{}, fmt.Errorf("picker returned unknown label %q", chosen)
}

// registerTarget walks the user through creating a new named target:
// pick an SSH host from ~/.ssh/config, pick one of that host's existing
// Echo projects (read over SSH), and name it. Nothing is scanned beyond
// Echo's own config on the server.
func registerTarget(ctx context.Context, palette theme.Palette) (config.ConnectTarget, error) {
	hosts := sshConfigHosts()
	if len(hosts) == 0 {
		return config.ConnectTarget{}, fmt.Errorf("no Host entries found in ~/.ssh/config")
	}
	host, err := runSingleFuzzyPicker("Select SSH host (from ~/.ssh/config)", hosts, palette)
	if err != nil {
		return config.ConnectTarget{}, err
	}

	projects, err := remoteEchoProjects(ctx, host)
	if err != nil {
		return config.ConnectTarget{}, err
	}
	if len(projects) == 0 {
		return config.ConnectTarget{}, fmt.Errorf(
			"no Echo projects with a stored path on %q — run Echo there once (update it if needed) so its profiles record project_path", host)
	}

	labels := make([]string, len(projects))
	for i, p := range projects {
		labels[i] = fmt.Sprintf("%-20s  %s", p.DBName, p.ProjectPath)
	}
	chosenLabel, err := runSingleFuzzyPicker("Select project on "+host, labels, palette)
	if err != nil {
		return config.ConnectTarget{}, err
	}
	var picked config.ProjectInfo
	for i, lbl := range labels {
		if lbl == chosenLabel {
			picked = projects[i]
			break
		}
	}

	name := defaultTargetName(picked)
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Target name").
			Description("How you'll refer to it: echo connect <name>").
			Value(&name),
	)).WithTheme(BuildHuhTheme(palette)).Run(); err != nil {
		return config.ConnectTarget{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return config.ConnectTarget{}, fmt.Errorf("target name is required")
	}

	target := config.ConnectTarget{
		Name:       name,
		SSHHost:    host,
		RemotePath: picked.ProjectPath,
		DBName:     picked.DBName,
	}
	if err := config.SaveConnectTarget(target); err != nil {
		return config.ConnectTarget{}, fmt.Errorf("save target: %w", err)
	}
	fmt.Printf("✓ Registered target %q → %s:%s\n", target.Name, target.SSHHost, target.RemotePath)
	return target, nil
}

// remoteEchoProjects reads the server's Echo project profiles over SSH
// and returns those that recorded a project_path (older profiles that
// predate the field are skipped — they can't be used as a target).
func remoteEchoProjects(ctx context.Context, host string) ([]config.ProjectInfo, error) {
	const sep = "==ECHO-PROFILE=="
	listCmd := `for f in ~/.config/echo/projects/*.toml; do [ -e "$f" ] && { echo '` + sep + `'; cat "$f"; }; done`
	out, err := runSSH(ctx, host, listCmd, nil)
	if err != nil {
		return nil, fmt.Errorf("read Echo config on %q: %w", host, err)
	}
	var projects []config.ProjectInfo
	for _, chunk := range strings.Split(string(out), sep) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		info := config.ParseProjectInfo([]byte(chunk))
		if info.ProjectPath != "" {
			projects = append(projects, info)
		}
	}
	return projects, nil
}

func defaultTargetName(p config.ProjectInfo) string {
	if p.DBName != "" {
		return p.DBName
	}
	return filepath.Base(p.ProjectPath)
}

func targetNames(targets []config.ConnectTarget) string {
	if len(targets) == 0 {
		return "none registered — run `echo connect --add`"
	}
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}
