package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// ErrNoPullRemote is returned when i18n-pull has no remote to pull from:
// no project [connect], no named connect targets, and no --from given.
var ErrNoPullRemote = errors.New(
	"no remote configured — add a connect target (`echo connect --add`), set this project's [connect], or pass --from <target>")

// I18nPullOpts configures an `i18n-pull` run.
type I18nPullOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	StreamOut func(string)
}

// i18nPullArgs is the parsed shape of the i18n-pull input.
type i18nPullArgs struct {
	module string
	lang   string
	from   string // connect target name; "" → project's own [connect]
	all    bool
}

// parseI18nPullArgs extracts --from/--all and up to two positionals. With
// --all the single optional positional is the language; otherwise the
// positionals are module then language.
func parseI18nPullArgs(args []string) (i18nPullArgs, error) {
	out := i18nPullArgs{lang: defaultI18nLang}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--from requires a target name")
			}
			out.from = args[i+1]
			i++
		case strings.HasPrefix(a, "--from="):
			out.from = strings.TrimPrefix(a, "--from=")
		case a == "--all":
			out.all = true
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("unknown flag: %s", a)
		default:
			positional = append(positional, a)
		}
	}
	if out.all {
		if len(positional) > 1 {
			return out, fmt.Errorf("--all takes only an optional language")
		}
		if len(positional) == 1 {
			out.lang = positional[0]
		}
		return out, nil
	}
	if len(positional) > 2 {
		return out, fmt.Errorf("too many arguments (module then language)")
	}
	if len(positional) > 0 {
		out.module = positional[0]
	}
	if len(positional) > 1 {
		out.lang = positional[1]
	}
	return out, nil
}

// resolvePullRemote returns the ssh host + remote project path to pull
// from: a named connect target when --from is set, otherwise the project's
// own [connect] config. Errors when neither yields a usable remote.
func resolvePullRemote(cfg *config.Config, from string) (sshHost, remotePath string, err error) {
	if from != "" {
		for _, t := range cfg.ConnectTargets {
			if t.Name == from {
				if t.SSHHost == "" || t.RemotePath == "" {
					return "", "", fmt.Errorf("connect target %q has no ssh_host/remote_path", from)
				}
				return t.SSHHost, t.RemotePath, nil
			}
		}
		return "", "", fmt.Errorf("unknown connect target: %s", from)
	}
	if cfg.ConnectSSHHost == "" || cfg.ConnectRemotePath == "" {
		return "", "", ErrNoPullRemote
	}
	return cfg.ConnectSSHHost, cfg.ConnectRemotePath, nil
}

// pickPullTarget resolves a connect target name from the global registry
// when there's no explicit --from and no project [connect]: a single
// target is used automatically (with an info line), several open a picker,
// none yields ErrNoPullRemote. The picker is TTY-guarded, so a headless
// run with several targets fails closed asking for --from.
func pickPullTarget(cfg *config.Config, palette theme.Palette, stream func(string)) (string, error) {
	targets := cfg.ConnectTargets
	switch len(targets) {
	case 0:
		return "", ErrNoPullRemote
	case 1:
		if stream != nil {
			stream("using connect target " + targets[0].Name)
		}
		return targets[0].Name, nil
	}
	labels := make([]string, len(targets))
	for i, t := range targets {
		labels[i] = fmt.Sprintf("%-16s  %s:%s", t.Name, t.SSHHost, t.RemotePath)
	}
	chosen, err := runSingleFuzzyPicker("Select connect target to pull from", labels, palette)
	if err != nil {
		return "", err
	}
	for i, lbl := range labels {
		if lbl == chosen {
			return targets[i].Name, nil
		}
	}
	return "", fmt.Errorf("picker returned unknown label %q", chosen)
}

