package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/config"
)

// Synthetic rows in the remote directory picker.
const (
	dirPickerUse = "· use this directory"
	dirPickerUp  = ".. (up)"
)

// runSetDest implements `push --set-dest`: resolve the remote target, pick
// (or take via --dest) the destination directory, and persist it to the
// local [push] path — no modules, no rsync, no prod gate. Storing the path
// relative when it falls under remotePath keeps the profile portable.
func runSetDest(ctx context.Context, opts PushOpts, p pushArgs) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
	if err != nil {
		return err
	}
	var dest string
	if p.dest != "" {
		dest, err = prepareExplicitDest(ctx, rsc, p.dest, p.mkdir, false)
	} else {
		dest, err = pickRemoteDir(ctx, rsc, opts, rsc.remotePath)
	}
	if err != nil {
		return err
	}
	stored := dest
	if rel, ok := underPath(rsc.remotePath, dest); ok {
		stored = rel
	}
	cfgCopy := *opts.Cfg
	cfgCopy.PushPath = stored
	if p.mkdir {
		t := true
		cfgCopy.PushMkdir = &t
	}
	if serr := config.SaveProject(&cfgCopy); serr != nil {
		return fmt.Errorf("save push destination: %w", serr)
	}
	opts.Cfg.PushPath = stored
	if p.mkdir {
		opts.Cfg.PushMkdir = cfgCopy.PushMkdir
	}
	opts.log("INFO", "", "push destination set", rsc.prof.DBName,
		[2]string{"path", stored}, [2]string{"source", "set-dest"})
	return nil
}

// resolvePushDest applies the destination precedence — `--dest` flag ›
// server `[push] path` › local `[push] path` › none — and returns the
// winning path (still un-joined/un-validated), the source label for the
// log line, and the merged mkdir policy (flag `--mkdir` OR the winning
// side's mkdir). An empty dest means auto-detect. Pure: no SSH.
func resolvePushDest(p pushArgs, prof config.RemoteProfile, cfg *config.Config) (dest, source string, mkdir bool) {
	switch {
	case p.dest != "":
		return p.dest, "flag", p.mkdir
	case prof.PushPath != "":
		return prof.PushPath, "server", p.mkdir || boolVal(prof.PushMkdir)
	case cfg != nil && cfg.PushPath != "":
		return cfg.PushPath, "local", p.mkdir || boolVal(cfg.PushMkdir)
	}
	return "", "", p.mkdir
}

// boolVal dereferences an optional bool (nil → false).
func boolVal(b *bool) bool { return b != nil && *b }

// resolvePushDestination turns the parsed push flags into the resolved
// remote base directory every module lands under, or "" for the legacy
// per-module auto-detect. Order: `--pick-dest` → explicit (flag/config)
// → auto-detect, with a TTY picker fallback when auto-detect can't find a
// place to write (container-internal remote / no addons dir).
func resolvePushDestination(ctx context.Context, rsc remoteShellContext, opts PushOpts, p pushArgs, modules []string) (string, error) {
	rv := remoteView{rsc: rsc}
	if p.pickDest {
		return pickAndMaybePersist(ctx, rsc, opts, rv)
	}
	dest, source, mkdir := resolvePushDest(p, rsc.prof, opts.Cfg)
	if dest != "" {
		return applyResolvedDest(ctx, rsc, opts, dest, source, mkdir, p.dryRun)
	}
	// Auto-detect: probe with the first module. If it can't resolve a target
	// and we have a TTY, fall into the picker instead of failing closed.
	if len(modules) > 0 {
		if _, derr := pushDest(ctx, rv, opts, modules[0]); derr != nil {
			if stdinIsTTY() {
				opts.log("WARNING", "", "auto-detect found no addons dir — pick a destination", rsc.prof.DBName,
					[2]string{"reason", derr.Error()})
				return pickAndMaybePersist(ctx, rsc, opts, rv)
			}
			return "", derr
		}
	}
	return "", nil // per-module auto-detect in pushModuleSet
}

// applyResolvedDest validates/joins an explicit destination, probes (and,
// with mkdir, creates) it on the remote, logs the resolution, and returns
// the absolute remote path.
func applyResolvedDest(ctx context.Context, rsc remoteShellContext, opts PushOpts, dest, source string, mkdir, dryRun bool) (string, error) {
	resolved, err := prepareExplicitDest(ctx, rsc, dest, mkdir, dryRun)
	if err != nil {
		return "", err
	}
	opts.log("INFO", "", "using explicit destination", rsc.prof.DBName,
		[2]string{"dest", resolved}, [2]string{"source", source})
	return resolved, nil
}

// prepareExplicitDest resolves dest against the remote path (relative →
// joined, absolute → as-is), rejects the compose root, and ensures the
// directory exists (creating it under mkdir). In dry-run nothing is
// created — a missing dir without mkdir still errors so the run fails the
// same way it would live.
func prepareExplicitDest(ctx context.Context, rsc remoteShellContext, dest string, mkdir, dryRun bool) (string, error) {
	resolved := resolveDestPath(rsc.remotePath, dest)
	if resolved == "" {
		return "", fmt.Errorf("%w: push destination cannot be the compose project root", ErrUsage)
	}
	if remoteDirExists(ctx, rsc.sshHost, resolved) {
		return resolved, nil
	}
	if !mkdir {
		return "", fmt.Errorf("remote destination %s does not exist (pass --mkdir to create it, or set [push] mkdir = true)", resolved)
	}
	if !dryRun {
		if _, err := runSSH(ctx, rsc.sshHost, "mkdir -p "+shellQuote(resolved), nil); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", resolved, err)
		}
	}
	return resolved, nil
}

