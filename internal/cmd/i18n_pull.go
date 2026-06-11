package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log emits one Odoo-style progress line (rendered by the REPL through
	// emitOdooLog under `echo.i18n-pull[.sub]`), mirroring connect's logger.
	// `db` overrides the line's database segment; nil is a no-op.
	Log func(level, sub, msg, db string, fields ...[2]string)
}

// log emits a progress line when a logger is set; a no-op otherwise.
func (o I18nPullOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// i18nPullArgs is the parsed shape of the i18n-pull input.
type i18nPullArgs struct {
	module    string
	lang      string
	from      string // connect target name; "" → project's own [connect]
	all       bool
	installed bool // list candidates from the DB (all installed) vs conf addons
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
		case a == "--installed":
			out.installed = true
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
func pickPullTarget(opts I18nPullOpts) (string, error) {
	targets := opts.Cfg.ConnectTargets
	switch len(targets) {
	case 0:
		return "", ErrNoPullRemote
	case 1:
		opts.log("INFO", "remote", "using connect target", "", [2]string{"target", targets[0].Name})
		return targets[0].Name, nil
	}
	labels := make([]string, len(targets))
	for i, t := range targets {
		labels[i] = fmt.Sprintf("%-16s  %s:%s", t.Name, t.SSHHost, t.RemotePath)
	}
	chosen, err := runSingleFuzzyPicker("Select connect target to pull from", labels, opts.Palette)
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
		opts.log("INFO", "", "selecting remote target", "")
		name, perr := pickPullTarget(opts)
		if perr != nil {
			return perr
		}
		sshHost, remotePath, err = resolvePullRemote(opts.Cfg, name)
	}
	if err != nil {
		return err
	}
	opts.log("INFO", "remote", "target resolved", "",
		[2]string{"host", sshHost}, [2]string{"path", remotePath})

	// Resolve the remote container/db mapping AND its addons config from the
	// server's own Echo profile, then its Postgres credentials from the
	// remote .env.
	cfgRemote := *opts.Cfg
	cfgRemote.ConnectSSHHost = sshHost
	cfgRemote.ConnectRemotePath = remotePath
	opts.log("INFO", "remote", "reading remote profile", "", [2]string{"host", sshHost})
	prof, err := fetchRemoteProfile(ctx, ConnectOpts{Cfg: &cfgRemote, Root: opts.Root})
	if err != nil {
		return err
	}
	target := connectTarget{
		remote:        true,
		composeCmd:    prof.ComposeCmd,
		odooContainer: prof.OdooContainer,
		dbContainer:   prof.DBContainer,
		dbName:        prof.DBName,
		stage:         prof.Stage,
		odooVersion:   prof.OdooVersion,
	}
	// The system-status line doubles as the "connected" signal: it is the
	// first line carrying the resolved remote environment (Echo + Odoo
	// version, project, db), emitted the moment the remote profile is read —
	// the earliest point the remote Odoo version is known.
	opts.log("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage,
			statusProjectName(opts.Cfg, true, remotePath, p.from),
			prof.DBName)...)
	conn := odoo.Conn{DB: target.dbName, Host: target.dbContainer}
	pg := remotePullEnv(ctx, sshHost, remotePath)
	conn.Port = pg["POSTGRES_PORT"]
	conn.User = pg["POSTGRES_USER"]
	conn.Password = pg["POSTGRES_PASSWORD"]

	// Module candidates come from the REMOTE instance. By default they are
	// the modules in the remote project's own addons (read from its
	// odoo.conf, or the addons paths stored in its Echo profile) — i.e. the
	// modules the developer maintains, not every stock Odoo module. With
	// --installed the candidates are instead every installed module
	// (`ir_module_module`), kept as an escape hatch.
	listRemote := func() ([]string, error) {
		src := "project addons"
		if p.installed {
			src = "installed (db)"
		}
		opts.log("INFO", "remote", "listing modules", prof.DBName, [2]string{"source", src})
		var (
			mods []string
			err  error
		)
		if p.installed {
			mods, err = listRemoteModules(ctx, sshHost, remotePath, target, conn.User, target.dbName)
		} else {
			mods, err = listRemoteConfModules(ctx, sshHost, remotePath, target, prof.ConfPath, prof.AddonsPaths)
		}
		if err == nil {
			opts.log("INFO", "remote", fmt.Sprintf("%d module(s) found", len(mods)), prof.DBName)
		}
		return mods, err
	}

	var modules []string
	switch {
	case p.all:
		avail, err := listRemote()
		if err != nil {
			return fmt.Errorf("list remote modules: %w", err)
		}
		if len(avail) == 0 {
			return ErrNoModulesAvailable
		}
		modules = avail
	case p.module != "":
		modules = []string{p.module}
	default:
		avail, err := listRemote()
		if err != nil {
			return fmt.Errorf("list remote modules: %w", err)
		}
		if len(avail) == 0 {
			return ErrNoModulesAvailable
		}
		picked, err := runSingleFuzzyPickerStaged("Module to pull translations for", avail, opts.Palette, prof.Stage)
		if err != nil {
			return err
		}
		modules = []string{picked}
	}

	var pulled, skipped int
	for _, mod := range modules {
		opts.log("INFO", "", "exporting translations", prof.DBName,
			[2]string{"module", mod}, [2]string{"lang", p.lang})
		data, err := pullRemotePO(ctx, sshHost, remotePath, target, conn, mod, p.lang)
		if err != nil {
			if p.all {
				opts.log("WARNING", "", "skipped", prof.DBName,
					[2]string{"module", mod}, [2]string{"reason", err.Error()})
				skipped++
				continue
			}
			return err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			opts.log("WARNING", "", "skipped", prof.DBName,
				[2]string{"module", mod}, [2]string{"reason", "no translations for " + p.lang})
			skipped++
			continue
		}
		dest := pullDest(opts.Cfg, opts.Root, mod, p.lang)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create i18n dir: %w", err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		pulled++
		opts.log("INFO", "", "pulled", prof.DBName,
			[2]string{"module", mod}, [2]string{"file", dest})
	}

	if p.all || skipped > 0 {
		opts.log("INFO", "", "pull complete", prof.DBName,
			[2]string{"pulled", strconv.Itoa(pulled)}, [2]string{"skipped", strconv.Itoa(skipped)})
	}
	return nil
}

