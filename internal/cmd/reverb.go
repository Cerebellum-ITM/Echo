package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/reverb"
)

// reverbRefPrefix marks a target reference as a Reverb environment rather
// than a name in [connect_targets]. `-E <spec>` is sugar for
// `--from env:<spec>`, which is what lets every command that already
// threads a `from` string reach Reverb mode without a new parameter.
// The prefix is RESERVED: a connect target must not be named with it.
const reverbRefPrefix = "env:"

// Reverb resolve retry policy for a 409 not_ready — an environment whose
// create/fork job is still running. Waiting it out beats failing: the
// user asked for an environment that is on its way up.
const (
	reverbResolveAttempts = 3
	reverbResolveDelay    = 2 * time.Second
)

// defaultReverbComposeCmd is the compose binary assumed on a Reverb host.
// A classic remote declares its own in the server's Echo profile; a Reverb
// host has no such profile, so this is the default and [reverb]
// compose_cmd is the override.
const defaultReverbComposeCmd = "docker compose"

// reverbEnv is the resolved Reverb environment attached to a
// remoteShellContext. Nil on every classic target — code that must behave
// differently in Reverb mode branches on `rsc.reverb != nil`, so the
// legacy paths are untouched by construction.
type reverbEnv struct {
	project string
	env     string
	paths   reverb.Paths
}

// ref renders the environment back as the canonical target reference,
// used as the history/label key in place of a connect-target name.
func (r *reverbEnv) ref() string {
	return reverbRefPrefix + r.project + "/" + r.env
}

// ReverbRef renders a `-E` value as the canonical target reference.
// Exported for the REPL-side parsers (shell-run) that capture the flag
// value themselves instead of going through remoteFlagsIn.
func ReverbRef(spec string) string { return reverbRefPrefix + spec }

// reverbRefIn reports whether a target reference names a Reverb
// environment, returning the `<project>/<env>` (or bare `<env>`) spec.
func reverbRefIn(from string) (spec string, ok bool) {
	if !strings.HasPrefix(from, reverbRefPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(from, reverbRefPrefix)), true
}

// parseReverbSpec splits a reference into project and environment. A bare
// `<env>` returns an empty project, which the resolver fills in from
// GET /envs. More than one separator is a usage error rather than a
// silent guess.
func parseReverbSpec(spec string) (project, env string, err error) {
	spec = strings.Trim(strings.TrimSpace(spec), "/")
	if spec == "" {
		return "", "", fmt.Errorf("%w: -E needs <project>/<env> or <env>", ErrUsage)
	}
	parts := strings.Split(spec, "/")
	switch len(parts) {
	case 1:
		return "", parts[0], nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("%w: malformed environment reference %q — use <project>/<env>", ErrUsage, spec)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("%w: malformed environment reference %q — use <project>/<env>", ErrUsage, spec)
	}
}

// reverbClient builds the API client from the global [reverb] table, or
// explains that the section is missing. The token is never echoed.
func reverbClient(cfg *config.Config) (*reverb.Client, error) {
	c, err := reverb.New(cfg.ReverbURL, cfg.ReverbToken)
	if errors.Is(err, reverb.ErrNotConfigured) {
		return nil, fmt.Errorf("%w: no Reverb configured — add a [reverb] section with `url` and `token` "+
			"(scope: echo) to global.toml", ErrUsage)
	}
	return c, err
}

// reverbComposeCmd returns the compose binary for a Reverb host.
func reverbComposeCmd(cfg *config.Config) string {
	if c := strings.TrimSpace(cfg.ReverbComposeCmd); c != "" {
		return c
	}
	return defaultReverbComposeCmd
}