// resolveDestPath cleans a destination path: an absolute one is used
// as-is, a relative one is joined under remotePath. The compose root
// ("."/""/"/") is rejected (returns "") — a module must never be written
// at the project root.
func resolveDestPath(remotePath, dest string) string {
	d := path.Clean(strings.TrimSpace(dest))
	if d == "." || d == "" || d == "/" {
		return ""
	}
	if path.IsAbs(d) {
		return d
	}
	return path.Join(remotePath, d)
}

// pickAndMaybePersist runs the remote directory picker starting at the
// compose root, then offers to persist the choice as the project's local
// [push] path so the next push is non-interactive.
func pickAndMaybePersist(ctx context.Context, rsc remoteShellContext, opts PushOpts, rv remoteView) (string, error) {
	picked, err := pickRemoteDir(ctx, rsc, opts, rsc.remotePath)
	if err != nil {
		return "", err
	}
	if confirmPersistDest(opts) {
		stored := picked
		if rel, ok := underPath(rsc.remotePath, picked); ok {
			stored = rel
		}
		cfgCopy := *opts.Cfg
		cfgCopy.PushPath = stored
		if serr := config.SaveProject(&cfgCopy); serr != nil {
			opts.log("WARNING", "", "could not save push destination", rsc.prof.DBName,
				[2]string{"err", serr.Error()})
		} else {
			opts.Cfg.PushPath = stored
			opts.log("INFO", "", "saved push destination", rsc.prof.DBName,
				[2]string{"path", stored})
		}
	}
	return picked, nil
}

// pickRemoteDir browses the remote host filesystem level by level over the
// shared fuzzy picker (stage-tinted), starting at `start`. It returns the
// selected absolute directory; the compose root is rejected in place.
func pickRemoteDir(ctx context.Context, rsc remoteShellContext, opts PushOpts, start string) (string, error) {
	if err := requireTTY("pass --dest <path> instead"); err != nil {
		return "", err
	}
	cur := path.Clean(start)
	if cur == "" {
		cur = "/"
	}
	for {
		dirs, err := listRemoteDirs(ctx, rsc.sshHost, cur)
		if err != nil {
			return "", fmt.Errorf("list %s: %w", cur, err)
		}
		choice, err := runSingleFuzzyPickerStaged("Push destination: "+cur,
			dirPickerEntries(cur, dirs), opts.Palette, rsc.target.stage)
		if err != nil {
			return "", err // ErrCancelled / ErrQuit propagate
		}
		switch choice {
		case dirPickerUse:
			if path.Clean(cur) == path.Clean(rsc.remotePath) {
				opts.log("WARNING", "", "cannot push to the compose project root — pick a subdirectory", rsc.prof.DBName)
				continue
			}
			return cur, nil
		case dirPickerUp:
			cur = path.Dir(cur)
			if cur == "" {
				cur = "/"
			}
		default:
			cur = path.Join(cur, choice)
		}
	}
}

// dirPickerEntries builds the picker row set for a level: the "use this
// directory" action, an "up" row (except at "/"), then the subdirectories.
func dirPickerEntries(cur string, dirs []string) []string {
	entries := []string{dirPickerUse}
	if path.Clean(cur) != "/" {
		entries = append(entries, dirPickerUp)
	}
	return append(entries, dirs...)
}

// listRemoteDirs returns the immediate subdirectory names of dir on the
// remote host. A package var so tests can stub the SSH call.
var listRemoteDirs = func(ctx context.Context, sshHost, dir string) ([]string, error) {
	out, err := runSSH(ctx, sshHost,
		"find "+shellQuote(dir)+" -maxdepth 1 -mindepth 1 -type d 2>/dev/null", nil)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			dirs = append(dirs, path.Base(l))
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// underPath reports whether p is strictly below base, returning the
// base-relative remainder. Equal paths (the compose root itself) are not
// "under".
func underPath(base, p string) (string, bool) {
	base, p = path.Clean(base), path.Clean(p)
	if p == base {
		return "", false
	}
	if strings.HasPrefix(p, base+"/") {
		return strings.TrimPrefix(p, base+"/"), true
	}
	return "", false
}

// confirmPersistDest asks whether to save the picked destination. Non-TTY
// (shouldn't happen — the picker is TTY-gated) declines silently.
func confirmPersistDest(opts PushOpts) bool {
	if !stdinIsTTY() {
		return false
	}
	save := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Save as this project's push destination?").
			Description("The next push reuses it without the picker.").
			Affirmative("Save").
			Negative("Just this run").
			Value(&save),
	)).
		WithTheme(BuildHuhTheme(opts.Palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return false
	}
	return save
}
