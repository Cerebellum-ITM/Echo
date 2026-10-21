package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/relay"
)

// relayEnv is the Relay env behind a remoteShellContext; nil on every other
// target.
type relayEnv struct {
	app, env string
	client   *relay.Client
	resolved relay.Env
}

func (r *relayEnv) spec() string { return r.app + "/" + r.env }

// relayDeployPoll is how often a delegated deploy is read back while it runs.
var relayDeployPoll = 2 * time.Second

// relaySupported are the commands that work on a Relay target. deploy is
// delegated to Relay; the rest run in the env's containers.
var relaySupported = map[string]bool{
	"shell": true, "shell-run": true, "logs": true, "ps": true, "test": true, "update": true,
	"i18n-pull": true, "i18n-update": true, "deploy": true, "link": true,
}

// relayAlwaysRemote are the unsupported commands that reach the bound target
// even without a remote flag.
var relayAlwaysRemote = map[string]bool{
	"push": true, "watch": true, "sideload": true, "checkpoint": true, "actions": true,
	"db-pull": true, "db-push": true, "doctor": true,
}

const relayBakedAddons = "Relay builds the env's image from git with the addons baked in; " +
	"push your commits to the env's branch (Relay deploys every push) or run `deploy` to deploy now"

var relayRefusals = map[string]string{
	"push":       relayBakedAddons,
	"watch":      relayBakedAddons,
	"sideload":   "use `relay sideload add <app>/<env>`, which pins the module into every deploy",
	"checkpoint": "Relay snapshots the env before every deploy and restores it on failure",
	"up":         "relayd owns the env's containers",
	"stop":       "relayd owns the env's containers",
	"restart":    "relayd owns the env's containers",
	"down":       "relayd owns the env's containers",
}

// RelayGuard refuses, from the config alone and before any SSH, a command
// that cannot run on the Relay env it would reach: the --from target, or the
// directory's binding for a command that runs remotely.
func RelayGuard(cfg *config.Config, name string, args []string) error {
	if relaySupported[name] {
		return nil
	}
	from, remote := remoteFlagsIn(args)
	if from == "" && !remote && !relayAlwaysRemote[name] {
		return nil
	}
	_, path, _, ok := relayTargetFor(cfg, from)
	if !ok {
		return nil
	}
	spec, _ := config.RelaySpec(path)
	why := relayRefusals[name]
	if why == "" {
		why = "it works through a project directory on the host, and a Relay env has none"
	}
	return fmt.Errorf("%w: %s does not run on the Relay env %s — %s", ErrUsage, name, spec, why)
}

// relayTargetFor returns the target a remote run would use when it is a
// Relay env: the named one, else the directory's binding. It never prompts.
func relayTargetFor(cfg *config.Config, from string) (host, path, name string, ok bool) {
	if from == "" {
		_, ok = config.RelaySpec(cfg.ConnectRemotePath)
		return cfg.ConnectSSHHost, cfg.ConnectRemotePath, linkTargetName(cfg), ok
	}
	for _, t := range cfg.ConnectTargets {
		if t.Name == from {
			_, ok = config.RelaySpec(t.RemotePath)
			return t.SSHHost, t.RemotePath, t.Name, ok
		}
	}
	return "", "", "", false
}

func parseRelaySpec(spec string) (app, env string, err error) {
	app, env, found := strings.Cut(strings.TrimSpace(spec), "/")
	if !found || app == "" || env == "" || strings.Contains(env, "/") {
		return "", "", fmt.Errorf("%w: a Relay env is <app>/<env>, got %q", ErrUsage, spec)
	}
	return app, env, nil
}

// relayClient builds the client from [relay]; token wins over token_file.
func relayClient(cfg *config.Config) (*relay.Client, error) {
	token := cfg.RelayToken
	if strings.TrimSpace(token) == "" && cfg.RelayTokenFile != "" {
		data, err := os.ReadFile(expandHomePath(cfg.RelayTokenFile))
		if err != nil {
			return nil, fmt.Errorf("read the [relay] token_file: %w", err)
		}
		token = string(data)
	}
	c, err := relay.New(cfg.RelayURL, token)
	if errors.Is(err, relay.ErrNotConfigured) {
		return nil, fmt.Errorf("%w: Relay is not configured — add a [relay] section with `url` and `token_file` "+
			"(an echo-scope token) to global.toml", ErrUsage)
	}
	return c, err
}

