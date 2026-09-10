package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/odoo"
)

// runUpdateRemote handles `update … --remote | --from <t>`: it runs the Odoo
// module update on a remote target over SSH instead of the local container,
// reusing the same transport as `deploy`/`shell`. The update executes in the
// remote's already-running Odoo container (`compose exec … odoo -u …
// --stop-after-init`), exactly like the local path does in its container — no
// container recreation (that is `deploy`'s job). The remote DB is mutated, so
// a prod-stage target asks for a red confirmation unless `--force`.
//
// Module resolution mirrors the local flow, but sourced from the remote:
// explicit names, `--all`, or a picker over the remote's own addons (or, with
// `--installed`, every module installed in the remote DB). `--last` is
// local-only state and is rejected here.
func runUpdateRemote(ctx context.Context, opts ModulesOpts, from string) ([]string, error) {
	level, rest, err := extractLevel(opts.Args)
	if err != nil {
		return nil, err
	}
	all, i18n, installed, noCheckpoint, modules, err := parseRemoteUpdateFlags(rest)
	if err != nil {
		return nil, err
	}

	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)
	if err != nil {
		return nil, err
	}

	// No modules and not --all → pick from the remote's module list, tinted by
	// the remote stage.
	if !all && len(modules) == 0 {
		avail, lerr := remoteUpdateCandidates(ctx, rsc, installed)
		if lerr != nil {
			return nil, lerr
		}
		if len(avail) == 0 {
			return nil, ErrNoModulesAvailable
		}
		title := "Modules to update on " + targetLabel(rsc)
		picked, _, canceled, perr := runFuzzyPickerCore(title, avail, nil, nil, nil, opts.Palette, rsc.target.stage)
		if perr != nil {
			return nil, perr
		}
		if canceled || len(picked) == 0 {
			return nil, ErrCancelled
		}
		modules = picked
	}

	if err := confirmRemoteProd(opts.Palette, "update", rsc, opts.Args); err != nil {
		return nil, err
	}

	// A linked Reverb environment with a reachable API runs the update on the
	// daemon's side: Odoo stopped, signaling sequences reset, a pre_update
	// checkpoint and a rollback on failure — none of which a `compose exec`
	// beside the live process gives. `--i18n` stays on the classic path: the
	// job has no i18n switch.
	if rsc.reverb != nil && !i18n {
		if all {
			return nil, fmt.Errorf("%w: --all is refused on a Reverb environment; list the modules to update", ErrUsage)
		}
		return runUpdateReverb(ctx, opts, rsc, modules, !noCheckpoint)
	}

	var argv odoo.Cmd
	resolved := modules
	if all {
		argv = odoo.UpdateAll(rsc.conn)
		resolved = []string{"--all"}
	} else {
		argv = odoo.Update(rsc.conn, modules)
	}
	argv = odoo.WithI18nOverwrite(odoo.WithLogLevel(argv, level), i18n)

	emitResolved(opts, resolved)
	if err := runSSHStream(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, rsc.target, argv), nil, opts.StreamOut); err != nil {
		return resolved, err
	}
	return resolved, nil
}

// parseRemoteUpdateFlags reads the post-`extractLevel` args for a remote
// update: the `--all`/`--i18n`/`--installed` switches and the positional
// module names. The remote selectors (`--from <t>`/`--from=t`/`--remote`) and
// `--force` are consumed here (their value token skipped) so they never read
// as modules. `--last` is rejected (local-only state) and any other `-`-flag
// is a usage error.
func parseRemoteUpdateFlags(rest []string) (all, i18n, installed, noCheckpoint bool, modules []string, err error) {
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--all":
			all = true
		case a == "--i18n":
			i18n = true
		case a == "--installed":
			installed = true
		case a == "--no-checkpoint":
			// Only meaningful on a Reverb environment, where the update takes a
			// pre_update checkpoint by default.
			noCheckpoint = true
		case a == "--force", a == "--remote":
			// consumed by confirmRemoteProd / remoteFlagsIn
		case a == "--from", a == "-E", a == "--env":
			i++ // skip the target value; captured by remoteFlagsIn
		case strings.HasPrefix(a, "--from="), strings.HasPrefix(a, "-E="),
			strings.HasPrefix(a, "--env="):
			// consumed by remoteFlagsIn
		case a == "--last":
			return false, false, false, false, nil,
				fmt.Errorf("%w: --last is local-only (not supported with --remote)", ErrUsage)
		case strings.HasPrefix(a, "-"):
			return false, false, false, false, nil, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			modules = append(modules, a)
		}
	}
	return all, i18n, installed, noCheckpoint, modules, nil
}

// runUpdateReverb posts the update to Reverb and streams the job's events,
// so the user sees the stop, the one-shot's output, the start and — when it
// fails — the rollback to the checkpoint.
func runUpdateReverb(ctx context.Context, opts ModulesOpts, rsc remoteShellContext, modules []string, checkpoint bool) ([]string, error) {
	client, err := reverbClientFor(opts.Cfg, rsc.reverb)
	if err != nil {
		return nil, err
	}
	jobID, err := client.UpdateModules(ctx, rsc.reverb.id, modules, checkpoint)
	if err != nil {
		return nil, reverbError(err)
	}
	emitResolved(opts, modules)
	stream := func(line string) {
		if opts.StreamOut != nil {
			opts.StreamOut(line)
		}
	}
	stream("reverb: update delegated to " + rsc.reverb.ref() + " job=" + jobID + " checkpoint=" + boolWord(checkpoint))
	if err := followReverbJob(ctx, client, jobID, "update",
		func(_, _, msg, _ string, _ ...[2]string) { stream(msg) },
		rsc.prof.DBName); err != nil {
		return modules, err
	}
	return modules, nil
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// remoteUpdateCandidates lists the modules the remote `update` picker offers:
// the remote's own addons by default, or every module installed in the remote
// database with --installed (so core modules like `base` are pickable).
func remoteUpdateCandidates(ctx context.Context, rsc remoteShellContext, installed bool) ([]string, error) {
	if installed {
		return listRemoteModules(ctx, rsc.sshHost, rsc.remotePath, rsc.target, rsc.conn.User, rsc.target.dbName)
	}
	return listRemoteConfModules(ctx, rsc.sshHost, rsc.remotePath, rsc.target, rsc.prof.ConfPath, rsc.prof.AddonsPaths)
}
