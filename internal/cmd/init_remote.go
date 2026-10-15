package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// initRunSSH is the transport of every SSH call `init --remote` makes.
var initRunSSH = runSSH

const remoteEchoDir = "~/.config/echo"

var odooVersionRe = regexp.MustCompile(`Odoo Server (\d+)\.`)

type InitRemoteOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	Log     func(level, sub, msg, db string, fields ...[2]string)
}

type InitRemoteResult struct {
	Host        string
	Path        string
	Key         string
	ProfileFile string
	Updated     bool
	Stage       string
	Target      string
	Linked      bool
}

func (o InitRemoteOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

type initRemoteArgs struct {
	remote        bool
	from          string
	host          string
	path          string
	stage         string
	odooVersion   string
	odooContainer string
	dbContainer   string
	db            string
	name          string
	link          bool
	force         bool
}

func parseInitRemoteArgs(args []string) (initRemoteArgs, error) {
	var p initRemoteArgs
	values := map[string]*string{
		"--from":           &p.from,
		"--host":           &p.host,
		"--path":           &p.path,
		"--stage":          &p.stage,
		"--odoo-version":   &p.odooVersion,
		"--odoo-container": &p.odooContainer,
		"--db-container":   &p.dbContainer,
		"--db":             &p.db,
		"--name":           &p.name,
	}
	fromSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--remote":
			p.remote = true
			continue
		case "--link":
			p.link = true
			continue
		case "--force":
			p.force = true
			continue
		}
		flag, value, inline := strings.Cut(a, "=")
		dst, ok := values[flag]
		if !ok {
			if strings.HasPrefix(a, "-") {
				return p, fmt.Errorf("%w: init: unknown flag %s", ErrUsage, a)
			}
			return p, fmt.Errorf("%w: init: unexpected argument %q", ErrUsage, a)
		}
		if !inline {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return p, fmt.Errorf("%w: init: %s needs a value", ErrUsage, flag)
			}
			i++
			value = args[i]
		}
		if strings.TrimSpace(value) == "" {
			return p, fmt.Errorf("%w: init: %s needs a value", ErrUsage, flag)
		}
		*dst = value
		if flag == "--from" {
			fromSet = true
		}
	}
	switch {
	case p.remote && fromSet:
		return p, fmt.Errorf("%w: init: --remote and --from are exclusive", ErrUsage)
	case !p.remote && !fromSet:
		return p, fmt.Errorf("%w: init: remote flags need --remote or --from", ErrUsage)
	case !p.remote && (p.host != "" || p.path != ""):
		return p, fmt.Errorf("%w: init: --host and --path need --remote", ErrUsage)
	case fromSet && p.name != "":
		return p, fmt.Errorf("%w: init: --name does not apply to --from (the target already exists)", ErrUsage)
	}
	if p.stage != "" && !slices.Contains([]string{"dev", "staging", "prod"}, p.stage) {
		return p, fmt.Errorf("%w: init: --stage must be dev, staging or prod", ErrUsage)
	}
	if p.odooVersion != "" && !slices.Contains([]string{"17", "18", "19"}, p.odooVersion) {
		return p, fmt.Errorf("%w: init: --odoo-version must be 17, 18 or 19", ErrUsage)
	}
	if !stdinIsTTY() {
		missing := map[string]bool{"--stage": p.stage == "", "--host": p.remote && p.host == "", "--path": p.remote && p.path == ""}
		for _, flag := range []string{"--host", "--path", "--stage"} {
			if missing[flag] {
				return p, fmt.Errorf("%w: init: pass %s", ErrNonInteractive, flag)
			}
		}
	}
	return p, nil
}

