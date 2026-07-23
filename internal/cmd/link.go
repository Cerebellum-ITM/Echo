package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	target  string
	show    bool
	rm      bool
	next    bool
	list    bool
	jsonOut bool
}

// parseLinkArgs extracts the mode flags and the optional target positional.
// At most one mode may be named: --show, --rm, --next, --list or a target
// (bare `link` is the switcher). --json only decorates --list.
func parseLinkArgs(args []string) (linkArgs, error) {
	var out linkArgs
	for _, a := range args {
		switch {
		case a == "--show":
			out.show = true
		case a == "--rm":
			out.rm = true
		case a == "--next":
			out.next = true
		case a == "--list":
			out.list = true
		case a == "--json":
			out.jsonOut = true
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			if out.target != "" {
				return out, fmt.Errorf("%w: link takes a single target name", ErrUsage)
			}
			out.target = a
		}
	}
	modes := 0
	for _, on := range []bool{out.show, out.rm, out.next, out.list, out.target != ""} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return out, fmt.Errorf("%w: --show/--rm/--next/--list and a target name are mutually exclusive", ErrUsage)
	}
	if out.jsonOut && !out.list {
		return out, fmt.Errorf("%w: --json only applies to --list", ErrUsage)
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
	case p.list:
		return runLinkList(opts, p)
	case p.next:
		return runLinkNext(ctx, opts)
	}
	return runLinkBind(ctx, opts, p.target)
}

// linkTargetRow is the machine-readable shape of `link --list --json`.
type linkTargetRow struct {
	Name       string `json:"name"`
	SSHHost    string `json:"ssh_host"`
	RemotePath string `json:"remote_path"`
	DBName     string `json:"db_name,omitempty"`
	Current    bool   `json:"current"`
}

// runLinkList inventories the registered targets, marking the current
// binding. Deliberately offline: no SSH and no write, so it stays instant —
// `--show` is the one that probes.
func runLinkList(opts LinkOpts, p linkArgs) error {
	targets := opts.Cfg.ConnectTargets
	current := linkTargetName(opts.Cfg)
	rows := make([]linkTargetRow, 0, len(targets))
	for _, t := range targets {
		rows = append(rows, linkTargetRow{
			Name: t.Name, SSHHost: t.SSHHost, RemotePath: t.RemotePath,
			DBName: t.DBName, Current: t.Name == current && current != "",
		})
	}
	if p.jsonOut {
		b, err := json.Marshal(rows)
		if err != nil {
			return fmt.Errorf("encode targets: %w", err)
		}
		os.Stdout.Write(b)
		os.Stdout.Write([]byte("\n"))
		return nil
	}
	if len(rows) == 0 {
		return ErrNoConnectTargets
	}
	for _, r := range rows {
		mark := " "
		if r.Current {
			mark = "●"
		}
		fields := [][2]string{{"host", r.SSHHost}, {"path", r.RemotePath}}
		if r.DBName != "" {
			fields = append([][2]string{{"db", r.DBName}}, fields...)
		}
		opts.log("INFO", "target", mark+" "+r.Name, "", fields...)
	}
	return nil
}

// nextTargetIndex is the cycle step: the index after current (wrapping), or 0
// when the binding matches no registered target (unlinked / hand-written).
// Fewer than two targets is a usage error — there is nothing to cycle to.
func nextTargetIndex(targets []config.ConnectTarget, current string) (int, error) {
	if len(targets) < 2 {
		return 0, fmt.Errorf("%w: --next needs at least two connect targets (have %d)", ErrUsage, len(targets))
	}
	for i, t := range targets {
		if t.Name == current && current != "" {
			return (i + 1) % len(targets), nil
		}
	}
	return 0, nil
}

// runLinkNext rebinds to the next target in the registry, wrapping — the
// no-picker toggle for a two-environment setup.
func runLinkNext(ctx context.Context, opts LinkOpts) error {
	idx, err := nextTargetIndex(opts.Cfg.ConnectTargets, linkTargetName(opts.Cfg))
	if err != nil {
		return err
	}
	return runLinkBind(ctx, opts, opts.Cfg.ConnectTargets[idx].Name)
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
	// Switching to where you already are costs nothing: no rewrite, no probe.
	if t.SSHHost == opts.Cfg.ConnectSSHHost && t.RemotePath == opts.Cfg.ConnectRemotePath {
		opts.log("INFO", "", "already linked", "", [2]string{"target", t.Name})
		return nil
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
	return pickConnectTarget(targets, opts.Palette, "Switch connect target", linkTargetName(opts.Cfg), opts.Log)
}

// pickConnectTarget resolves a target when no name was given: none yields
// ErrNoConnectTargets, a single one is auto-used (with an info line),
// several open a TTY-guarded picker. Shared by `link` and `deploy`.
//
// current names the target the picker should mark and open on ("" when the
// caller has no notion of a current one, e.g. deploy's target prompt).
func pickConnectTarget(targets []config.ConnectTarget, palette theme.Palette, title, current string, log func(level, sub, msg, db string, fields ...[2]string)) (config.ConnectTarget, error) {
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
	start := 0
	for i, t := range targets {
		mark := " "
		if t.Name == current && current != "" {
			mark, start = "●", i
		}
		db := t.DBName
		if db == "" {
			db = "-"
		}
		labels[i] = fmt.Sprintf("%s %-16s  %-20s  %s:%s", mark, t.Name, db, t.SSHHost, t.RemotePath)
	}
	chosen, err := runSingleFuzzyPickerAt(title, labels, palette, "", start)
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
