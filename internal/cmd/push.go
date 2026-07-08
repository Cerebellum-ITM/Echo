package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// PushOpts configures a `push` run.
type PushOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log emits one Odoo-style progress line under `echo.push[.sub]`.
	Log func(level, sub, msg, db string, fields ...[2]string)
	// StreamOut receives any raw remote lines (unused by rsync, which is now
	// parsed into a change tree); kept for interface parity with other verbs.
	StreamOut func(string)
	// OnSync, when set, receives a module's parsed file changes so the caller
	// (the REPL) can render the change tree between the syncing/synced frame.
	OnSync func(changes []FileChange)
}

// log emits a progress line when a logger is set; a no-op otherwise.
func (o PushOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// pushArgs is the parsed shape of the push input.
type pushArgs struct {
	modules []string
	dirty   bool
	dryRun  bool
	del     bool
	from    string
	remote  bool
}

// parsePushArgs extracts the module positionals and flags. The remote-mode
// switches (`--from <t>` / `--from=t` / `--remote`) are consumed here so the
// value token after a bare `--from` is not read as a module; any other
// `-`-prefixed token errors.
func parsePushArgs(args []string) (pushArgs, error) {
	out := pushArgs{}
	out.from, out.remote = remoteFlagsIn(args)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dirty":
			out.dirty = true
		case a == "--dry-run":
			out.dryRun = true
		case a == "--delete":
			out.del = true
		case a == "--force":
			// consumed by confirmRemoteProd
		case a == "--from":
			i++ // skip the target value; captured by remoteFlagsIn
		case strings.HasPrefix(a, "--from="), a == "--remote":
			// consumed by remoteFlagsIn
		case strings.HasPrefix(a, "-"):
			return pushArgs{}, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			out.modules = append(out.modules, a)
		}
	}
	return out, nil
}

// RunPush rsyncs the selected local modules to the remote target's addons
// directory over SSH: resolve the target, gate prod, then sync each module
// in place (existing remote location) or mirrored at the local subpath.
func RunPush(ctx context.Context, opts PushOpts) error {
	if err := requireRsync(); err != nil {
		return err
	}
	p, err := parsePushArgs(opts.Args)
	if err != nil {
		return err
	}

	// Positionals are validated against the local repo before any SSH: a name
	// that isn't an addon here is a usage error, caught early (the deploy
	// --modules pattern).
	modules := append([]string(nil), p.modules...)
	for _, m := range modules {
		if _, derr := resolveModuleDir(opts.Cfg, opts.Root, m); derr != nil {
			return fmt.Errorf("%w: module %q is not an addon in %s (no __manifest__.py)", ErrUsage, m, opts.Root)
		}
	}
	if p.dirty {
		dirty, derr := gitDirtyModules(ctx, opts.Root)
		if derr != nil {
			return fmt.Errorf("--dirty: %w", derr)
		}
		modules = mergeModules(modules, dirtyNames(dirty))
	}

	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
	if err != nil {
		return err
	}

	// No modules named and none dirty → open the multi-select picker, tinted
	// by the remote profile's stage (TTY-guarded inside the picker core).
	if len(modules) == 0 {
		if p.dirty {
			opts.log("INFO", "", "nothing to push — no dirty modules", rsc.prof.DBName)
			return nil
		}
		picked, perr := pickModulesInteractive(ctx,
			ModulesOpts{Cfg: withStage(opts.Cfg, rsc.target.stage), Root: opts.Root, Palette: opts.Palette},
			"Modules to push", nil)
		if perr != nil {
			return perr
		}
		modules = picked
	}

	if !p.dryRun {
		if err := confirmRemoteProd(opts.Palette, "push", rsc, opts.Args); err != nil {
			return err
		}
	}

	files, err := pushModuleSet(ctx, rsc, opts, modules, opts.Root, p.dryRun, p.del)
	if err != nil {
		return err
	}
	verb := "push complete"
	if p.dryRun {
		verb = "dry-run — nothing transferred"
	}
	opts.log("INFO", "", verb, rsc.prof.DBName,
		[2]string{"modules", strconv.Itoa(len(modules))},
		[2]string{"files", strconv.Itoa(files)})
	return nil
}

// FileChange is one file rsync would (or did) transfer for a module, with a
// coarse operation: "new" (created), "changed" (updated), or "deleted".
type FileChange struct {
	Op   string
	Path string // module-relative, e.g. "data/mail_template_data.xml"
}

