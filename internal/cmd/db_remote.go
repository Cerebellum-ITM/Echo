package cmd

import (
	"context"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

// DB exec transports. A remote Postgres reached as a service of the
// project's compose file uses dbExecCompose; one that only exists as a
// container — Reverb keeps its Postgres in the project's compose, not the
// environment's — uses dbExecDocker.
const (
	dbExecCompose = "compose"
	dbExecDocker  = "docker"
)

// remoteConnectTarget builds the resolved container/db mapping from a
// server-side Echo profile. Every remote command goes through it, so the
// choice of DB transport is made in one place: a profile carrying the
// [reverb] marker names its db container, not a compose service.
func remoteConnectTarget(prof config.RemoteProfile) connectTarget {
	t := connectTarget{
		remote:        true,
		composeCmd:    prof.ComposeCmd,
		odooContainer: prof.OdooContainer,
		dbContainer:   prof.DBContainer,
		dbName:        prof.DBName,
		stage:         prof.Stage,
		odooVersion:   prof.OdooVersion,
	}
	if prof.Reverb != nil {
		t.dbExec = dbExecDocker
	}
	return t
}

// dbExecMode is the transport the target declares, defaulting to compose so
// a zero-value target behaves as it always did.
func (t connectTarget) dbExecMode() string {
	if t.dbExec == dbExecDocker {
		return dbExecDocker
	}
	return dbExecCompose
}

// dbExecInner builds the exec that runs argv in the remote Postgres
// container, without the `cd`. Callers that redirect to a server-side file
// prepend their own working directory.
func dbExecInner(t connectTarget, mode string, argv odoo.Cmd) string {
	var b strings.Builder
	if mode == dbExecDocker {
		// -i, not -it: pg_restore reads its archive from stdin and an SSH
		// run has no TTY.
		b.WriteString("docker exec -i ")
	} else {
		b.WriteString(t.composeCmd)
		b.WriteString(" exec -T ")
	}
	b.WriteString(shellQuote(t.dbContainer))
	for _, a := range argv {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	return b.String()
}

// dbExecCmd is dbExecInner as a full remote command line: the compose form
// needs the project directory, the docker form resolves the container by
// name and does not.
func dbExecCmd(remotePath string, t connectTarget, mode string, argv odoo.Cmd) string {
	inner := dbExecInner(t, mode, argv)
	if mode == dbExecDocker {
		return inner
	}
	return "cd " + shellQuote(remotePath) + " && " + inner
}

// withDBExecFallback runs the target's declared DB transport and retries
// once with `docker exec` when the compose project turns out to have no
// such service. That covers a hand-built target whose Postgres moved out of
// its compose file and carries no [reverb] marker to declare it.
func withDBExecFallback(t connectTarget, run func(mode string) error) error {
	mode := t.dbExecMode()
	err := run(mode)
	if err != nil && mode == dbExecCompose && isNoSuchService(err) {
		return run(dbExecDocker)
	}
	return err
}

// isNoSuchService reports whether err carries compose's complaint about a
// service the project does not define. Both SSH runners fold the remote
// stderr into the error message, so matching the text is the only signal
// available.
func isNoSuchService(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such service")
}

// sshRunner is the buffered SSH transport, parameterized so the checkpoint
// path can pass its own test seam.
type sshRunner func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error)

// runRemoteDBCmd runs argv in the remote Postgres container and returns its
// output, honoring the target's transport and the no-such-service fallback.
func runRemoteDBCmd(ctx context.Context, run sshRunner, sshHost, remotePath string, t connectTarget, argv odoo.Cmd) ([]byte, error) {
	var out []byte
	err := withDBExecFallback(t, func(mode string) error {
		var e error
		out, e = run(ctx, sshHost, dbExecCmd(remotePath, t, mode, argv), nil)
		return e
	})
	return out, err
}