// resolveReverbShell builds a remoteShellContext from a Reverb resolve
// call — the Reverb-mode counterpart of the classic path's
// resolveRemoteTarget + fetchRemoteProfile + remotePullEnv, which it
// replaces with one HTTP request. Nothing is persisted: the target lives
// only for this invocation, which is the whole point of Reverb mode.
func resolveReverbShell(ctx context.Context, cfg *config.Config, spec string, log func(level, sub, msg, db string, fields ...[2]string)) (remoteShellContext, error) {
	emit := func(level, sub, msg, db string, fields ...[2]string) {
		if log != nil {
			log(level, sub, msg, db, fields...)
		}
	}
	project, env, err := parseReverbSpec(spec)
	if err != nil {
		return remoteShellContext{}, err
	}
	client, err := reverbClient(cfg)
	if err != nil {
		return remoteShellContext{}, err
	}

	// A bare <env> needs its project. The listing carries no secrets and
	// is the cheap call, so it only runs for the ambiguous form.
	if project == "" {
		refs, lerr := client.ListEnvs(ctx)
		if lerr != nil {
			return remoteShellContext{}, reverbError(lerr)
		}
		project, err = reverb.FindEnv(refs, env)
		if err != nil {
			return remoteShellContext{}, err
		}
	}

	emit("INFO", "reverb", "resolving environment", "",
		[2]string{"project", project}, [2]string{"env", env})

	re, err := client.ResolveWithRetry(ctx, project, env,
		reverbResolveAttempts, reverbResolveDelay,
		func(attempt, total int) {
			emit("WARNING", "reverb", "environment is still provisioning — waiting", "",
				[2]string{"attempt", fmt.Sprintf("%d/%d", attempt, total)})
		})
	if err != nil {
		return remoteShellContext{}, reverbError(err)
	}

	// No ssh_host means the daemon has no public_host configured. Per the
	// contract this is a server-side configuration error, never a silent
	// fallback to some other host.
	if strings.TrimSpace(re.SSHHost) == "" {
		return remoteShellContext{}, fmt.Errorf(
			"reverb resolved %s/%s without an ssh_host — the daemon has no public_host set "+
				"(REVERB_PUBLIC_HOST); fix it there, there is no client-side workaround",
			project, env)
	}
	if strings.TrimSpace(re.Paths.ComposeDir) == "" {
		return remoteShellContext{}, fmt.Errorf(
			"reverb resolved %s/%s without paths.compose_dir — the daemon predates the Echo contract (unit 20)",
			project, env)
	}

	renv := &reverbEnv{project: re.Project, env: re.Env, paths: re.Paths}
	if renv.project == "" {
		renv.project = project
	}
	if renv.env == "" {
		renv.env = env
	}

	target := connectTarget{
		remote:        true,
		composeCmd:    reverbComposeCmd(cfg),
		odooContainer: re.Containers.Odoo,
		dbContainer:   re.Containers.DB,
		dbName:        re.DB.Name,
		stage:         re.Stage,
		odooVersion:   re.OdooVersion,
	}
	// The profile is synthesized from the same payload so every call site
	// reading rsc.prof.* behaves exactly as it does on a classic target.
	// AddonsPaths stays empty: the module-base probes already fall back.
	prof := config.RemoteProfile{
		ComposeCmd:    target.composeCmd,
		OdooContainer: target.odooContainer,
		DBContainer:   target.dbContainer,
		DBName:        target.dbName,
		Stage:         target.stage,
		OdooVersion:   target.odooVersion,
	}

	emit("INFO", "remote", "target resolved", "",
		[2]string{"host", re.SSHHost}, [2]string{"path", re.Paths.ComposeDir})
	emit("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage, renv.project+"/"+renv.env, prof.DBName)...)

	return remoteShellContext{
		sshHost:    re.SSHHost,
		remotePath: re.Paths.ComposeDir,
		fromName:   renv.ref(),
		target:     target,
		prof:       prof,
		conn: odoo.Conn{
			DB:       re.DB.Name,
			Host:     re.Containers.DB,
			User:     re.DB.User,
			Password: re.DB.PasswordValue(),
		},
		reverb: renv,
	}, nil
}

// reverbError turns the client's sentinels into the operator-facing
// message: 401/403 are configuration problems, said plainly, and the
// token itself is never printed.
func reverbError(err error) error {
	switch {
	case errors.Is(err, reverb.ErrUnauthorized):
		return fmt.Errorf("check `token` in the [reverb] section of global.toml — mint a new one in the "+
			"Reverb UI with scope `echo`: %w", err)
	case errors.Is(err, reverb.ErrForbidden):
		return fmt.Errorf("mint a token with the `echo` scope and update [reverb] token: %w", err)
	case errors.Is(err, reverb.ErrNotReady):
		return fmt.Errorf("gave up after %d attempts — retry in a moment: %w", reverbResolveAttempts, err)
	default:
		return err
	}
}

// requireNoReverb refuses a command that cannot run against a Reverb
// target yet. These are not oversights: each names the reason running it
// anyway would corrupt state, plus what to do instead.
//
// Deferred to Units 108-110 (deploy delegation, snapshots, docker API).
func requireNoReverb(name, from string) error {
	if _, ok := reverbRefIn(from); !ok {
		return nil
	}
	var why string
	switch name {
	case "deploy":
		why = "Reverb runs the deploy itself (POST /environments/{id}/deploy) and replaces the addons " +
			"directory wholesale, so Echo's own deploy would be overwritten by the next one — " +
			"use `push -E …` for uncommitted code meanwhile"
	case "watch":
		why = "its deploy half has to be delegated to Reverb first — use `push -E …` to sync modules meanwhile"
	case "checkpoint":
		why = "Reverb has snapshots with the same intent, and a parallel checkpoint store would duplicate " +
			"state and confuse rollback"
	case "i18n-pull":
		why = "it reads the server's own Echo profile over SSH, which a Reverb host does not have"
	case "up", "down", "stop", "restart":
		why = "Reverb reconciles desired vs observed state, so a compose command run behind its back " +
			"shows up as drift — `ps` and `logs` are read-only and do work"
	default:
		why = "it is not part of the Reverb-mode surface yet"
	}
	return fmt.Errorf("%w: %s does not support Reverb targets yet — %s", ErrUsage, name, why)
}