// pushModuleSet syncs each module to the remote target, reading the source
// files from srcRoot (the project root for a manual push, the archive
// scratch dir for the watcher). Returns the total number of changed files.
// Shared by `push`, `deploy --push`, and `watch`. A greppable syncing/synced
// log frame brackets each module; opts.OnSync (when set) receives the file
// list so the caller can render the change tree between them.
func pushModuleSet(ctx context.Context, rsc remoteShellContext, opts PushOpts, modules []string, srcRoot string, dryRun, del bool) (int, error) {
	rv := remoteView{rsc: rsc}
	total := 0
	for _, m := range modules {
		srcDir, err := moduleSrcDir(opts.Cfg, srcRoot, m)
		if err != nil {
			return total, fmt.Errorf("module %q: %w", m, err)
		}
		destDir, err := pushDest(ctx, rv, opts, m)
		if err != nil {
			return total, err
		}
		opts.log("INFO", "module", "syncing", rsc.prof.DBName,
			[2]string{"module", m}, [2]string{"dest", destDir})
		changes, err := rsyncModule(ctx, srcDir, rsc.sshHost, destDir, dryRun, del)
		if err != nil {
			return total, fmt.Errorf("rsync %q: %w", m, err)
		}
		if opts.OnSync != nil {
			opts.OnSync(changes)
		}
		total += len(changes)
		newN, chgN, delN := countChanges(changes)
		fields := [][2]string{{"module", m}, {"new", strconv.Itoa(newN)}, {"changed", strconv.Itoa(chgN)}}
		if delN > 0 {
			fields = append(fields, [2]string{"deleted", strconv.Itoa(delN)})
		}
		opts.log("INFO", "module", "synced", rsc.prof.DBName, fields...)
	}
	return total, nil
}

// countChanges tallies a change slice by operation.
func countChanges(changes []FileChange) (newN, chgN, delN int) {
	for _, c := range changes {
		switch c.Op {
		case "new":
			newN++
		case "changed":
			chgN++
		case "deleted":
			delN++
		}
	}
	return newN, chgN, delN
}

// probeRemoteBase / probeRemoteDir are the SSH-facing seams pushDest uses,
// overridable in tests so destination resolution is unit-testable without a
// live remote.
var (
	probeRemoteBase = remoteModuleBase
	probeRemoteDir  = remoteDirExists
)

// pushDest resolves where a module's files land on the remote host
// filesystem. The destination is decided by the REMOTE layout, never the
// local working directory — so `push` sends to the same place regardless of
// whether it runs from the project root or from inside addons/:
//
//  1. If the module already lives in a real remote addons subdir, update it
//     in place.
//  2. Otherwise place it in the remote's addons directory: the first
//     candidate (the profile's relative addons paths, else addons/custom)
//     that actually exists under remotePath.
//
// A module is never written at the compose-project root (base "."), and a
// container-only (conf-mode) remote fails closed.
func pushDest(ctx context.Context, rv remoteView, opts PushOpts, module string) (string, error) {
	base, inContainer, err := probeRemoteBase(ctx, rv, module)
	if err == nil {
		if inContainer {
			return "", fmt.Errorf("remote addons are container-internal — push needs a host checkout")
		}
		// base "." means it was found at the docker root — almost always a
		// prior mis-push. Ignore it and re-home the module in a real addons dir.
		if base != "." {
			return remoteModuleDir(rv, base, module, false), nil
		}
	}

	candidates := remoteAddonsCandidates(rv.rsc.prof.AddonsPaths)
	for _, b := range candidates {
		dir := path.Join(rv.rsc.remotePath, b)
		if probeRemoteDir(ctx, rv.rsc.sshHost, dir) {
			return path.Join(dir, module), nil
		}
	}
	return "", fmt.Errorf("no addons directory found under %s on the remote (tried: %s)",
		rv.rsc.remotePath, strings.Join(candidates, ", "))
}