func expandHomePath(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// relayError says what to do about a token Relay refused; the token itself
// is never printed.
func relayError(err error) error {
	switch {
	case errors.Is(err, relay.ErrUnauthorized):
		return fmt.Errorf("check the [relay] token in global.toml — mint an echo-scope token in Relay's UI: %w", err)
	case errors.Is(err, relay.ErrForbidden):
		return fmt.Errorf("the [relay] token needs the echo scope: %w", err)
	}
	return err
}

// resolveRelayShell builds the remote context of a Relay env from one resolve
// call. Every command then reaches the containers with `docker exec`, and Odoo
// reads its db credentials from the odoo.conf Relay rendered into the image's
// ODOO_RC, so the connection carries only the database and its user.
func resolveRelayShell(ctx context.Context, cfg *config.Config, sshHost, remotePath, fromName string, log func(level, sub, msg, db string, fields ...[2]string)) (remoteShellContext, error) {
	emit := func(level, sub, msg, db string, fields ...[2]string) {
		if log != nil {
			log(level, sub, msg, db, fields...)
		}
	}
	spec, _ := config.RelaySpec(remotePath)
	app, env, err := parseRelaySpec(spec)
	if err != nil {
		return remoteShellContext{}, err
	}
	client, err := relayClient(cfg)
	if err != nil {
		return remoteShellContext{}, err
	}
	re, err := client.Resolve(ctx, app, env)
	if err != nil {
		return remoteShellContext{}, relayError(err)
	}
	targetName := targetNameAt(cfg, sshHost, remotePath)
	if sshHost == "" {
		sshHost = re.SSH.Host
	}

	prof := config.RemoteProfile{
		ComposeCmd:    "docker compose",
		OdooContainer: re.Containers.Odoo,
		DBContainer:   re.Containers.DB,
		DBName:        re.DB.Name,
		Stage:         re.Stage,
		OdooVersion:   re.OdooVersion,
		ConfPath:      re.OdooConf,
	}
	target := remoteConnectTarget(prof)
	target.dbExec = dbExecDocker
	target.odooExec = dbExecDocker
	target.confPath = re.OdooConf
	target.composeProject = re.ComposeProject

	emit("INFO", "remote", "target resolved", "",
		[2]string{"host", sshHost}, [2]string{"relay", spec}, [2]string{"server", re.Server})
	emit("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage, statusProjectName(cfg, true, remotePath, fromName), prof.DBName)...)
	warnUndeclaredStage(target, emit)

	rsc := remoteShellContext{
		sshHost:    sshHost,
		remotePath: remotePath,
		fromName:   fromName,
		targetName: targetName,
		target:     target,
		prof:       prof,
		conn:       odoo.Conn{DB: re.DB.Name, User: re.DB.User},
		relay:      &relayEnv{app: app, env: env, client: client, resolved: re},
	}
	reportRemoteResolved(rsc)
	return rsc, nil
}

// relayDeployArgs accepts only the deploy flags that mean something on a
// Relay env: Relay plans the deploy (modules, snapshot, rollback) itself.
func relayDeployArgs(args []string) (dryRun bool, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--dry-run":
			dryRun = true
		case a == "--from":
			i++
		case strings.HasPrefix(a, "--from="), a == "--remote", a == "--force", a == "--json":
		default:
			return false, fmt.Errorf("%w: deploy on a Relay env takes only --dry-run (and --from/--remote/--force/--json); "+
				"Relay plans the modules, snapshot and rollback itself, so %s does not apply", ErrUsage, a)
		}
	}
	return dryRun, nil
}

