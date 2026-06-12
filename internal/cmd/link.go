package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/theme"
)

// ErrNoConnectTargets is returned when `link` has nothing to bind to:
// the global config holds no named connect targets.
var ErrNoConnectTargets = errors.New(
	"no connect targets configured — register one with `echo connect <name>`")

// LinkOpts configures a `link` run.
type LinkOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log emits one Odoo-style progress line (rendered by the REPL through
	// emitOdooLog under `echo.link[.sub]`), mirroring i18n-pull's logger.
	Log func(level, sub, msg, db string, fields ...[2]string)
	// StreamOut receives the remote `compose ps` lines streamed by
	// `link --show` when the structured read fails. Nil discards them.
	StreamOut func(string)
	// OnPS, when set, receives the remote containers parsed from
	// `compose ps --format json` (plus the remote database name) so the
	// REPL can render the same styled table the local `ps` uses. Nil (or
	// a failed structured read) falls back to streaming the raw
	// `compose ps` through StreamOut.
	OnPS func(rows []docker.PSContainer, db string)
}

// log emits a progress line when a logger is set; a no-op otherwise.
func (o LinkOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// linkArgs is the parsed shape of the link input.
type linkArgs struct {
	target string
	show   bool
	rm     bool
}

// parseLinkArgs extracts --show/--rm and the optional target positional.
// The flags are mutually exclusive with each other and with a target.
func parseLinkArgs(args []string) (linkArgs, error) {
	var out linkArgs
	for _, a := range args {
		switch {
		case a == "--show":
			out.show = true
		case a == "--rm":
			out.rm = true
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("unknown flag: %s", a)
		default:
			if out.target != "" {
				return out, fmt.Errorf("link takes a single target name")
			}
			out.target = a
		}
	}
	if out.show && out.rm {
		return out, fmt.Errorf("--show and --rm are mutually exclusive")
	}
	if (out.show || out.rm) && out.target != "" {
		return out, fmt.Errorf("--show/--rm take no target argument")
	}
	return out, nil
}

// RunLink binds the current project directory to a named connect target by
// writing the target's ssh_host/remote_path into the per-project [connect]
// section — the binding `connect`, `i18n-pull` and `deploy` consume. With
// --show it reports the current binding, probes the remote profile and
// streams the remote `compose ps`; with --rm it removes the binding.
func RunLink(ctx context.Context, opts LinkOpts) error {
	p, err := parseLinkArgs(opts.Args)
	if err != nil {
		return err
	}
	switch {
	case p.rm:
		return runLinkRm(opts)
	case p.show:
		return runLinkShow(ctx, opts)
	}
	return runLinkBind(ctx, opts, p.target)
}

// runLinkBind resolves the target (explicit name, single auto-pick, or
// picker) and persists the binding. The save happens BEFORE the probe: a
// broken VPN must not lose the binding, so an unreachable remote is a
// WARNING, never a failure.
func runLinkBind(ctx context.Context, opts LinkOpts, name string) error {
	t, err := resolveLinkTarget(opts, name)
	if err != nil {
		return err
	}
	opts.Cfg.ConnectSSHHost = t.SSHHost
	opts.Cfg.ConnectRemotePath = t.RemotePath
	if t.ChromePath != "" {
		opts.Cfg.ConnectChromePath = t.ChromePath
	}
	if err := config.SaveProject(opts.Cfg); err != nil {
		return fmt.Errorf("save project config: %w", err)
	}
	opts.log("INFO", "", "saved", "",
		[2]string{"target", t.Name},
		[2]string{"host", t.SSHHost},
		[2]string{"path", t.RemotePath})
	probeLink(ctx, opts, t.Name)
	return nil
}

// runLinkShow reports the current binding, probes the remote profile, and
// streams the remote `compose ps` so the binding is verified end to end.
func runLinkShow(ctx context.Context, opts LinkOpts) error {
	if opts.Cfg.ConnectSSHHost == "" || opts.Cfg.ConnectRemotePath == "" {
		opts.log("INFO", "", "not linked", "",
			[2]string{"hint", "run `link <target>` to bind this directory"})
		return nil
	}
	name := linkTargetName(opts.Cfg)
	fields := [][2]string{
		{"host", opts.Cfg.ConnectSSHHost},
		{"path", opts.Cfg.ConnectRemotePath},
	}
	if name != "" {
		fields = append([][2]string{{"target", name}}, fields...)
	}
	opts.log("INFO", "", "linked to", "", fields...)
	prof, ok := probeLink(ctx, opts, name)
	if !ok {
		return nil
	}
	opts.log("INFO", "remote", "remote containers", prof.DBName)
	if opts.OnPS != nil {
		jsonCmd := remoteComposeCmd(opts.Cfg.ConnectRemotePath, prof.ComposeCmd, "ps", "--format", "json")
		if out, err := runSSH(ctx, opts.Cfg.ConnectSSHHost, jsonCmd, nil); err == nil {
			if rows, perr := docker.ParsePS(out); perr == nil {
				opts.OnPS(rows, prof.DBName)
				return nil
			}
		}
		// Structured read failed — fall back to the raw stream below.
	}
	psCmd := remoteComposeCmd(opts.Cfg.ConnectRemotePath, prof.ComposeCmd, "ps")
	if err := runSSHStream(ctx, opts.Cfg.ConnectSSHHost, psCmd, nil, opts.StreamOut); err != nil {
		return fmt.Errorf("remote ps: %w", err)
	}
	return nil
}

// runLinkRm clears the per-project [connect] binding. Idempotent.
func runLinkRm(opts LinkOpts) error {
	if opts.Cfg.ConnectSSHHost == "" && opts.Cfg.ConnectRemotePath == "" &&
		opts.Cfg.ConnectChromePath == "" {
		opts.log("INFO", "", "not linked", "")
		return nil
	}
	opts.Cfg.ConnectSSHHost = ""
	opts.Cfg.ConnectRemotePath = ""
	opts.Cfg.ConnectChromePath = ""
	if err := config.SaveProject(opts.Cfg); err != nil {
		return fmt.Errorf("save project config: %w", err)
	}
	opts.log("INFO", "", "unlinked", "")
	return nil
}

// probeLink reads the remote Echo profile over SSH and reports it. The
// system-status line doubles as the "reachable" signal (same shape as
// connect / i18n-pull). Failure is a WARNING — the binding is config, not
// a connection — and reports ok=false so callers skip remote follow-ups.
func probeLink(ctx context.Context, opts LinkOpts, fromName string) (config.RemoteProfile, bool) {
	opts.log("INFO", "remote", "probing remote", "",
		[2]string{"host", opts.Cfg.ConnectSSHHost})
	prof, err := fetchRemoteProfile(ctx, ConnectOpts{Cfg: opts.Cfg, Root: opts.Root})
	if err != nil {
		opts.log("WARNING", "remote", "linked but unreachable", "",
			[2]string{"err", err.Error()})
		return config.RemoteProfile{}, false
	}
	opts.log("INFO", "system", "system", prof.DBName,
		statusFields(prof.OdooVersion, prof.Stage,
			statusProjectName(opts.Cfg, true, opts.Cfg.ConnectRemotePath, fromName),
			prof.DBName)...)
	opts.log("INFO", "", "linked", prof.DBName,
		[2]string{"stage", prof.Stage},
		[2]string{"db", prof.DBName})
	return prof, true
}

// resolveLinkTarget picks the connect target to bind: an explicit name is
// looked up (error listing the available names when unknown), no name with
// a single registered target auto-uses it, several open a TTY-guarded
// picker, none yields ErrNoConnectTargets.
func resolveLinkTarget(opts LinkOpts, name string) (config.ConnectTarget, error) {
	targets := opts.Cfg.ConnectTargets
	if name != "" {
		for _, t := range targets {
			if t.Name == name {
				return validLinkTarget(t)
			}
		}
		if len(targets) == 0 {
			return config.ConnectTarget{}, ErrNoConnectTargets
		}
		return config.ConnectTarget{}, fmt.Errorf(
			"unknown connect target: %s (available: %s)",
			name, strings.Join(connectTargetNames(targets), ", "))
	}
	return pickConnectTarget(targets, opts.Palette, "Select connect target to link", opts.Log)
}

// pickConnectTarget resolves a target when no name was given: none yields
// ErrNoConnectTargets, a single one is auto-used (with an info line),
// several open a TTY-guarded picker. Shared by `link` and `deploy`.
func pickConnectTarget(targets []config.ConnectTarget, palette theme.Palette, title string, log func(level, sub, msg, db string, fields ...[2]string)) (config.ConnectTarget, error) {
	switch len(targets) {
	case 0:
		return config.ConnectTarget{}, ErrNoConnectTargets
	case 1:
		if log != nil {
			log("INFO", "", "using connect target", "",
				[2]string{"target", targets[0].Name})
		}
		return validLinkTarget(targets[0])
	}
	labels := make([]string, len(targets))
	for i, t := range targets {
		labels[i] = fmt.Sprintf("%-16s  %s:%s", t.Name, t.SSHHost, t.RemotePath)
	}
	chosen, err := runSingleFuzzyPicker(title, labels, palette)
	if err != nil {
		return config.ConnectTarget{}, err
	}
	for i, lbl := range labels {
		if lbl == chosen {
			return validLinkTarget(targets[i])
		}
	}
	return config.ConnectTarget{}, fmt.Errorf("picker returned unknown label %q", chosen)
}

// validLinkTarget rejects a target that cannot back a binding.
func validLinkTarget(t config.ConnectTarget) (config.ConnectTarget, error) {
	if t.SSHHost == "" || t.RemotePath == "" {
		return t, fmt.Errorf("connect target %q has no ssh_host/remote_path", t.Name)
	}
	return t, nil
}

// connectTargetNames lists the registered target names, in config order.
func connectTargetNames(targets []config.ConnectTarget) []string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return names
}

// linkTargetName resolves the registered name of the project's current
// binding by matching host+path against the global targets; "" when the
// binding was written by hand and matches none.
func linkTargetName(cfg *config.Config) string {
	for _, t := range cfg.ConnectTargets {
		if t.SSHHost == cfg.ConnectSSHHost && t.RemotePath == cfg.ConnectRemotePath {
			return t.Name
		}
	}
	return ""
}
