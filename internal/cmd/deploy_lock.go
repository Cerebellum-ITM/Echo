package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

// DeployLock is the record of the code Echo shipped to one target. It lives on
// the target itself, at <remote_path>/.echo/lock.json, so every machine that
// deploys there and anyone reading it over SSH get the same answer to "which
// version of this module is running, and where did it come from?".
type DeployLock struct {
	Schema  int                   `json:"schema"`
	Target  string                `json:"target,omitempty"`
	Base    *LockBase             `json:"base,omitempty"`
	Modules map[string]LockModule `json:"modules"`
}

// LockBase is a git-deploy target's deploy branch, mirroring the Unit 113
// `echo.deployed-*` keys of the checkout.
type LockBase struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref,omitempty"`
	At     string `json:"at"`
}

// LockModule is the content one module last shipped with. SHA is the commit
// the content belongs to (for a worktree source, the local HEAD at push time),
// Tree the module directory's git tree id at that commit, and Verified whether
// Odoo has since run -u on it successfully.
type LockModule struct {
	Source   string `json:"source"`
	Ref      string `json:"ref,omitempty"`
	SHA      string `json:"sha,omitempty"`
	Tree     string `json:"tree,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	Version  string `json:"version,omitempty"`
	Dest     string `json:"dest,omitempty"`
	Via      string `json:"via"`
	At       string `json:"at"`
	By       string `json:"by,omitempty"`
	Verified bool   `json:"verified"`
}

const (
	lockSourceWorktree = "worktree"
	lockSourceCommit   = "commit"
	lockSourceBranch   = "branch"

	lockSchema  = 1
	lockDirName = ".echo"
)

// lockRunSSH carries every lock read and write; a seam so tests can keep the
// remote file in memory.
var lockRunSSH = runSSH

func newDeployLock(target string) DeployLock {
	return DeployLock{Schema: lockSchema, Target: target, Modules: map[string]LockModule{}}
}

func lockFilePath(remotePath string) string {
	return path.Join(remotePath, lockDirName, "lock.json")
}

// label renders where an entry's content came from: `worktree@0b6fc41+dirty`.
func (m LockModule) label() string {
	s := m.Source
	if m.SHA != "" {
		s += "@" + shortSHA(m.SHA)
	}
	if m.Dirty {
		s += "+dirty"
	}
	return s
}

func (l *DeployLock) record(entries map[string]LockModule) {
	for name, e := range entries {
		l.Modules[name] = e
	}
}

func (l *DeployLock) markVerified(shipped map[string]LockModule) {
	for name := range shipped {
		if e, ok := l.Modules[name]; ok {
			e.Verified = true
			l.Modules[name] = e
		}
	}
}

func (l *DeployLock) forget(names []string) {
	for _, name := range names {
		delete(l.Modules, name)
	}
}

// rebase records a move of the whole deploy branch. The branch entries it
// supersedes go away; overlay entries survive only when the overlay did.
func (l *DeployLock) rebase(base LockBase, keepOverlay bool) {
	l.Base = &base
	for name, e := range l.Modules {
		if e.Source == lockSourceBranch || !keepOverlay {
			delete(l.Modules, name)
		}
	}
}

func sortedLockModules(l DeployLock) []string {
	names := make([]string, 0, len(l.Modules))
	for name := range l.Modules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// readDeployLock reads the target's lock; found is false when it has none. An
// unreadable or unparsable file warns and reads as a fresh lock, so the next
// write replaces it instead of every deploy failing on it.
func readDeployLock(ctx context.Context, rsc remoteShellContext, log logFn) (lock DeployLock, found bool) {
	lock = newDeployLock(rsc.fromName)
	if rsc.remotePath == "" {
		return lock, false
	}
	out, err := lockRunSSH(ctx, rsc.sshHost, "cat "+shellQuote(lockFilePath(rsc.remotePath))+" 2>/dev/null || true", nil)
	if err != nil {
		ckptLog(log, "WARNING", "lock", "could not read the deploy lock", rsc.prof.DBName,
			[2]string{"reason", err.Error()})
		return lock, false
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return lock, false
	}
	var parsed DeployLock
	if err := json.Unmarshal(out, &parsed); err != nil {
		ckptLog(log, "WARNING", "lock", "deploy lock is unreadable — the next write replaces it", rsc.prof.DBName,
			[2]string{"reason", err.Error()})
		return lock, false
	}
	if parsed.Modules == nil {
		parsed.Modules = map[string]LockModule{}
	}
	return parsed, true
}

// writeDeployLock replaces the target's lock. Metadata, never a gate: a failed
// write warns and whatever the run did stands.
func writeDeployLock(ctx context.Context, rsc remoteShellContext, lock DeployLock, log logFn) {
	if rsc.remotePath == "" {
		return
	}
	lock.Schema = lockSchema
	if rsc.fromName != "" {
		lock.Target = rsc.fromName
	}
	body, _ := json.MarshalIndent(lock, "", "  ")
	out, err := lockRunSSH(ctx, rsc.sshHost, lockWriteScript(rsc.remotePath), append(body, '\n'))
	if err != nil {
		ckptLog(log, "WARNING", "lock", "could not write the deploy lock", rsc.prof.DBName,
			[2]string{"reason", err.Error()})
		return
	}
	if tracked := nonEmptyLines(string(out)); len(tracked) > 0 {
		ckptLog(log, "WARNING", "lock", "the server's git repository tracks .echo/ — untrack it there with `git rm --cached -r .echo`",
			rsc.prof.DBName, [2]string{"files", strings.Join(tracked, ",")})
	}
}

// lockWriteScript writes stdin to the lock through a temp file and mv. The
// `*` .gitignore makes .echo/ ignore itself in any repository that contains
// it without touching the repository; the trailing ls-files reports the one
// case that cannot cover — paths under .echo/ the repository already tracks.
func lockWriteScript(remotePath string) string {
	dir := shellQuote(path.Join(remotePath, lockDirName))
	return "mkdir -p " + dir +
		" && { [ -f " + dir + "/.gitignore ] || printf '*\\n' > " + dir + "/.gitignore; }" +
		" && cat > " + dir + "/lock.json.tmp && mv " + dir + "/lock.json.tmp " + dir + "/lock.json" +
		" && { cd " + shellQuote(remotePath) + " && git ls-files -- " + lockDirName + " 2>/dev/null || true; }"
}

func updateDeployLock(ctx context.Context, rsc remoteShellContext, log logFn, change func(*DeployLock)) {
	lock, _ := readDeployLock(ctx, rsc, log)
	change(&lock)
	writeDeployLock(ctx, rsc, lock, log)
}

// shipSource is where the content of a set of modules comes from. sha may be
// empty for a worktree source (the local HEAD is read); srcRoot is the local
// directory the push reads, unused for a branch source.
type shipSource struct {
	kind    string
	sha     string
	ref     string
	srcRoot string
	via     string
}

// lockEntries describes what shipping modules from src puts on the target.
// Every field is best-effort: one that cannot be read is left out rather than
// failing the deploy that is about to happen.
func lockEntries(ctx context.Context, cfg *config.Config, root string, src shipSource, modules []string) map[string]LockModule {
	entries := make(map[string]LockModule, len(modules))
	if len(modules) == 0 {
		return entries
	}
	now := time.Now().UTC().Format(time.RFC3339)
	by := ""
	if out, err := gitOutput(ctx, root, "config", "user.email"); err == nil {
		by = firstLine(string(out))
	}
	sha := src.sha
	dirty := map[string]bool{}
	if src.kind == lockSourceWorktree {
		if sha == "" {
			if out, err := gitOutput(ctx, root, "rev-parse", "HEAD"); err == nil {
				sha = firstLine(string(out))
			}
		}
		if dms, err := gitDirtyModules(ctx, src.srcRoot); err == nil {
			for _, dm := range dms {
				dirty[dm.name] = true
			}
		}
	}

	for _, m := range modules {
		e := LockModule{Source: src.kind, Ref: src.ref, SHA: sha, Via: src.via, At: now, By: by}
		pathRoot := src.srcRoot
		if src.kind == lockSourceBranch {
			pathRoot = root
		}
		repoPath, err := moduleRepoPath(cfg, pathRoot, m)
		if err == nil {
			e.Dirty = dirty[m]
			if src.kind == lockSourceBranch {
				e.Version = versionAt(ctx, root, sha, repoPath)
			} else if b, rerr := os.ReadFile(filepath.Join(src.srcRoot, repoPath, "__manifest__.py")); rerr == nil {
				e.Version = manifestVersion(string(b))
			}
			if src.kind != lockSourceWorktree && sha != "" {
				if out, terr := gitOutput(ctx, root, "rev-parse", sha+":"+repoPath); terr == nil {
					e.Tree = firstLine(string(out))
				}
			}
		}
		entries[m] = e
	}
	return entries
}

// deployShipEntries describes the code a deploy ships: the modules riding the
// git deploy branch (which also becomes the lock base) and the ones the push
// rsyncs, from the watcher's archive or from the working tree.
func deployShipEntries(ctx context.Context, opts DeployOpts, g gitDeployConfig, gitTip string, branchMods, pushMods []string) (map[string]LockModule, *LockBase) {
	via := opts.Via
	if via == "" {
		via = "deploy"
	}
	shipped := map[string]LockModule{}
	var base *LockBase
	if gitTip != "" {
		ref := localBranchName(ctx, opts.Root)
		shipped = lockEntries(ctx, opts.Cfg, opts.Root,
			shipSource{kind: lockSourceBranch, sha: gitTip, ref: ref, via: via}, branchMods)
		base = &LockBase{Branch: g.branch, SHA: gitTip, Ref: ref, At: time.Now().UTC().Format(time.RFC3339)}
	}
	src := shipSource{kind: lockSourceWorktree, srcRoot: opts.Root, via: via}
	if opts.PushSrcRoot != "" {
		src = shipSource{kind: lockSourceCommit, sha: opts.PushSrcSHA, srcRoot: opts.PushSrcRoot, via: via}
	}
	for name, e := range lockEntries(ctx, opts.Cfg, opts.Root, src, pushMods) {
		shipped[name] = e
	}
	return shipped, base
}

// setLockDests fills in the remote directory each pushed module landed in.
func setLockDests(entries map[string]LockModule, dests map[string]string) {
	for m, d := range dests {
		if e, ok := entries[m]; ok {
			e.Dest = d
			entries[m] = e
		}
	}
}

// versionAt reads a module's manifest version as committed at sha.
func versionAt(ctx context.Context, root, sha, repoPath string) string {
	out, err := gitOutput(ctx, root, "show", sha+":"+repoPath+"/__manifest__.py")
	if err != nil {
		return ""
	}
	return manifestVersion(string(out))
}

// moduleRepoPath is the module's directory relative to root, slash-separated
// ("addons/sale", or "sale" for a module at the root).
func moduleRepoPath(cfg *config.Config, root, module string) (string, error) {
	sub, err := localAddonsSubpath(cfg, root, module)
	if err != nil {
		return "", err
	}
	if sub == "." || sub == "" {
		return module, nil
	}
	return sub + "/" + module, nil
}

// logCodePlan prints, per module about to ship, what ships next to what the
// lock says the target holds now.
func logCodePlan(log logFn, db string, shipped map[string]LockModule, current DeployLock) {
	names := make([]string, 0, len(shipped))
	for name := range shipped {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e := shipped[name]
		fields := [][2]string{{"module", name}, {"ship", e.label()}}
		if e.Version != "" {
			fields = append(fields, [2]string{"version", e.Version})
		}
		if prev, ok := current.Modules[name]; ok {
			fields = append(fields, [2]string{"locked", prev.label()})
			if prev.Version != "" {
				fields = append(fields, [2]string{"locked_version", prev.Version})
			}
		} else {
			fields = append(fields, [2]string{"locked", "none"})
		}
		ckptLog(log, "INFO", "plan", "code", db, fields...)
	}
}

// runDeployLockShow prints the target's lock (deploy --lock): read-only, no
// selection, no remote change.
func runDeployLockShow(ctx context.Context, opts DeployOpts, p deployArgs) (DeployResult, error) {
	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, logFn)
	if err != nil {
		return DeployResult{}, err
	}
	db := rsc.prof.DBName
	lock, found := readDeployLock(ctx, rsc, logFn)
	res := DeployResult{Target: rsc.fromName, DB: db, Lock: &lock, JSON: p.jsonOut}
	if !found {
		opts.log("INFO", "lock", "no deploy lock on this target", db)
		return res, nil
	}
	if b := lock.Base; b != nil {
		fields := [][2]string{{"branch", b.Branch}, {"sha", shortSHA(b.SHA)}}
		if b.Ref != "" {
			fields = append(fields, [2]string{"ref", b.Ref})
		}
		opts.log("INFO", "lock", "base", db, append(fields, [2]string{"at", b.At})...)
	}
	for _, name := range sortedLockModules(lock) {
		e := lock.Modules[name]
		fields := [][2]string{{"name", name}, {"source", e.label()}}
		if e.Version != "" {
			fields = append(fields, [2]string{"version", e.Version})
		}
		fields = append(fields, [2]string{"at", e.At}, [2]string{"verified", strconv.FormatBool(e.Verified)})
		opts.log("INFO", "lock", "module", db, fields...)
	}
	return res, nil
}

// reportDeployLock adds the lock summary to `link --show`. A target without a
// lock prints nothing.
func reportDeployLock(ctx context.Context, opts LinkOpts, db string) {
	rsc := remoteShellContext{sshHost: opts.Cfg.ConnectSSHHost, remotePath: opts.Cfg.ConnectRemotePath}
	lock, found := readDeployLock(ctx, rsc, nil)
	if !found {
		return
	}
	unverified, last := 0, ""
	var overlay []string
	for _, name := range sortedLockModules(lock) {
		e := lock.Modules[name]
		if !e.Verified {
			unverified++
		}
		if e.At > last {
			last = e.At
		}
		if lock.Base != nil && e.Source != lockSourceBranch {
			overlay = append(overlay, name)
		}
	}
	fields := [][2]string{
		{"modules", strconv.Itoa(len(lock.Modules))},
		{"unverified", strconv.Itoa(unverified)},
	}
	if last != "" {
		fields = append(fields, [2]string{"last", last})
	}
	if len(overlay) > 0 {
		fields = append(fields, [2]string{"overlay", strings.Join(overlay, ",")})
	}
	opts.log("INFO", "", "deploy lock", db, fields...)
}