// remoteAddonsCandidates lists the remote addons subdirectories a new module
// may land in: the profile's relative addons paths, else the conventional
// addons/custom. Absolute (container) paths and the docker root (".") are
// never candidates — a module must never be written at the compose-project
// root, and rsync can't target a path inside the image.
func remoteAddonsCandidates(profPaths []string) []string {
	var out []string
	for _, b := range profPaths {
		if b == "" || b == "." || path.IsAbs(b) {
			continue
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		out = []string{"addons", "custom"}
	}
	return out
}

// rsyncCommand builds the rsync invocation. A package-level hook so tests can
// substitute a local command for the real rsync binary.
var rsyncCommand = func(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "rsync", args...)
}

// rsyncArgs builds the rsync argv: archive + itemized changes, the shared
// exclude set (build/VCS noise, mirroring skipViewPath), optional dry-run
// (`-n`) and `--delete`, and a trailing slash on both endpoints so the
// module's contents map into the remote module dir.
//
// `--checksum` makes rsync decide by CONTENT, not size+mtime: the watcher
// ships each commit from a `git archive`, which stamps every file with the
// commit's mtime, so a size+mtime comparison would re-sync the whole module
// on every commit. With --checksum only genuinely content-changed files are
// transferred (and, paired with parseItemize dropping attribute-only lines,
// only those show in the change tree).
func rsyncArgs(srcDir, sshHost, destDir string, dryRun, del bool) []string {
	args := []string{
		"-az", "--checksum", "--itemize-changes",
		"--exclude", "__pycache__", "--exclude", "*.pyc", "--exclude", ".git",
		"-e", "ssh -o BatchMode=yes",
	}
	if dryRun {
		args = append(args, "-n")
	}
	if del {
		args = append(args, "--delete")
	}
	args = append(args, withTrailingSlash(srcDir), sshHost+":"+withTrailingSlash(destDir))
	return args
}

// rsyncModule runs one module's rsync and parses its itemized output into a
// typed change list (rather than dumping rsync's cryptic `<f+++++++++` codes).
// stderr is folded into the error on failure.
func rsyncModule(ctx context.Context, srcDir, sshHost, destDir string, dryRun, del bool) ([]FileChange, error) {
	c := rsyncCommand(ctx, rsyncArgs(srcDir, sshHost, destDir, dryRun, del)...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}

	var (
		mu          sync.Mutex
		changes     []FileChange
		lastErrLine string
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if fc, ok := parseItemize(sc.Text()); ok {
				mu.Lock()
				changes = append(changes, fc)
				mu.Unlock()
			}
		}
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if line := sc.Text(); strings.TrimSpace(line) != "" {
				mu.Lock()
				lastErrLine = line
				mu.Unlock()
			}
		}
	}()
	wg.Wait()

	if err := c.Wait(); err != nil {
		if s := strings.TrimSpace(lastErrLine); s != "" {
			return nil, fmt.Errorf("%w: %s", err, s)
		}
		return nil, err
	}
	return changes, nil
}

// parseItemize turns one rsync `--itemize-changes` line into a FileChange.
// The itemize code is `YXcstpoguax`: Y = update type, X = file type, then 9
// attribute flags. Directory entries, symlinks, and non-item noise
// ("created directory …") return ok=false — the tree groups by directory
// itself. A new file has all-`+` flags; a deletion is the `*deleting` form.
func parseItemize(line string) (FileChange, bool) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return FileChange{}, false
	}
	if strings.HasPrefix(line, "*deleting") {
		p := strings.TrimSpace(strings.TrimPrefix(line, "*deleting"))
		if p == "" || strings.HasSuffix(p, "/") {
			return FileChange{}, false // whole-dir deletions aren't listed
		}
		return FileChange{Op: "deleted", Path: p}, true
	}
	sp := strings.IndexByte(line, ' ')
	if sp < 2 {
		return FileChange{}, false
	}
	code, p := line[:sp], line[sp+1:]
	if len(code) < 2 || code[1] != 'f' { // only regular files (X == 'f')
		return FileChange{}, false
	}
	// The update type Y (code[0]) is '.' when nothing was transferred — only
	// attributes (e.g. mtime) changed. Those aren't content changes: skip them
	// so a re-stamped-but-identical file never appears as a change.
	if code[0] == '.' {
		return FileChange{}, false
	}
	op := "changed"
	if flags := code[2:]; len(flags) > 0 && strings.Trim(flags, "+") == "" {
		op = "new" // all-`+` attribute flags → freshly created
	}
	return FileChange{Op: op, Path: p}, true
}

