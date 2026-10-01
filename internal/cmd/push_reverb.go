package cmd

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// reverbPushDestination resolves where `push` writes on a Reverb target.
//
// This is the one behavioral change Reverb mode makes: Reverb OWNS the
// addons directory and replaces it wholesale on every deploy, while the
// overlay is the directory it never touches — and a module in the overlay
// shadows the git copy (Odoo's get_module_path resolves it there). So code
// pushed to addons is destroyed by the next deploy, and code pushed to the
// overlay survives and runs.
//
// Order: an explicit destination (`--dest` / `[push] path`) is honored as
// an escape hatch UNLESS it lands under paths.addons, which is refused;
// otherwise the default is paths.overlay. The per-module auto-detect never
// applies here — the payload already names the right directory.
func reverbPushDestination(ctx context.Context, rsc remoteShellContext, opts PushOpts, p pushArgs) (string, error) {
	overlay := strings.TrimSpace(rsc.reverb.paths.Overlay)
	if overlay == "" {
		return "", errNoOverlay(rsc)
	}
	if p.pickDest || p.setDest {
		return "", fmt.Errorf("%w: --pick-dest/--set-dest do not apply to a Reverb target — "+
			"the destination is the environment's overlay (%s)", ErrUsage, overlay)
	}

	dest, source, mkdir := resolvePushDest(p, rsc.prof, opts.Cfg)
	if dest == "" {
		if err := ensureRemoteDir(ctx, rsc, overlay, p.dryRun); err != nil {
			return "", err
		}
		opts.log("INFO", "", "using the environment overlay", rsc.prof.DBName,
			[2]string{"dest", overlay}, [2]string{"source", "reverb"})
		return overlay, nil
	}

	resolved := resolveDestPath(rsc.remotePath, dest)
	if resolved == "" {
		return "", fmt.Errorf("%w: push destination cannot be the compose project root", ErrUsage)
	}
	if isUnderAddons(rsc.reverb.paths.Addons, resolved) {
		return "", fmt.Errorf("%w: refusing to push to %s (destination from: %s) — Reverb replaces the addons "+
			"directory wholesale on every deploy, so the code would be destroyed by the next one; drop that "+
			"destination and the push lands in the overlay (%s), which Reverb never touches",
			ErrUsage, resolved, source, overlay)
	}
	return applyResolvedDest(ctx, rsc, opts, dest, source, mkdir, p.dryRun)
}

// errNoOverlay explains a Reverb environment whose overlay directory is
// unknown. It has two sources — the resolve payload under `-E`, the
// profile's [push] path on a linked target — and both mean the daemon
// predates the Echo contract.
func errNoOverlay(rsc remoteShellContext) error {
	return fmt.Errorf("no overlay directory for %s — neither the resolve payload nor the server profile's "+
		"[push] path names one; the daemon predates the Echo contract (unit 20)", rsc.reverb.ref())
}

// isUnderAddons reports whether dest is the addons directory itself or
// anything below it. Empty addons (a daemon that didn't send it) can't be
// checked, so nothing is refused.
func isUnderAddons(addons, dest string) bool {
	addons = strings.TrimSpace(addons)
	if addons == "" {
		return false
	}
	if path.Clean(addons) == path.Clean(dest) {
		return true
	}
	_, under := underPath(addons, dest)
	return under
}

// ensureRemoteDir creates dir when missing. The overlay is created by
// Reverb at env creation, so this only covers an older environment; a
// dry-run reports what it would do instead of creating anything.
func ensureRemoteDir(ctx context.Context, rsc remoteShellContext, dir string, dryRun bool) error {
	if remoteDirExists(ctx, rsc.sshHost, dir) {
		return nil
	}
	if dryRun {
		return nil
	}
	if _, err := runSSH(ctx, rsc.sshHost, "mkdir -p "+shellQuote(dir), nil); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}