// runDeployRelay hands the deploy to Relay and follows it to its end. Echo
// ships nothing: Relay deploys the env's branch.
func runDeployRelay(ctx context.Context, opts DeployOpts, sshHost, remotePath, fromName string) (DeployResult, error) {
	dryRun, err := relayDeployArgs(opts.Args)
	if err != nil {
		return DeployResult{}, err
	}
	rsc, err := resolveRelayShell(ctx, opts.Cfg, sshHost, remotePath, fromName, opts.Log)
	if err != nil {
		return DeployResult{}, err
	}
	if err := confirmRemoteProd(opts.Palette, "deploy", rsc, opts.Args); err != nil {
		return DeployResult{}, err
	}
	db := rsc.prof.DBName
	result := DeployResult{Target: targetLabel(rsc), DB: db}
	client := rsc.relay.client

	d, err := client.CreateDeploy(ctx, relay.DeployRequest{App: rsc.relay.app, Env: rsc.relay.env, DryRun: dryRun})
	if err != nil {
		return result, relayError(err)
	}
	opts.log("INFO", "relay", "deploy queued", db,
		[2]string{"deploy", strconv.FormatInt(d.ID, 10)}, [2]string{"ref", d.Ref},
		[2]string{"url", client.DeployURL(d.ID)})

	d, err = followRelayDeploy(ctx, client, d, func(level, msg string, fields ...[2]string) {
		opts.log(level, "relay", msg, db, fields...)
	})
	result.Ref, result.CodeSHA, result.Planned = d.Ref, d.AppSHA, dryRun
	if err != nil {
		return result, err
	}
	switch d.State {
	case relay.StateSucceeded:
		opts.log("INFO", "relay", "deploy succeeded", db,
			[2]string{"deploy", strconv.FormatInt(d.ID, 10)}, [2]string{"sha", shortSHA(d.AppSHA)})
		return result, nil
	case relay.StateAwaitingApproval:
		opts.log("WARNING", "relay", "deploy is waiting for an approval in Relay", db,
			[2]string{"url", client.DeployURL(d.ID)})
		return result, nil
	}
	return result, fmt.Errorf("relay deploy %d ended %s: %s", d.ID, d.State, d.Error)
}

// followRelayDeploy reads the deploy back until it ends or waits for an
// approval, reporting each state and step change once. Cancelling ctx stops
// the follow, not the deploy.
func followRelayDeploy(ctx context.Context, client *relay.Client, d relay.Deploy, log func(level, msg string, fields ...[2]string)) (relay.Deploy, error) {
	state := d.State
	steps := map[int]string{}
	for !d.Finished() && d.State != relay.StateAwaitingApproval {
		select {
		case <-ctx.Done():
			return d, fmt.Errorf("stopped following relay deploy %d (it keeps running): %w", d.ID, ctx.Err())
		case <-time.After(relayDeployPoll):
		}
		next, err := client.Deploy(ctx, d.ID)
		if err != nil {
			return d, relayError(err)
		}
		d = next
		for _, s := range d.Steps {
			if steps[s.Seq] == s.State {
				continue
			}
			steps[s.Seq] = s.State
			level := "INFO"
			if s.State == relay.StateFailed {
				level = "ERROR"
			}
			fields := [][2]string{{"step", s.Name}, {"state", s.State}}
			if s.Error != "" {
				fields = append(fields, [2]string{"err", s.Error})
			}
			log(level, "step", fields...)
		}
		if d.State != state {
			state = d.State
			log("INFO", "deploy state", [2]string{"state", state})
		}
	}
	return d, nil
}

// runLinkAddRelay registers name as the Relay env spec and binds this
// directory to it. Resolving first means an unknown env, an unreachable
// Relay or a refused token writes nothing.
func runLinkAddRelay(ctx context.Context, opts LinkOpts, name, spec, sshHost string) error {
	app, env, err := parseRelaySpec(spec)
	if err != nil {
		return err
	}
	client, err := relayClient(opts.Cfg)
	if err != nil {
		return err
	}
	re, err := client.Resolve(ctx, app, env)
	if err != nil {
		return relayError(err)
	}
	if sshHost == "" {
		sshHost = re.SSH.Host
		if re.SSH.Port != 0 && re.SSH.Port != 22 {
			opts.log("WARNING", "relay", "the receiver's SSH port is not 22 and a bare host carries no port", "",
				[2]string{"host", sshHost}, [2]string{"port", strconv.Itoa(re.SSH.Port)},
				[2]string{"fix", "add a Host block for it in ~/.ssh/config and pass that alias with --ssh-host"})
		}
	}
	t := config.ConnectTarget{
		Name:       name,
		SSHHost:    sshHost,
		RemotePath: config.RelayPath(app + "/" + env),
		Relay:      app + "/" + env,
		DBName:     re.DB.Name,
	}
	if err := config.SaveConnectTarget(t); err != nil {
		return fmt.Errorf("save connect target: %w", err)
	}
	rememberTarget(opts.Cfg, t)
	opts.log("INFO", "", "registered", re.DB.Name,
		[2]string{"target", name}, [2]string{"relay", t.Relay}, [2]string{"host", sshHost})
	return bindLinkTarget(ctx, opts, t)
}