// SyncRow is one rendered line of the change tree: a tree connector prefix, an
// optional operation glyph, the file/dir name, and a kind the caller colors
// by (new/changed/deleted/dir).
type SyncRow struct {
	Prefix string
	Glyph  string
	Name   string
	Kind   string
}

// glyphForOp maps a change operation to its tree glyph.
func glyphForOp(op string) string {
	switch op {
	case "new":
		return "+"
	case "changed":
		return "~"
	case "deleted":
		return "−"
	}
	return ""
}

// BuildSyncTree groups a module's changes into a directory tree: root files
// first, then each subdirectory (by full relative path) with its files. Pure
// and deterministic — the caller renders the returned rows with color.
func BuildSyncTree(changes []FileChange) []SyncRow {
	var rootFiles []FileChange
	dirs := map[string][]FileChange{}
	var dirOrder []string
	for _, c := range changes {
		d := path.Dir(c.Path)
		if d == "." {
			rootFiles = append(rootFiles, c)
			continue
		}
		if _, ok := dirs[d]; !ok {
			dirOrder = append(dirOrder, d)
		}
		dirs[d] = append(dirs[d], c)
	}
	sort.Slice(rootFiles, func(i, j int) bool { return rootFiles[i].Path < rootFiles[j].Path })
	sort.Strings(dirOrder)

	// Top-level entries: root files (leaves), then directory groups.
	type entry struct {
		dir  string // "" for a root-file leaf
		file FileChange
	}
	var entries []entry
	for _, f := range rootFiles {
		entries = append(entries, entry{file: f})
	}
	for _, d := range dirOrder {
		entries = append(entries, entry{dir: d})
	}

	var rows []SyncRow
	for i, e := range entries {
		last := i == len(entries)-1
		conn := "├─ "
		if last {
			conn = "└─ "
		}
		if e.dir == "" {
			rows = append(rows, SyncRow{Prefix: conn, Glyph: glyphForOp(e.file.Op), Name: path.Base(e.file.Path), Kind: e.file.Op})
			continue
		}
		rows = append(rows, SyncRow{Prefix: conn, Name: e.dir + "/", Kind: "dir"})
		childPre := "│    "
		if last {
			childPre = "     "
		}
		files := dirs[e.dir]
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		for _, f := range files {
			rows = append(rows, SyncRow{Prefix: childPre, Glyph: glyphForOp(f.Op), Name: path.Base(f.Path), Kind: f.Op})
		}
	}
	return rows
}

// requireRsync fails closed with a clear message when rsync isn't on PATH.
func requireRsync() error {
	if _, err := lookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found on PATH — install it to use push")
	}
	return nil
}

// moduleSrcDir returns the absolute local module directory under srcRoot,
// found by the same addons-path search resolveModuleDir uses.
func moduleSrcDir(cfg *config.Config, srcRoot, module string) (string, error) {
	dir, err := resolveModuleDir(cfg, srcRoot, module)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, module), nil
}

// localAddonsSubpath returns the module's addons directory as a
// root-relative, slash-separated path (e.g. "addons", "."), for mirroring
// the local layout onto a remote that doesn't have the module yet.
func localAddonsSubpath(cfg *config.Config, root, module string) (string, error) {
	dir, err := resolveModuleDir(cfg, root, module)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// remoteDirExists reports whether dir exists on the remote host.
func remoteDirExists(ctx context.Context, sshHost, dir string) bool {
	_, err := runSSH(ctx, sshHost, "test -d "+shellQuote(dir), nil)
	return err == nil
}

// withTrailingSlash ensures a single trailing slash so rsync copies a
// directory's contents rather than the directory itself.
func withTrailingSlash(p string) string {
	return strings.TrimRight(p, "/") + "/"
}

// dirtyNames extracts the module names from a dirtyModule slice.
func dirtyNames(dirty []dirtyModule) []string {
	out := make([]string, len(dirty))
	for i, d := range dirty {
		out[i] = d.name
	}
	return out
}

// mergeModules unions two module-name slices, preserving order and dropping
// duplicates.
func mergeModules(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string(nil), a...), b...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// withStage returns a shallow copy of cfg with Stage overridden, so a picker
// can be tinted by the remote profile's stage without mutating the session
// config.
func withStage(cfg *config.Config, stage string) *config.Config {
	c := *cfg
	if stage != "" {
		c.Stage = stage
	}
	return &c
}