// warnOverlayShadow flags the pushed modules that shadow a module deployed
// from git. The push still proceeds — shadowing is the intended mechanism —
// but the user should know the running code is now the overlay's copy.
//
// The comparison is the SERVER's to make: the interesting question is what
// the overlay hides in the deployed git tree, and the client has no local
// copy of that tree. So this asks GET /environments/{id}/overlay rather
// than probing paths.addons over SSH (which was also N round-trips).
//
// This is an advisory: the push already succeeded, so a failure to fetch
// it degrades to no warning rather than failing the command.
func warnOverlayShadow(ctx context.Context, rsc remoteShellContext, opts PushOpts, modules []string) {
	if rsc.reverb.id == 0 {
		return
	}
	client, err := reverbClientFor(opts.Cfg, rsc.reverb)
	if err != nil {
		return
	}
	ov, err := client.GetOverlay(ctx, rsc.reverb.id)
	if err != nil {
		opts.log("WARNING", "", "could not read the environment overlay report", rsc.prof.DBName,
			[2]string{"err", err.Error()})
		return
	}
	// Only what this push touched — the overlay may hold older modules the
	// user is not thinking about right now.
	pushed := moduleSet(modules)
	var shadowed []string
	for _, m := range ov.Shadowed {
		if pushed[m] {
			shadowed = append(shadowed, m)
		}
	}
	if len(shadowed) == 0 {
		return
	}
	opts.log("WARNING", "", "these modules now shadow the deployed copy — odoo loads the overlay's, not git's",
		rsc.prof.DBName,
		[2]string{"modules", strings.Join(shadowed, ",")},
		[2]string{"count", strconv.Itoa(len(shadowed))})
}

// runPushCleanReverb empties the environment's overlay: the Reverb-mode
// counterpart of reverting a git-deploy target's dirty overlay, run once
// the code it held has been committed and deployed by Reverb. The overlay
// is a plain directory, not a git checkout, so there is nothing to
// `git checkout --`/`git clean` — the modules are simply removed.
func runPushCleanReverb(ctx context.Context, opts PushOpts, p pushArgs, rsc remoteShellContext) error {
	overlay := strings.TrimSpace(rsc.reverb.paths.Overlay)
	if overlay == "" {
		return errNoOverlay(rsc)
	}
	present, err := listRemoteDirs(ctx, rsc.sshHost, overlay)
	if err != nil || len(present) == 0 {
		opts.log("INFO", "clean", "nothing to clean — the overlay is empty", rsc.prof.DBName,
			[2]string{"overlay", overlay})
		return nil
	}

	var scoped []string
	switch {
	case p.all:
		scoped = present
	case len(p.modules) > 0:
		scoped = intersectModules(present, moduleSet(p.modules))
		if len(scoped) == 0 {
			opts.log("INFO", "clean", "nothing to clean — none of those modules are in the overlay",
				rsc.prof.DBName, [2]string{"modules", strings.Join(p.modules, ",")})
			return nil
		}
	default:
		picked, perr := runFuzzyPicker("Modules to clean from the overlay of "+rsc.reverb.ref(), present, opts.Palette)
		if perr != nil {
			return perr
		}
		scoped = intersectModules(present, moduleSet(picked))
	}
	if len(scoped) == 0 {
		opts.log("INFO", "clean", "nothing to clean", rsc.prof.DBName)
		return nil
	}

	if p.dryRun {
		opts.log("INFO", "clean", "dry-run — nothing removed", rsc.prof.DBName,
			[2]string{"overlay", overlay}, [2]string{"modules", strings.Join(scoped, ",")})
		return nil
	}
	if err := confirmRemoteProd(opts.Palette, "push --clean", rsc, opts.Args); err != nil {
		return err
	}
	if !argsHaveForce(opts.Args) {
		if err := confirmPushClean(opts.Palette, rsc.prof.DBName, len(scoped)); err != nil {
			return err
		}
	}

	for _, m := range scoped {
		target := path.Join(overlay, m)
		// Guard against a picker/flag value that would escape the overlay.
		if _, ok := underPath(overlay, target); !ok {
			return fmt.Errorf("refusing to remove %s — outside the overlay", target)
		}
		if _, err := runSSH(ctx, rsc.sshHost, "rm -rf "+shellQuote(target), nil); err != nil {
			return fmt.Errorf("remove %s: %w", target, err)
		}
		opts.log("INFO", "clean", "removed from the overlay", rsc.prof.DBName, [2]string{"module", m})
	}
	updateDeployLock(ctx, rsc, opts.Log, func(l *DeployLock) { l.forget(scoped) })
	opts.log("INFO", "clean", "overlay cleaned", rsc.prof.DBName,
		[2]string{"modules", strconv.Itoa(len(scoped))})
	return nil
}

// moduleSet turns a module-name list into the keep set intersectModules
// filters by.
func moduleSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}