// RunI18nPull exports a module's translations from a remote Odoo instance
// (reached over SSH via the project's connect config or a --from target)
// and writes the resulting .po into the local repo at
// <addons>/<mod>/i18n/<lang>.po. With --all it pulls every module present
// in the local repo. The remote DB is never modified — this is a read.
func RunI18nPull(ctx context.Context, opts I18nPullOpts) error {
	p, err := parseI18nPullArgs(opts.Args)
	if err != nil {
		return err
	}

	sshHost, remotePath, err := resolvePullRemote(opts.Cfg, p.from)
	// No --from and no project [connect]: fall back to the named connect
	// targets from global.toml (one → auto, several → picker).
	if errors.Is(err, ErrNoPullRemote) && p.from == "" {
		name, perr := pickPullTarget(opts.Cfg, opts.Palette, opts.StreamOut)
		if perr != nil {
			return perr
		}
		sshHost, remotePath, err = resolvePullRemote(opts.Cfg, name)
	}
	if err != nil {
		return err
	}

	// Resolve the remote container/db mapping from the server's own Echo
	// profile, then its Postgres credentials from the remote .env.
	cfgRemote := *opts.Cfg
	cfgRemote.ConnectSSHHost = sshHost
	cfgRemote.ConnectRemotePath = remotePath
	target, err := resolveConnectTarget(ctx, ConnectOpts{Cfg: &cfgRemote, Root: opts.Root})
	if err != nil {
		return err
	}
	conn := odoo.Conn{
		DB:       target.dbName,
		Host:     target.dbContainer,
		Port:     "",
		User:     "",
		Password: "",
	}
	pg := remotePullEnv(ctx, sshHost, remotePath)
	conn.Port = pg["POSTGRES_PORT"]
	conn.User = pg["POSTGRES_USER"]
	conn.Password = pg["POSTGRES_PASSWORD"]

	// Decide which modules to pull.
	var modules []string
	switch {
	case p.all:
		modules = listAvailableModules(opts.Cfg, opts.Root)
		if len(modules) == 0 {
			return ErrNoModulesAvailable
		}
	case p.module != "":
		modules = []string{p.module}
	default:
		picked, err := pickModuleSingle(opts.Cfg, opts.Root, opts.Palette, "Module to pull translations for")
		if err != nil {
			return err
		}
		modules = []string{picked}
	}

	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("pulling %s from %s:%s", p.lang, sshHost, remotePath))
	}

	var pulled, skipped int
	for _, mod := range modules {
		addonsDir, err := resolveModuleDir(opts.Cfg, opts.Root, mod)
		if err != nil {
			if p.all { // skip a local module we can't place, keep going
				if opts.StreamOut != nil {
					opts.StreamOut("skip " + mod + ": " + err.Error())
				}
				skipped++
				continue
			}
			return err
		}
		data, err := pullRemotePO(ctx, sshHost, remotePath, target, conn, mod, p.lang)
		if err != nil {
			if p.all {
				if opts.StreamOut != nil {
					opts.StreamOut("skip " + mod + ": " + err.Error())
				}
				skipped++
				continue
			}
			return err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			if opts.StreamOut != nil {
				opts.StreamOut("skip " + mod + ": empty .po returned")
			}
			skipped++
			continue
		}
		dest := defaultExportDest(addonsDir, mod, p.lang)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create i18n dir: %w", err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		pulled++
		if opts.StreamOut != nil {
			opts.StreamOut("→ " + dest)
		}
	}

	if opts.StreamOut != nil && (p.all || skipped > 0) {
		opts.StreamOut(fmt.Sprintf("pulled %d module(s), skipped %d", pulled, skipped))
	}
	return nil
}

// remotePullEnv reads the remote project's .env over SSH and parses it.
// A read failure yields an empty map (Odoo may still connect via its own
// container config), never an error.
func remotePullEnv(ctx context.Context, sshHost, remotePath string) map[string]string {
	out, err := runSSH(ctx, sshHost, "cat "+shellQuote(remotePath+"/.env"), nil)
	if err != nil {
		return map[string]string{}
	}
	return env.Parse(bytes.NewReader(out))
}

// pullRemotePO runs `odoo --i18n-export` for one module inside the remote
// Odoo container, reads the produced .po back over SSH, and cleans up the
// temp file. Returns the raw .po bytes.
func pullRemotePO(ctx context.Context, sshHost, remotePath string, t connectTarget, conn odoo.Conn, module, lang string) ([]byte, error) {
	tmp := tmpPathInContainer()
	exportArgv := odoo.ExportI18n(conn, module, lang, tmp)
	if _, err := runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, exportArgv), nil); err != nil {
		return nil, fmt.Errorf("remote export: %w", err)
	}
	data, readErr := runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, odoo.Cmd{"cat", tmp}), nil)
	// Best-effort cleanup regardless of the read outcome.
	_, _ = runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, odoo.Cmd{"rm", "-f", tmp}), nil)
	if readErr != nil {
		return nil, fmt.Errorf("remote read: %w", readErr)
	}
	return data, nil
}

// remoteContainerCmd builds the SSH command that runs argv inside the
// remote Odoo container: `cd <path> && <compose> exec -T <odoo> <argv...>`.
// Every argv token is shell-quoted; the compose command is emitted raw so
// a two-word "docker compose" splits into its two tokens.
func remoteContainerCmd(remotePath string, t connectTarget, argv odoo.Cmd) string {
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(shellQuote(remotePath))
	b.WriteString(" && ")
	b.WriteString(t.composeCmd)
	b.WriteString(" exec -T ")
	b.WriteString(shellQuote(t.odooContainer))
	for _, a := range argv {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	return b.String()
}
