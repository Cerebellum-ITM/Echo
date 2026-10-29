package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/odoo"
)

// runUninstallRemote handles `uninstall … --remote | --from <t>`: the same
// UninstallScript the local path pipes into `odoo shell`, run in the remote's
// already-running Odoo container over SSH (the transport of remote
// `shell-run`). Module resolution and the prod gate follow runUpdateRemote.
// Reverb has no uninstall job, so a Reverb environment takes this path too.
func runUninstallRemote(ctx context.Context, opts ModulesOpts, from string) ([]string, error) {
	level, rest, err := extractLevel(opts.Args)
	if err != nil {
		return nil, err
	}
	installed, modules, err := parseRemoteUninstallFlags(rest)
	if err != nil {
		return nil, err
	}

	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)
	if err != nil {
		return nil, err
	}

	if len(modules) == 0 {
		avail, lerr := remoteUpdateCandidates(ctx, rsc, installed)
		if lerr != nil {
			return nil, lerr
		}
		if len(avail) == 0 {
			return nil, ErrNoModulesAvailable
		}
		title := "Modules to uninstall on " + targetLabel(rsc)
		picked, _, canceled, perr := runFuzzyPickerCore(title, avail, nil, nil, nil, opts.Palette, rsc.target.rawStage)
		if perr != nil {
			return nil, perr
		}
		if canceled || len(picked) == 0 {
			return nil, ErrCancelled
		}
		modules = picked
	}

	if err := confirmRemoteProd(opts.Palette, "uninstall", rsc, opts.Args); err != nil {
		return nil, err
	}

	emitResolved(opts, modules)
	argv := odoo.WithLogLevel(odoo.Shell(rsc.conn), level)
	return modules, runSSHStream(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, rsc.target, argv),
		[]byte(odoo.UninstallScript(modules)), opts.StreamOut)
}

// parseRemoteUninstallFlags reads the post-`extractLevel` args for a remote
// uninstall. The remote selectors and `--force` are consumed (a bare
// `--from`'s value token skipped) so they never read as modules; any other
// `-`-flag, `--all` and `--last` included, is a usage error.
func parseRemoteUninstallFlags(rest []string) (installed bool, modules []string, err error) {
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--installed":
			installed = true
		case a == "--force", a == "--remote", a == "--json":
			// consumed by confirmRemoteProd / remoteFlagsIn / the REPL
		case a == "--from", a == "-E", a == "--env":
			i++
		case strings.HasPrefix(a, "--from="), strings.HasPrefix(a, "-E="),
			strings.HasPrefix(a, "--env="):
		case strings.HasPrefix(a, "-"):
			return false, nil, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			modules = append(modules, a)
		}
	}
	return installed, modules, nil
}
