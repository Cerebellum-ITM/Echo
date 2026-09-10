package cmd

import (
	"context"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// remoteShellContext is everything a remote Odoo-shell run needs once the
// target is resolved: host/path, the server's profile mapping, and the
// connection flags for the shell argv.
type remoteShellContext struct {
	sshHost    string
	remotePath string
	fromName   string
	target     connectTarget
	prof       config.RemoteProfile
	conn       odoo.Conn
	// reverb is set only when the target was resolved from a Reverb daemon
	// (`-E`, Unit 107). Nil on every classic target, so the paths that must
	// behave differently in Reverb mode branch on it and legacy behavior is
	// unchanged by construction.
	reverb *reverbEnv
}

// remoteFlagsIn extracts the remote-mode switches from an argument list:
// `--from <target>` / `--from=<target>` names a global connect target
// (implying remote); bare `--remote` uses the resolution chain without a
// name (the directory's link binding, with the global-targets fallback);
// `-E <project>/<env>` / `--env <spec>` names a Reverb environment,
// normalized to the `env:` target reference so it travels through the very
// same `from` string every command already threads (Unit 107).
func remoteFlagsIn(args []string) (from string, remote bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			if i+1 < len(args) {
				from = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--from="):
			from = strings.TrimPrefix(a, "--from=")
		case a == "-E" || a == "--env":
			if i+1 < len(args) {
				from = reverbRefPrefix + args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-E="):
			from = reverbRefPrefix + strings.TrimPrefix(a, "-E=")
		case strings.HasPrefix(a, "--env="):
			from = reverbRefPrefix + strings.TrimPrefix(a, "--env=")
		case a == "--remote":
			remote = true
		}
	}
	return from, remote
}

// resolveRemoteShell resolves the remote target, reads the server's Echo
// profile, and assembles the Odoo connection — the shared preamble of the
// remote `shell` and `shell-run` paths (same recipe as i18n-pull/deploy).
// log is nil-safe.
func resolveRemoteShell(ctx context.Context, cfg *config.Config, palette theme.Palette, root, from string, log func(level, sub, msg, db string, fields ...[2]string)) (remoteShellContext, error) {
	emit := func(level, sub, msg, db string, fields ...[2]string) {
		if log != nil {
			log(level, sub, msg, db, fields...)
		}
	}
	// Reverb mode: one HTTP resolve replaces the target lookup, the SSH
	// read of the server's Echo profile and the SSH read of its .env.
	if spec, ok := reverbRefIn(from); ok {
		return resolveReverbShell(ctx, cfg, spec, log)
	}
	sshHost, remotePath, fromName, err := resolveRemoteTarget(cfg, palette, from, log)
	if err != nil {
		return remoteShellContext{}, err
	}
	emit("INFO", "remote", "target resolved", "",
		[2]string{"host", sshHost}, [2]string{"path", remotePath})

	cfgRemote := *cfg
	cfgRemote.ConnectSSHHost = sshHost
	cfgRemote.ConnectRemotePath = remotePath
	prof, err := fetchRemoteProfile(ctx, ConnectOpts{Cfg: &cfgRemote, Root: root})
	if err != nil {
		return remoteShellContext{}, err
	}
	target := remoteConnectTarget(prof)
	emit("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage,
			statusProjectName(cfg, true, remotePath, fromName),
			prof.DBName)...)

	conn := odoo.Conn{DB: target.dbName, Host: target.dbContainer}
	pg := remotePullEnv(ctx, sshHost, remotePath)
	conn.Port = pg["POSTGRES_PORT"]
	conn.User = pg["POSTGRES_USER"]
	conn.Password = pg["POSTGRES_PASSWORD"]

	// A target whose profile carries the [reverb] marker gets the
	// Reverb-specific behaviors without a flag — but only when this machine
	// can reach the daemon, since they are API calls. Without that, one line
	// says what is missing and everything runs classically.
	renv, why := reverbEnvFromProfile(cfg, prof, remotePath)
	if why != "" {
		emit("INFO", "reverb", "Reverb environment; checkpoints and lifecycle go through compose", prof.DBName,
			[2]string{"env", prof.Reverb.Project + "/" + prof.Reverb.Env}, [2]string{"reason", why})
	}

	return remoteShellContext{
		sshHost:    sshHost,
		remotePath: remotePath,
		fromName:   fromName,
		target:     target,
		prof:       prof,
		conn:       conn,
		reverb:     renv,
	}, nil
}

// confirmRemoteProd gates a remote action on the REMOTE profile's stage
// (the local config's stage is irrelevant for a remote run). `--force`
// in args skips the prompt; non-TTY fails closed inside confirmProd.
func confirmRemoteProd(palette theme.Palette, action string, rsc remoteShellContext, args []string) error {
	if !strings.EqualFold(rsc.target.stage, "prod") {
		return nil
	}
	for _, a := range args {
		if a == "--force" {
			return nil
		}
	}
	return confirmProd(palette, action, rsc.target.dbName)
}
