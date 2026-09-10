package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

// Reverb waiting policy for a 409 not_ready — an environment whose
// create/fork job is still running. Waiting it out beats failing: the
// user asked for an environment that is on its way up. A real env_create
// takes 60-90s, so the bounded fallback has to be minutes, not seconds.
const (
	reverbResolveDelay  = 2 * time.Second
	reverbProvisionWait = 2 * time.Minute
	reverbJobPoll       = 2 * time.Second
	// reverbSettleWait covers the gap between a create job reporting
	// success and resolve reporting the environment ready — two separate
	// observations, so the second one gets a short grace window.
	reverbSettleWait = 20 * time.Second
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
	// id addresses every route past resolve (snapshots, overlay, deploy).
	id      int64
	project string
	env     string
	paths   reverb.Paths
	// apiURL is the daemon the HOST declared in its profile marker. It wins
	// over the client's own [reverb] url so moving the daemon does not touch
	// every laptop. Empty on the `-E` path, where the client already had to
	// know the URL to resolve at all.
	apiURL string
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

// reverbClientFor builds the client for a resolved environment, preferring
// the API URL the host declared over the client's own.
func reverbClientFor(cfg *config.Config, env *reverbEnv) (*reverb.Client, error) {
	if env == nil || strings.TrimSpace(env.apiURL) == "" {
		return reverbClient(cfg)
	}
	c, err := reverb.New(env.apiURL, cfg.ReverbToken)
	if errors.Is(err, reverb.ErrNotConfigured) {
		return nil, fmt.Errorf("%w: no Reverb token — add `token` (scope: echo) to the [reverb] section "+
			"of global.toml; the url comes from the environment's own profile (%s)", ErrUsage, env.apiURL)
	}
	return c, err
}

// reverbEnvFromProfile builds the environment context for a CLASSIC linked
// target whose server-side profile carries the [reverb] marker — link mode,
// where nothing was resolved over HTTP.
//
// It returns a context only when the client can also reach the daemon,
// because that is the invariant every `rsc.reverb != nil` branch relies on:
// snapshots and the lifecycle verbs are API calls. With a marker but no
// token the second return value says what is missing and the caller falls
// back to the classic behavior, which still works.
//
// paths comes from the profile rather than a resolve: Reverb declares the
// overlay as the profile's [push] path, and the compose dir is the target's
// own remote path. addons stays empty — isUnderAddons treats that as "not
// checkable" and refuses nothing.
func reverbEnvFromProfile(cfg *config.Config, prof config.RemoteProfile, remotePath string) (*reverbEnv, string) {
	m := prof.Reverb
	if m == nil {
		return nil, ""
	}
	url := strings.TrimSpace(m.APIURL)
	if url == "" {
		url = strings.TrimSpace(cfg.ReverbURL)
	}
	switch {
	case url == "":
		return nil, "no daemon url — the profile declares none and [reverb] url is unset"
	case strings.TrimSpace(cfg.ReverbToken) == "":
		return nil, "no [reverb] token configured"
	}
	return &reverbEnv{
		id:      m.EnvID,
		project: m.Project,
		env:     m.Env,
		apiURL:  url,
		paths: reverb.Paths{
			Overlay:    prof.PushPath,
			ComposeDir: remotePath,
		},
	}, ""
}

// reverbComposeCmd returns the compose binary for a Reverb host.
func reverbComposeCmd(cfg *config.Config) string {
	if c := strings.TrimSpace(cfg.ReverbComposeCmd); c != "" {
		return c
	}
	return defaultReverbComposeCmd
}

// reverbSSHHost decides which host every ssh/rsync of this run dials.
//
// Echo has never had a port field, in any mode: it passes the host
// verbatim and lets the user's ~/.ssh/config resolve port, user, identity
// and ProxyJump. That works in classic mode because `ssh_host` is a name
// the USER wrote — an alias matching one of their `Host` blocks. The
// payload's host is written by the daemon and is a literal `user@ip`,
// which matches no `Host` block, so everything that block carried is lost.
//
// The fix is to let the user name the host again ([reverb] ssh_host),
// rather than to teach Echo a second SSH-configuration channel. The
// payload could only ever supply the port — the identity is deliberately
// never in it ("Reverb ships no keys") — so `ssh -p N` would resolve the
// least useful third of the problem and still leave the ssh_config block
// to be written. `ssh_port` is therefore read as a DIAGNOSTIC only: it
// turns an otherwise inscrutable "connection refused" into a message
// naming the gap.
func reverbSSHHost(cfg *config.Config, re reverb.Env, emit func(level, sub, msg, db string, fields ...[2]string)) string {
	if alias := strings.TrimSpace(cfg.ReverbSSHHost); alias != "" {
		emit("INFO", "reverb", "using the local ssh alias", "",
			[2]string{"alias", alias}, [2]string{"payload_host", re.SSHHost})
		return alias
	}
	// A non-default port the payload reports but ssh will never see, because
	// a literal user@host matches no Host block. Say so before the failure.
	if re.SSHPort != 0 && re.SSHPort != 22 {
		emit("WARNING", "reverb", "the daemon reports a non-default SSH port that your ssh config will not apply", "",
			[2]string{"host", re.SSHHost},
			[2]string{"port", strconv.Itoa(re.SSHPort)},
			[2]string{"fix", "add a Host block for it in ~/.ssh/config, or set [reverb] ssh_host to your own alias"})
	}
	return re.SSHHost
}

// resolveWaiting resolves an environment, waiting out a provisioning job
// instead of failing on the 409 it answers with. A real env_create takes
// 60-90s, so a blind short retry always loses the race.
//
// The good path follows the job and streams its progress: the id from the
// listing finds the in-flight env_create/env_fork, and its events are the
// same ones Reverb's own UI shows. When the job cannot be identified (no
// id, listing unavailable, nothing in flight) it degrades to a bounded
// backoff rather than giving up after a few seconds.
func resolveWaiting(ctx context.Context, client *reverb.Client, project, env string, envID int64, emit func(level, sub, msg, db string, fields ...[2]string)) (reverb.Env, error) {
	re, err := client.Resolve(ctx, project, env)
	if err == nil || !errors.Is(err, reverb.ErrNotReady) {
		return re, err
	}

	if envID == 0 {
		// The bare-<env> form already listed; the qualified one has not.
		if refs, lerr := client.ListEnvs(ctx); lerr == nil {
			for _, r := range refs {
				if r.Project == project && r.Env == env {
					envID = r.ID
					break
				}
			}
		}
	}
	if envID != 0 {
		if jobs, lerr := client.ListJobs(ctx, envID); lerr == nil {
			if job, ok := reverb.ProvisioningJob(jobs); ok {
				emit("INFO", "reverb", "environment is provisioning — following the job", "",
					[2]string{"job", job.ID}, [2]string{"kind", job.Kind})
				done, ferr := client.FollowJob(ctx, job.ID, reverbJobPoll, func(e reverb.JobEvent) {
					emit(reverbEventLevel(e.Level), "reverb", e.Message, "")
				})
				if ferr != nil {
					return reverb.Env{}, ferr
				}
				if jerr := reverb.JobFailure(done); jerr != nil {
					return reverb.Env{}, jerr
				}
				// The job finishing and resolve reporting ready are two
				// separate observations: retry briefly rather than losing
				// the race by a second.
				return resolveBounded(ctx, client, project, env, reverbSettleWait)
			}
		}
	}

	// No job to follow — wait it out, bounded, instead of failing fast.
	emit("WARNING", "reverb", "environment is still provisioning — waiting", "",
		[2]string{"timeout", reverbProvisionWait.String()})
	return resolveBounded(ctx, client, project, env, reverbProvisionWait)
}

// resolveBounded retries a not_ready resolve for at most `window`.
func resolveBounded(ctx context.Context, client *reverb.Client, project, env string, window time.Duration) (reverb.Env, error) {
	attempts := int(window / reverbResolveDelay)
	if attempts < 1 {
		attempts = 1
	}
	return client.ResolveWithRetry(ctx, project, env, attempts, reverbResolveDelay, nil)
}

// reverbEventLevel maps a job event's level onto Echo's log levels.
func reverbEventLevel(level string) string {
	switch strings.ToLower(level) {
	case "error", "fatal":
		return "ERROR"
	case "warn", "warning":
		return "WARNING"
	default:
		return "INFO"
	}
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
	// is the cheap call, so it only runs for the ambiguous form. It also
	// yields the id, which is what a not_ready wait needs to find the job.
	var listed reverb.EnvRef
	if project == "" {
		refs, lerr := client.ListEnvs(ctx)
		if lerr != nil {
			return remoteShellContext{}, reverbError(lerr)
		}
		listed, err = reverb.FindEnv(refs, env)
		if err != nil {
			return remoteShellContext{}, err
		}
		project = listed.Project
	}

	emit("INFO", "reverb", "resolving environment", "",
		[2]string{"project", project}, [2]string{"env", env})

	re, err := resolveWaiting(ctx, client, project, env, listed.ID, emit)
	if err != nil {
		return remoteShellContext{}, reverbError(err)
	}

	// No ssh_host means the daemon has no public_host configured. Per the
	// contract this is a server-side configuration error, never a silent
	// fallback to some other host.
	if strings.TrimSpace(re.SSHHost) == "" && strings.TrimSpace(cfg.ReverbSSHHost) == "" {
		return remoteShellContext{}, fmt.Errorf(
			"reverb resolved %s/%s without an ssh_host — the daemon has no public_host set "+
				"(REVERB_PUBLIC_HOST); fix it there, or name your own alias in [reverb] ssh_host",
			project, env)
	}
	if strings.TrimSpace(re.Paths.ComposeDir) == "" {
		return remoteShellContext{}, fmt.Errorf(
			"reverb resolved %s/%s without paths.compose_dir — the daemon predates the Echo contract (unit 20)",
			project, env)
	}

	renv := &reverbEnv{id: re.ID, project: re.Project, env: re.Env, paths: re.Paths}
	if renv.project == "" {
		renv.project = project
	}
	if renv.env == "" {
		renv.env = env
	}
	if renv.id == 0 {
		renv.id = listed.ID
	}

	sshHost := reverbSSHHost(cfg, re, emit)

	target := connectTarget{
		remote:        true,
		composeCmd:    reverbComposeCmd(cfg),
		odooContainer: re.Containers.Odoo,
		dbContainer:   re.Containers.DB,
		dbName:        re.DB.Name,
		stage:         re.Stage,
		odooVersion:   re.OdooVersion,
		// containers.db in the resolve payload is a container NAME; the
		// environment's compose file has no service for it.
		dbExec: dbExecDocker,
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
		[2]string{"host", sshHost}, [2]string{"path", re.Paths.ComposeDir})
	emit("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage, renv.project+"/"+renv.env, prof.DBName)...)

	return remoteShellContext{
		sshHost:    sshHost,
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
		return fmt.Errorf("gave up after %s — retry in a moment: %w", reverbProvisionWait, err)
	default:
		return err
	}
}

// reverbDeferred names the commands that still cannot run against a
// Reverb target, each with the reason running it anyway would corrupt
// state. Anything absent from this map is supported — the lifecycle and
// checkpoint verbs used to live here and were only ever blocked by token
// scope, which the contract has since granted.
var reverbDeferred = map[string]string{
	"deploy": "Reverb runs the deploy itself (POST /environments/{id}/deploy) and replaces the addons " +
		"directory wholesale, so Echo's own deploy would be overwritten by the next one — " +
		"use `push -E …` for uncommitted code meanwhile",
	"watch":     "its deploy half has to be delegated to Reverb first — use `push -E …` to sync modules meanwhile",
	"i18n-pull": "it reads the server's own Echo profile over SSH, which a Reverb host does not have",
}

// requireNoReverb refuses one of the deferred commands when the target is
// a Reverb environment. Every other command passes through untouched.
func requireNoReverb(name, from string) error {
	if _, ok := reverbRefIn(from); !ok {
		return nil
	}
	why, deferred := reverbDeferred[name]
	if !deferred {
		return nil
	}
	return fmt.Errorf("%w: %s does not support Reverb targets yet — %s", ErrUsage, name, why)
}
