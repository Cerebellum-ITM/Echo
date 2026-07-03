package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/odoo"
)

// parseTestArgs splits a `test` argument list into the resolved module
// positionals and its flags. The remote switches (`--from <t>` /
// `--from=<t>` / `--remote`) are consumed here too so the value token
// after a bare `--from` is never captured as a module name; other
// `-`-prefixed tokens are ignored (forward-compat). Kept pure so it is
// unit-testable without a container.
func parseTestArgs(args []string) (modules []string, tags string, update bool, from string, remote bool, err error) {
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
		case a == "--remote":
			remote = true
		case a == "--tags":
			if i+1 >= len(args) {
				return nil, "", false, "", false, fmt.Errorf("--tags requires a value")
			}
			tags = args[i+1]
			i++
		case strings.HasPrefix(a, "--tags="):
			tags = strings.TrimPrefix(a, "--tags=")
		case a == "--update":
			update = true
		case strings.HasPrefix(a, "-"):
			// forward-compat: ignore unknown flags instead of failing
		default:
			modules = append(modules, a)
		}
	}
	return modules, tags, update, from, remote, nil
}

// runTestRemote runs the resolved test suite in a REMOTE Odoo container
// over SSH: `ssh <host> 'cd <path> && <compose> exec -T <odoo> odoo …'`,
// streamed live through runSSHStream — the remote analog of runOdoo's
// docker.Exec path, sharing the transport with deploy / shell-run. The
// connection comes from the remote Echo profile, never local config.
func runTestRemote(ctx context.Context, opts ModulesOpts, from string, o odoo.TestOpts) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)
	if err != nil {
		return err
	}
	if err := confirmRemoteProd(opts.Palette, "test", rsc, opts.Args); err != nil {
		return err
	}
	remoteCmd := remoteContainerCmd(rsc.remotePath, rsc.target, odoo.Test(rsc.conn, o))
	return runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut)
}