// RunInitRemote writes the server-side Echo profile of a remote project over
// SSH, so the host needs no echo_cli of its own. It detects the stack, edits
// an existing profile in place (only the init fields), then registers the
// connect target and optionally binds the local directory to it.
func RunInitRemote(ctx context.Context, opts InitRemoteOpts) (InitRemoteResult, error) {
	var res InitRemoteResult
	p, err := parseInitRemoteArgs(opts.Args)
	if err != nil {
		return res, err
	}
	tty := stdinIsTTY()

	host, rawPath := p.host, p.path
	if p.from != "" {
		t, err := initFromTarget(p.from)
		if err != nil {
			return res, err
		}
		host, rawPath = t.SSHHost, t.RemotePath
		res.Target = t.Name
	} else {
		if host == "" {
			hosts := sshConfigHosts()
			if len(hosts) == 0 {
				return res, fmt.Errorf("no Host entries found in ~/.ssh/config")
			}
			if host, err = runSingleFuzzyPicker("Select SSH host (from ~/.ssh/config)", hosts, opts.Palette); err != nil {
				return res, err
			}
		}
		if rawPath == "" {
			if err := huh.NewForm(huh.NewGroup(
				huh.NewInput().
					Title("Remote project path").
					Description("Directory on " + host + " holding docker-compose.yml").
					Value(&rawPath),
			)).WithTheme(BuildHuhTheme(opts.Palette)).Run(); err != nil {
				return res, err
			}
			if rawPath = strings.TrimSpace(rawPath); rawPath == "" {
				return res, fmt.Errorf("%w: init: a remote path is required", ErrUsage)
			}
		}
	}
	res.Host = host

	opts.log("INFO", "", "probing", "", [2]string{"host", host}, [2]string{"path", rawPath})
	path, err := normalizeRemoteProject(ctx, host, rawPath)
	if err != nil {
		return res, err
	}
	res.Path = path
	res.Key = config.ProjectKey(path)
	projectFile := remoteEchoDir + "/projects/" + res.Key + ".toml"
	globalFile := remoteEchoDir + "/" + config.GlobalFileName
	res.ProfileFile = projectFile

	globalData, globalExists, err := readRemoteFile(ctx, host, globalFile)
	if err != nil {
		return res, err
	}
	projData, projExists, err := readRemoteFile(ctx, host, projectFile)
	if err != nil {
		return res, err
	}
	existing, err := config.DecodeRemoteProject(projData)
	if err != nil {
		return res, remoteProfileErr(err, host, projectFile)
	}
	res.Updated = projExists

	compose, writeGlobal, err := remoteComposeCommand(ctx, host, globalData, globalExists, globalFile)
	if err != nil {
		return res, err
	}

	services, err := remoteComposeServices(ctx, host, path, compose)
	if err != nil {
		return res, err
	}
	containers := remoteServiceContainers(ctx, host, path, compose, services)

	odooSvc := firstNonEmpty(p.odooContainer, existing.OdooContainer, matchService(containers, "odoo"), services[0])
	dbSvc := firstNonEmpty(p.dbContainer, existing.DBContainer, matchService(containers, "db", "postgres"), services[0])

	envOut, _ := initRunSSH(ctx, host, "cat "+shellQuote(path+"/.env"), nil)
	envVars := env.Parse(bytes.NewReader(envOut))
	pgUser := firstNonEmpty(envVars["POSTGRES_USER"], "postgres")
	var dbs []string
	if out, err := initRunSSH(ctx, host, remoteExec(path, compose, dbSvc, odoo.Cmd{"psql", "-U", pgUser, "-lqt"}), nil); err == nil {
		dbs = docker.ParseDatabases(string(out))
	}
	detectedVersion := ""
	if out, err := initRunSSH(ctx, host, remoteExec(path, compose, odooSvc, odoo.Cmd{"odoo", "--version"}), nil); err == nil {
		if m := odooVersionRe.FindStringSubmatch(string(out)); m != nil && slices.Contains([]string{"17", "18", "19"}, m[1]) {
			detectedVersion = m[1]
		}
	}
	opts.log("INFO", "detect", "detected", "",
		[2]string{"compose", compose},
		[2]string{"services", fmt.Sprint(len(services))},
		[2]string{"odoo", firstNonEmpty(detectedVersion, "unknown")},
		[2]string{"dbs", fmt.Sprint(len(dbs))})

	fields := config.RemoteInitFields{
		OdooVersion:   firstNonEmpty(p.odooVersion, existing.OdooVersion, detectedVersion, config.Defaults.OdooVersion),
		Stage:         firstNonEmpty(p.stage, existing.Stage, config.Defaults.Stage),
		OdooContainer: odooSvc,
		DBContainer:   dbSvc,
		DBName:        firstNonEmpty(p.db, existing.DBName, firstStr(dbs), envVars["POSTGRES_DB"], config.Defaults.DBName),
		ProjectPath:   path,
	}
	allFlagged := p.stage != "" && p.odooVersion != "" && p.odooContainer != "" && p.dbContainer != "" && p.db != ""
	if tty && !allFlagged {
		if err := runInitRemoteForm(opts.Palette, host, services, containers, dbs, &fields); err != nil {
			return res, err
		}
	}
	res.Stage = fields.Stage

	if projExists && !slices.Contains([]string{"dev", "staging"}, existing.Stage) && fields.Stage != "prod" && !p.force {
		if err := confirmStageDowngrade(opts.Palette, existing.Stage, fields.Stage, fields.DBName); err != nil {
			return res, err
		}
	}

	rendered, err := config.RenderRemoteProject(projData, fields)
	if err != nil {
		return res, remoteProfileErr(err, host, projectFile)
	}
	if writeGlobal {
		if err := writeRemoteFile(ctx, host, globalFile, config.RenderRemoteGlobalCompose(compose)); err != nil {
			return res, err
		}
		opts.log("INFO", "", "global written", "", [2]string{"file", globalFile}, [2]string{"compose", compose})
	}
	if err := writeRemoteFile(ctx, host, projectFile, rendered); err != nil {
		return res, err
	}
	written, _, err := readRemoteFile(ctx, host, projectFile)
	if err != nil {
		return res, err
	}
	if writeGlobal {
		globalData = config.RenderRemoteGlobalCompose(compose)
	}
	if _, err := config.ParseRemoteProfile(globalData, written); err != nil {
		return res, remoteProfileErr(err, host, projectFile)
	}
	msg := "profile written"
	if res.Updated {
		msg = "profile updated"
	}
	opts.log("INFO", "", msg, fields.DBName, [2]string{"file", projectFile}, [2]string{"stage", fields.Stage})

	if p.from != "" {
		return res, nil
	}
	target, err := registerInitTarget(opts, p, tty, host, path, fields.DBName)
	if err != nil || target.Name == "" {
		return res, err
	}
	res.Target = target.Name

	bind := p.link
	if tty && !bind {
		bind = true
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Link " + opts.Root + " to " + target.Name + "?").
				Affirmative("Link").
				Negative("Skip").
				Value(&bind),
		)).WithTheme(BuildHuhTheme(opts.Palette)).Run(); err != nil {
			return res, err
		}
	}
	if bind {
		linkOpts := LinkOpts{Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette, Log: opts.Log}
		if err := bindLinkTarget(ctx, linkOpts, target); err != nil {
			return res, err
		}
		res.Linked = true
	}
	return res, nil
}