// pullDest is where a pulled .po is written locally. When the module is on
// the host (host-mode dev), it lands in its real addons dir
// (<addons>/<mod>/i18n/<lang>.po) — preserving the existing flow. When it
// isn't (conf-mode / staging whose addons live only in the container), it
// falls back to a cwd-relative path so the file can still be pulled and
// committed.
func pullDest(cfg *config.Config, root, mod, lang string) string {
	if addonsDir, err := resolveModuleDir(cfg, root, mod); err == nil {
		return defaultExportDest(addonsDir, mod, lang)
	}
	return filepath.Join(root, mod, "i18n", lang+".po")
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

// pullRemotePO exports one module's translations inside the remote Odoo
// container, reads the produced .po back over SSH, and cleans up the temp
// file(s). Returns the raw .po bytes.
//
// On Odoo 19 (t.odooVersion major ≥ 19) the export runs through the
// `odoo i18n export` subcommand, which only takes `-c`/`-d`; the db
// credentials are written into an ephemeral in-container odoo.conf
// (RenderConf) passed with `-c` and removed alongside the .po.
func pullRemotePO(ctx context.Context, sshHost, remotePath string, t connectTarget, conn odoo.Conn, module, lang string) ([]byte, error) {
	tmp := tmpPathInContainer()
	cleanup := odoo.Cmd{"rm", "-f", tmp}

	confPath := ""
	if odoo.Major(t.odooVersion) >= 19 {
		confPath = tmpConfInContainer()
		writeArgv := odoo.Cmd{"sh", "-c", "cat > " + confPath}
		if _, err := runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, writeArgv), odoo.RenderConf(conn)); err != nil {
			return nil, fmt.Errorf("remote conf write: %w", err)
		}
		cleanup = append(cleanup, confPath)
	}

	exportArgv := odoo.ExportI18n(conn, t.odooVersion, module, lang, tmp, confPath)
	if _, err := runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, exportArgv), nil); err != nil {
		_, _ = runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, cleanup), nil)
		return nil, fmt.Errorf("remote export: %w", err)
	}
	data, readErr := runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, odoo.Cmd{"cat", tmp}), nil)
	// Best-effort cleanup regardless of the read outcome.
	_, _ = runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, cleanup), nil)
	if readErr != nil {
		return nil, fmt.Errorf("remote read: %w", readErr)
	}
	return data, nil
}