func initFromTarget(name string) (config.ConnectTarget, error) {
	g, err := config.LoadGlobal()
	if err != nil {
		return config.ConnectTarget{}, err
	}
	for _, t := range g.ConnectTargets {
		if t.Name == name {
			return t, nil
		}
	}
	if len(g.ConnectTargets) == 0 {
		return config.ConnectTarget{}, ErrNoConnectTargets
	}
	return config.ConnectTarget{}, fmt.Errorf("unknown connect target: %s (available: %s)",
		name, strings.Join(connectTargetNames(g.ConnectTargets), ", "))
}

// remoteCdArg quotes a remote path for `cd` while leaving a leading `~`
// unquoted so the remote shell still expands it.
func remoteCdArg(path string) string {
	switch {
	case path == "~":
		return "~"
	case strings.HasPrefix(path, "~/"):
		return "~/" + shellQuote(strings.TrimPrefix(path, "~/"))
	}
	return shellQuote(path)
}

// normalizeRemoteProject resolves rawPath on the host to the logical
// absolute path (`pwd`, the form a server-side Echo hashes through $PWD) and
// requires a compose file there. The result is both the profile's
// project_path and the target's remote_path: they must be the same string.
func normalizeRemoteProject(ctx context.Context, host, rawPath string) (string, error) {
	script := "cd " + remoteCdArg(rawPath) + " && pwd && " +
		"if [ -f docker-compose.yml ] || [ -f docker-compose.yaml ]; then echo yes; else echo no; fi"
	out, err := initRunSSH(ctx, host, script, nil)
	if err != nil {
		return "", fmt.Errorf("cannot enter %s on %s: %w", rawPath, host, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "", fmt.Errorf("unexpected answer probing %s on %s: %q", rawPath, host, string(out))
	}
	path := strings.TrimSpace(lines[0])
	if strings.TrimSpace(lines[len(lines)-1]) != "yes" {
		return "", fmt.Errorf("no docker-compose.yml in %s on %s", path, host)
	}
	return path, nil
}

func readRemoteFile(ctx context.Context, host, file string) ([]byte, bool, error) {
	out, err := initRunSSH(ctx, host,
		"if [ -f "+file+" ]; then echo present; cat "+file+"; else echo absent; fi", nil)
	if err != nil {
		return nil, false, fmt.Errorf("read %s on %s: %w", file, host, err)
	}
	status, body, _ := strings.Cut(string(out), "\n")
	if strings.TrimSpace(status) != "present" {
		return nil, false, nil
	}
	return []byte(body), true, nil
}

func writeRemoteFile(ctx context.Context, host, file string, data []byte) error {
	dir := file[:strings.LastIndex(file, "/")]
	tmp := file + ".tmp.$$"
	script := "umask 077 && mkdir -p " + dir + " && cat > " + tmp + " && mv " + tmp + " " + file
	if _, err := initRunSSH(ctx, host, script, data); err != nil {
		return fmt.Errorf("write %s on %s: %w", file, host, err)
	}
	return nil
}

func remoteProfileErr(err error, host, file string) error {
	var perr *config.ParseError
	if errors.As(err, &perr) {
		return fmt.Errorf("server profile %s on %s does not parse: %s", file, host, perr.Detail())
	}
	return fmt.Errorf("%s on %s: %w", file, host, err)
}

// remoteComposeCommand picks the compose command for the probes. A declared
// compose_cmd wins; otherwise v2 is preferred. writeGlobal is true only when
// the host has v1 alone and no global.toml exists: an existing remote
// global.toml is never rewritten.
func remoteComposeCommand(ctx context.Context, host string, globalData []byte, globalExists bool, globalFile string) (string, bool, error) {
	declared, err := config.DeclaredComposeCmd(globalData)
	if err != nil {
		return "", false, remoteProfileErr(err, host, globalFile)
	}
	if declared != "" {
		return declared, false, nil
	}
	out, err := initRunSSH(ctx, host,
		"if docker compose version >/dev/null 2>&1; then echo v2; "+
			"elif docker-compose version >/dev/null 2>&1; then echo v1; else echo none; fi", nil)
	if err != nil {
		return "", false, fmt.Errorf("probe docker compose on %s: %w", host, err)
	}
	switch strings.TrimSpace(string(out)) {
	case "v2":
		return "docker compose", false, nil
	case "v1":
		if globalExists {
			return "", false, fmt.Errorf("%s on %s has no compose_cmd and the host only has docker-compose v1: add compose_cmd = \"docker-compose\" there", globalFile, host)
		}
		return "docker-compose", true, nil
	}
	return "", false, fmt.Errorf("docker compose not found on %s", host)
}

// remoteComposeServices lists services from `compose config`, which works
// with the stack stopped, so init never has to start anything.
func remoteComposeServices(ctx context.Context, host, path, compose string) ([]string, error) {
	out, err := initRunSSH(ctx, host, "cd "+shellQuote(path)+" && "+compose+" config --services", nil)
	if err != nil {
		return nil, fmt.Errorf("compose config on %s: %w", host, err)
	}
	var services []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			services = append(services, s)
		}
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("compose on %s lists no services in %s", host, path)
	}
	return services, nil
}

// remoteServiceContainers pairs each service with its running container
// name; a stopped service keeps an empty Name.
func remoteServiceContainers(ctx context.Context, host, path, compose string, services []string) []docker.Container {
	running := map[string]string{}
	out, err := initRunSSH(ctx, host,
		"cd "+shellQuote(path)+" && "+compose+" ps --status=running --format "+shellQuote("{{.Service}}\t{{.Name}}"), nil)
	if err == nil {
		for _, c := range docker.ParseContainers(string(out)) {
			running[c.Service] = c.Name
		}
	}
	containers := make([]docker.Container, len(services))
	for i, s := range services {
		containers[i] = docker.Container{Service: s, Name: running[s]}
	}
	return containers
}

func serviceOptions(containers []docker.Container) []huh.Option[string] {
	out := make([]huh.Option[string], len(containers))
	for i, c := range containers {
		label := c.Service + " (stopped)"
		if c.Name != "" {
			label = c.Label()
		}
		out[i] = huh.NewOption(label, c.Service)
	}
	return out
}

func runInitRemoteForm(palette theme.Palette, host string, services []string, containers []docker.Container, dbs []string, f *config.RemoteInitFields) error {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Odoo version").
				Description("Major release of Odoo running on "+host).
				Options(opt("17"), opt("18"), opt("19")).
				Value(&f.OdooVersion),
			huh.NewSelect[string]().
				Title("Stage").
				Description("Every remote gate reads this value").
				Options(opt("dev"), opt("staging"), opt("prod")).
				Value(&f.Stage),
		).
			Title(IconOdoo+"  Odoo").
			Description("Version and environment of the remote project"),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Odoo container").
				Description("compose service running the Odoo process").
				Options(serviceOptions(containers)...).
				Value(&f.OdooContainer),
			huh.NewSelect[string]().
				Title("DB container").
				Description("compose service running PostgreSQL").
				Options(serviceOptions(containers)...).
				Value(&f.DBContainer),
		).
			Title(IconContainers+"  Containers").
			Description(fmt.Sprintf("%d compose services on %s", len(services), host)),
		huh.NewGroup(
			dbField(dbs, &f.DBName),
		).
			Title(IconDatabase+"  Database").
			Description("PostgreSQL database used by Odoo"),
	).
		WithTheme(huh.ThemeCharm()).
		WithInput(os.Stdin).
		WithOutput(os.Stdout).
		WithShowHelp(true).
		Run()
}