// remoteExec builds the SSH command that runs argv inside a remote compose
// service: `cd <path> && <compose> exec -T <container> <argv...>`. Every
// argv token is shell-quoted; the compose command is emitted raw so a
// two-word "docker compose" splits into its two tokens.
func remoteExec(remotePath, composeCmd, container string, argv odoo.Cmd) string {
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(shellQuote(remotePath))
	b.WriteString(" && ")
	b.WriteString(composeCmd)
	b.WriteString(" exec -T ")
	b.WriteString(shellQuote(container))
	for _, a := range argv {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	return b.String()
}

// remoteContainerCmd runs argv in the remote Odoo container.
func remoteContainerCmd(remotePath string, t connectTarget, argv odoo.Cmd) string {
	return remoteExec(remotePath, t.composeCmd, t.odooContainer, argv)
}

// remoteDBCmd runs argv in the remote Postgres container.
func remoteDBCmd(remotePath string, t connectTarget, argv odoo.Cmd) string {
	return remoteExec(remotePath, t.composeCmd, t.dbContainer, argv)
}

// listRemoteConfModules lists the remote project's own modules: the
// directories with a __manifest__.py under its addons paths. The paths come
// from the addons paths stored in the remote Echo profile when present,
// otherwise from parsing the remote odoo.conf. This is the default source —
// it yields the modules the developer maintains, not every stock module.
func listRemoteConfModules(ctx context.Context, sshHost, remotePath string, t connectTarget, confPath string, storedPaths []string) ([]string, error) {
	paths := storedPaths
	if len(paths) == 0 {
		if confPath == "" {
			confPath = "/etc/odoo/odoo.conf"
		}
		conf, err := runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, odoo.Cmd{"cat", confPath}), nil)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w (try --installed to list from the database)", confPath, err)
		}
		paths = parseAddonsPath(string(conf))
		if len(paths) == 0 {
			return nil, fmt.Errorf("no addons_path in remote %s (try --installed)", confPath)
		}
	}
	// Same one-deep manifest scan as the local conf-mode listing, run in the
	// remote Odoo container.
	const script = `for d in "$@"; do for m in "$d"/*/__manifest__.py; do [ -f "$m" ] && basename "$(dirname "$m")"; done; done`
	argv := append(odoo.Cmd{"sh", "-c", script, "_"}, paths...)
	out, err := runSSH(ctx, sshHost, remoteContainerCmd(remotePath, t, argv), nil)
	if err != nil {
		return nil, err
	}
	return dedupeSortedLines(string(out)), nil
}

// dedupeSortedLines splits output into trimmed non-empty lines, removing
// duplicates and sorting the result.
func dedupeSortedLines(out string) []string {
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// listRemoteModules queries the remote database for the names of installed
// modules — the set whose translations can be pulled. Run over SSH inside
// the remote Postgres container.
func listRemoteModules(ctx context.Context, sshHost, remotePath string, t connectTarget, pgUser, db string) ([]string, error) {
	if pgUser == "" {
		pgUser = "odoo"
	}
	q := "SELECT name FROM ir_module_module WHERE state = 'installed' ORDER BY name"
	argv := odoo.Cmd{"psql", "-U", pgUser, "-d", db, "-At", "-c", q}
	out, err := runSSH(ctx, sshHost, remoteDBCmd(remotePath, t, argv), nil)
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			mods = append(mods, line)
		}
	}
	return mods, nil
}