// confirmStageDowngrade guards rewriting a prod (or undeclared, which reads
// as prod) profile to a lower stage: that silently disables every remote
// prod gate for the target.
func confirmStageDowngrade(palette theme.Palette, from, to, db string) error {
	if err := requireTTY("pass --force to change the stage of a prod profile"); err != nil {
		return err
	}
	if from == "" {
		from = "undeclared"
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	confirmed := false
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Changing stage " + from + " → " + to + " on prod database " + red).
			Description("Remote prod confirmations stop applying to this target.").
			Affirmative("Change").
			Negative("Cancel").
			Value(&confirmed),
	)).WithTheme(BuildHuhTheme(palette)).WithInput(os.Stdin).WithOutput(os.Stdout).Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}

// registerInitTarget saves the connect target for the new profile. Headless
// runs register only with --name; a name already pointing elsewhere is a
// usage error, the same host+path is reused as is.
func registerInitTarget(opts InitRemoteOpts, p initRemoteArgs, tty bool, host, path, dbName string) (config.ConnectTarget, error) {
	name := p.name
	if name == "" && !tty {
		opts.log("INFO", "", "profile written; no target registered", dbName,
			[2]string{"hint", "pass --name to register it"})
		return config.ConnectTarget{}, nil
	}
	if name == "" {
		name = defaultTargetName(config.ProjectInfo{DBName: dbName, ProjectPath: path})
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("Target name").
				Description("How you'll refer to it: echo_cli link <name>").
				Value(&name),
		)).WithTheme(BuildHuhTheme(opts.Palette)).Run(); err != nil {
			return config.ConnectTarget{}, err
		}
		if name = strings.TrimSpace(name); name == "" {
			return config.ConnectTarget{}, fmt.Errorf("%w: init: target name is required", ErrUsage)
		}
	}
	g, err := config.LoadGlobal()
	if err != nil {
		return config.ConnectTarget{}, err
	}
	for _, t := range g.ConnectTargets {
		if t.Name != name {
			continue
		}
		if t.SSHHost == host && t.RemotePath == path {
			return t, nil
		}
		return config.ConnectTarget{}, fmt.Errorf("%w: target %s already points at %s:%s", ErrUsage, name, t.SSHHost, t.RemotePath)
	}
	target := config.ConnectTarget{Name: name, SSHHost: host, RemotePath: path, DBName: dbName}
	if err := config.SaveConnectTarget(target); err != nil {
		return config.ConnectTarget{}, fmt.Errorf("save target: %w", err)
	}
	opts.log("INFO", "", "registered target", dbName,
		[2]string{"target", name}, [2]string{"host", host}, [2]string{"path", path})
	return target, nil
}
