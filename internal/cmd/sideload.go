package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// sideloadHostDir is where sideloaded addons live on the server, relative to
// the compose project root. It sits inside the lock's `.echo/` directory, whose
// `*` .gitignore keeps it out of a client repository checked out at that root.
const sideloadHostDir = lockDirName + "/sideload"

// sideloadOverrideFile is the compose override Echo writes to expose the
// sideloaded addons to the container. Compose auto-loads it on every `up`,
// so the client's own restarts pick up the mounts without any change to
// their invocations.
const sideloadOverrideFile = "docker-compose.override.yml"

// sideloadOverrideMarker heads every Echo-managed override so a re-run can
// tell its own file from a client-authored one. A file lacking the marker
// is never overwritten — Echo refuses to edit what it did not create.
const sideloadOverrideMarker = "# managed by echo sideload — do not edit by hand"

// sideloadHashPrefix marks the content hash stored in a lock entry's Tree, so
// it never reads as a git tree id.
const sideloadHashPrefix = "sha256:"

// SideloadOpts configures a `sideload` run. It mirrors DeployOpts.
type SideloadOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log emits one Odoo-style progress line (rendered by the REPL through
	// emitOdooLog under `echo.sideload[.sub]`).
	Log func(level, sub, msg, db string, fields ...[2]string)
	// StreamOut receives the remote stop/up/odoo lines as they stream.
	StreamOut func(string)
}

func (o SideloadOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// SideloadResult is what a sideload run did, or with --list what the target
// holds; the REPL prints it under --json.
type SideloadResult struct {
	Target   string           `json:"target"`
	DB       string           `json:"db"`
	Addons   []SideloadModule `json:"addons"`
	Mounts   int              `json:"mounts,omitempty"`
	Recreate bool             `json:"recreate,omitempty"`
	Planned  bool             `json:"planned,omitempty"`
	JSON     bool             `json:"-"`
}

// SideloadModule is one addon of a run (action install|update|none, synced
// when its files travelled) or of a --list (at, verified, dest).
type SideloadModule struct {
	Name     string `json:"name"`
	Action   string `json:"action,omitempty"`
	Version  string `json:"version,omitempty"`
	Hash     string `json:"hash,omitempty"`
	Synced   bool   `json:"synced,omitempty"`
	At       string `json:"at,omitempty"`
	Verified *bool  `json:"verified,omitempty"`
	Dest     string `json:"dest,omitempty"`
}

type sideloadArgs struct {
	source    string
	from      string
	dryRun    bool
	allowProd bool
	resync    bool
	noLint    bool
	jsonOut   bool
	list      bool
}

func parseSideloadArgs(args []string) (sideloadArgs, error) {
	out := sideloadArgs{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			out.dryRun = true
		case a == "--allow-prod":
			out.allowProd = true
		case a == "--resync":
			out.resync = true
		case a == "--no-lint":
			out.noLint = true
		case a == "--json":
			out.jsonOut = true
		case a == "--list":
			out.list = true
		case a == "--from":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%w: --from requires a target name", ErrUsage)
			}
			out.from = args[i+1]
			i++
		case strings.HasPrefix(a, "--from="):
			out.from = strings.TrimPrefix(a, "--from=")
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			if out.source != "" {
				return out, fmt.Errorf("%w: sideload takes a single source path", ErrUsage)
			}
			out.source = a
		}
	}
	if out.list {
		if out.source != "" || out.dryRun || out.allowProd || out.resync || out.noLint {
			return out, fmt.Errorf("%w: --list takes only --from and --json", ErrUsage)
		}
		return out, nil
	}
	if out.source == "" {
		return out, fmt.Errorf("%w: sideload requires a source path (an addon dir or a folder of addons)", ErrUsage)
	}
	return out, nil
}

// sideloadAddon is one addon to ship: its module name and absolute local
// directory (the folder containing __manifest__.py).
type sideloadAddon struct {
	name string
	dir  string
}

// sideloadExcludes are directory/file names skipped when hashing and
// packing an addon — build noise that must never affect the content hash
// nor land on the server.
var sideloadExcludes = map[string]bool{
	".git":        true,
	"__pycache__": true,
	".DS_Store":   true,
}

// sideloadExcluded reports whether a path element (a single name) is
// excluded, plus a suffix check for compiled Python.
func sideloadExcluded(name string) bool {
	if sideloadExcludes[name] {
		return true
	}
	return strings.HasSuffix(name, ".pyc")
}

// collectAddons resolves the source path to one or more addons. A path
// that is itself a module (holds __manifest__.py) yields a single addon;
// a folder yields every direct child that is a module. An error is
// returned when nothing qualifies.
func collectAddons(source string) ([]sideloadAddon, error) {
	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("source path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source %q is not a directory", source)
	}
	if isModuleDir(abs) {
		return []sideloadAddon{{name: filepath.Base(abs), dir: abs}}, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var addons []sideloadAddon
	for _, e := range entries {
		if !e.IsDir() || sideloadExcluded(e.Name()) {
			continue
		}
		child := filepath.Join(abs, e.Name())
		if isModuleDir(child) {
			addons = append(addons, sideloadAddon{name: e.Name(), dir: child})
		}
	}
	if len(addons) == 0 {
		return nil, fmt.Errorf("no addon found under %q (need a __manifest__.py)", source)
	}
	sort.Slice(addons, func(i, j int) bool { return addons[i].name < addons[j].name })
	return addons, nil
}

// isModuleDir reports whether dir contains an Odoo manifest.
func isModuleDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "__manifest__.py"))
	return err == nil && !st.IsDir()
}

// hashAddonTree returns a hex SHA-256 over the addon's file tree —
// relative paths and file contents, in sorted path order, skipping the
// shared exclude set. Stable across machines: identical trees hash equal,
// so an unchanged addon is detected as unchanged.
func hashAddonTree(dir string) (string, error) {
	type entry struct {
		rel  string
		path string
	}
	var files []entry
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if sideloadExcluded(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, entry{rel: filepath.ToSlash(rel), path: path})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	h := sha256.New()
	for _, f := range files {
		fh := sha256.New()
		src, err := os.Open(f.path)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(fh, src); err != nil {
			src.Close()
			return "", err
		}
		src.Close()
		fmt.Fprintf(h, "%s\x00%x\n", f.rel, fh.Sum(nil))
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// tarAddon packs the addon directory into an in-memory .tar.gz with file
// paths relative to the addon root (so `tar xzf - -C <dir>` lands the
// files directly in <dir>), honoring the shared exclude set.
func tarAddon(dir string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if sideloadExcluded(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		hdr, herr := tar.FileInfoHeader(info, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		src, oerr := os.Open(path)
		if oerr != nil {
			return oerr
		}
		defer src.Close()
		_, cerr := io.Copy(tw, src)
		return cerr
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// pickMountBase chooses the container directory the sideloaded addons are
// nested under. A nested bind mount at `<base>/<addon>` makes the addon a
// sibling of the client's own modules inside a path already on
// `addons_path`, so Odoo discovers it without an odoo.conf edit. The stock
// python addons dir (under /usr) is skipped in favor of a developer addons
// path; absent any candidate it falls back to /mnt/extra-addons (the
// official image's default extra path).
func pickMountBase(addonsPaths []string) string {
	var fallback string
	for _, p := range addonsPaths {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if fallback == "" {
			fallback = p
		}
		if !strings.HasPrefix(p, "/usr/") {
			return p
		}
	}
	if fallback != "" {
		return fallback
	}
	return "/mnt/extra-addons"
}

// renderSideloadOverride produces the Echo-managed compose override that
// mounts each addon from the host's sideload dir into the container at
// <mountBase>/<addon>. Output is deterministic (addons sorted) so re-writes
// are idempotent and diffable.
func renderSideloadOverride(service, mountBase string, addons []string) string {
	sorted := append([]string(nil), addons...)
	sort.Strings(sorted)
	base := strings.TrimRight(mountBase, "/")

	var b strings.Builder
	b.WriteString(sideloadOverrideMarker + "\n")
	b.WriteString("services:\n")
	b.WriteString("  " + service + ":\n")
	b.WriteString("    volumes:\n")
	for _, name := range sorted {
		fmt.Fprintf(&b, "      - ./%s/%s:%s/%s\n", sideloadHostDir, name, base, name)
	}
	return b.String()
}

// checkAddonSeries refuses an addon whose manifest version names another Odoo
// series than the target's. Odoo prefixes the series to a version shorter
// than the full five parts, so only the full form can be wrong.
func checkAddonSeries(name, version, serie string) error {
	parts := strings.Split(version, ".")
	if len(parts) != 5 || serie == "" {
		return nil
	}
	if got := parts[0] + "." + parts[1]; got != serie {
		return fmt.Errorf("%w: %s is built for Odoo %s (version %s) but the target runs %s",
			ErrUsage, name, got, version, serie)
	}
	return nil
}

// sideloadTargetError says why a target cannot take a sideload at all, or nil.
func sideloadTargetError(rsc remoteShellContext, allowProd bool) error {
	if rsc.reverb != nil || rsc.prof.Reverb != nil {
		return fmt.Errorf("%w: sideload does not support Reverb environments (Reverb owns their compose)", ErrUsage)
	}
	if strings.EqualFold(rsc.target.stage, "prod") && !allowProd {
		return fmt.Errorf("%w: sideload refuses prod targets (stage=%s); pass --allow-prod to be asked",
			ErrUsage, rsc.target.rawStage)
	}
	return nil
}

// confirmSideloadProd asks before writing to a prod target. Unlike deploy,
// --force never answers for the user: without a TTY the run fails closed.
func confirmSideloadProd(palette theme.Palette, rsc remoteShellContext) error {
	if !strings.EqualFold(rsc.target.stage, "prod") {
		return nil
	}
	if err := requireTTY("sideload asks before writing to prod and has no headless override"); err != nil {
		return err
	}
	return confirmProd(palette, "sideload", rsc.target.dbName)
}

func sideloadLintPreflight(opts SideloadOpts, p sideloadArgs, addons []sideloadAddon, odooVersion string) error {
	if p.noLint {
		opts.log("WARNING", "lint", "pre-flight skipped", "", [2]string{"flag", "--no-lint"})
		return nil
	}
	dirs := make([]string, len(addons))
	names := make([]string, len(addons))
	for i, a := range addons {
		dirs[i], names[i] = a.dir, a.name
	}
	res, err := lintModuleDirs(odooVersion, dirs, names)
	if err != nil {
		opts.log("WARNING", "lint", "pre-flight could not run", "", [2]string{"err", err.Error()})
		return nil
	}
	errs := reportLintResult(opts.log, filepath.Dir(dirs[0]), odooVersion, res)
	if errs == 0 {
		return nil
	}
	opts.log("ERROR", "lint", "pre-flight blocked the sideload", "",
		[2]string{"errors", strconv.Itoa(errs)},
		[2]string{"hint", "fix the files above, or sideload --no-lint"})
	return fmt.Errorf("%w: %d file(s) the data loader would refuse (use --no-lint to override)",
		ErrLintBlocked, errs)
}

// RunSideload installs or updates addons that live outside the project's repo
// on a remote dev/staging target. It packs each local addon, streams the
// changed ones into <remotePath>/.echo/sideload/, exposes them through an
// Echo-managed docker-compose.override.yml (nested per-addon bind mounts, so
// odoo.conf and the client's compose stay untouched), records them in the
// deploy lock as source=sideload, then runs stop → up -d → one -i/-u. Unchanged
// addons (by content hash) are skipped; prod is refused unless --allow-prod
// and an interactive confirm.
func RunSideload(ctx context.Context, opts SideloadOpts) (SideloadResult, error) {
	p, err := parseSideloadArgs(opts.Args)
	if err != nil {
		return SideloadResult{}, err
	}
	if p.list {
		return runSideloadList(ctx, opts, p)
	}

	addons, err := collectAddons(p.source)
	if err != nil {
		return SideloadResult{}, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	names := make([]string, len(addons))
	for i, a := range addons {
		names[i] = a.name
	}
	opts.log("INFO", "", "addons collected", "", [2]string{"addons", strings.Join(names, ",")})

	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
	if err != nil {
		return SideloadResult{}, err
	}
	db := rsc.target.dbName
	res := SideloadResult{Target: rsc.fromName, DB: db, JSON: p.jsonOut}
	if err := sideloadTargetError(rsc, p.allowProd); err != nil {
		return res, err
	}

	serie := odooSerie(rsc.target.odooVersion)
	if serie == "" {
		opts.log("WARNING", "plan", "target declares no Odoo version, manifest series not checked", db)
	}
	versions := make(map[string]string, len(addons))
	for _, a := range addons {
		b, rerr := os.ReadFile(filepath.Join(a.dir, "__manifest__.py"))
		if rerr != nil {
			return res, rerr
		}
		versions[a.name] = manifestVersion(string(b))
		if err := checkAddonSeries(a.name, versions[a.name], serie); err != nil {
			return res, err
		}
	}
	if err := sideloadLintPreflight(opts, p, addons, rsc.target.odooVersion); err != nil {
		return res, err
	}

	// Volumes live under the compose service, not the container_name.
	service := resolveOdooService(ctx, rsc)
	opts.log("INFO", "mount", "compose service resolved", db, [2]string{"service", service})

	addonsPaths := rsc.prof.AddonsPaths
	if len(addonsPaths) == 0 {
		addonsPaths = remoteContainerAddonsPaths(ctx, rsc)
	}
	mountBase := pickMountBase(addonsPaths)
	opts.log("INFO", "mount", "mount base resolved", db, [2]string{"base", mountBase})

	projectKey := config.ProjectKey(opts.Root)
	targetKey := config.DeployTargetKey(rsc.sshHost, rsc.remotePath)
	storedHashes := config.LoadSideloadHashes(projectKey, targetKey)

	newHashes := make(map[string]string, len(addons))
	changed := make(map[string]bool, len(addons))
	var toSync []sideloadAddon
	for _, a := range addons {
		h, herr := hashAddonTree(a.dir)
		if herr != nil {
			return res, fmt.Errorf("hash %s: %w", a.name, herr)
		}
		newHashes[a.name] = h
		if p.resync || storedHashes[a.name] != h {
			changed[a.name] = true
			toSync = append(toSync, a)
		} else {
			opts.log("INFO", "sync", "up to date", db, [2]string{"addon", a.name})
		}
	}

	// Mount every addon ever sideloaded here, so adding B never un-mounts A.
	overrideNames := unionKeys(storedHashes, newHashes)
	res.Mounts = len(overrideNames)

	// The hash only gates the file sync; the module state decides -i vs -u.
	states, _, err := remoteModuleStates(ctx, rsc.sshHost, rsc.remotePath, rsc.target, rsc.conn.User, db)
	if err != nil {
		return res, fmt.Errorf("query remote module states: %w", err)
	}
	var install, update []string
	for _, a := range addons {
		action := "none"
		switch states[a.name] {
		case "installed", "to upgrade":
			if changed[a.name] {
				action = "update"
				update = append(update, a.name)
			}
		default:
			action = "install"
			install = append(install, a.name)
		}
		res.Addons = append(res.Addons, SideloadModule{
			Name: a.name, Action: action, Version: versions[a.name],
			Hash: newHashes[a.name], Synced: changed[a.name],
		})
	}

	opts.log("INFO", "plan", "sideload plan", db,
		[2]string{"sync", strconv.Itoa(len(toSync))},
		[2]string{"install", strings.Join(install, ",")},
		[2]string{"update", strings.Join(update, ",")},
		[2]string{"mounts", strconv.Itoa(len(overrideNames))})

	if p.dryRun {
		res.Planned = true
		for i := range res.Addons {
			res.Addons[i].Synced = false
		}
		opts.log("INFO", "", "dry-run — nothing executed", db)
		return res, nil
	}

	if err := confirmSideloadProd(opts.Palette, rsc); err != nil {
		return res, err
	}

	// Before any file travels: a foreign override stops the run with nothing written.
	recreate, err := writeSideloadOverride(ctx, opts, rsc, service, mountBase, overrideNames)
	if err != nil {
		return res, err
	}
	res.Recreate = recreate

	// Replace-then-untar gives --delete semantics with only `tar` on the server.
	echoDir := shellQuote(rsc.remotePath + "/" + lockDirName)
	ensureEchoDir := "mkdir -p " + echoDir + " && { [ -f " + echoDir + "/.gitignore ] || printf '*\\n' > " + echoDir + "/.gitignore; }"
	shipped := make(map[string]LockModule, len(toSync))
	by := ""
	if out, gerr := gitOutput(ctx, filepath.Dir(addons[0].dir), "config", "user.email"); gerr == nil {
		by = firstLine(string(out))
	}
	for _, a := range toSync {
		blob, terr := tarAddon(a.dir)
		if terr != nil {
			return res, fmt.Errorf("pack %s: %w", a.name, terr)
		}
		remoteDir := rsc.remotePath + "/" + sideloadHostDir + "/" + a.name
		q := shellQuote(remoteDir)
		untar := ensureEchoDir + " && rm -rf " + q + " && mkdir -p " + q + " && tar xzf - -C " + q
		opts.log("INFO", "sync", "syncing addon", db,
			[2]string{"addon", a.name}, [2]string{"bytes", strconv.Itoa(len(blob))})
		if err := runSSHStream(ctx, rsc.sshHost, untar, blob, opts.StreamOut); err != nil {
			return res, fmt.Errorf("sync %s failed: %w", a.name, err)
		}
		shipped[a.name] = LockModule{
			Source:  lockSourceSideload,
			Tree:    sideloadHashPrefix + newHashes[a.name],
			Version: versions[a.name],
			Dest:    remoteDir,
			Via:     "sideload",
			At:      time.Now().UTC().Format(time.RFC3339),
			By:      by,
		}
	}
	if len(shipped) > 0 {
		updateDeployLock(ctx, rsc, opts.Log, func(l *DeployLock) { l.record(shipped) })
	}

	step := func(name, remoteCmd string) error {
		opts.log("INFO", "compose", name, db)
		if err := runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut); err != nil {
			return fmt.Errorf("%s failed: %w", name, err)
		}
		return nil
	}
	if len(install) > 0 || len(update) > 0 {
		if err := step("stop", remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "stop")); err != nil {
			return res, err
		}
		if err := step("up -d", remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "up", "-d")); err != nil {
			return res, err
		}
		var runFields [][2]string
		if len(update) > 0 {
			runFields = append(runFields, [2]string{"update", strings.Join(update, ",")})
		}
		if len(install) > 0 {
			runFields = append(runFields, [2]string{"install", strings.Join(install, ",")})
		}
		opts.log("INFO", "odoo", "running module install/update", db, runFields...)
		argv := odoo.InstallUpdate(rsc.conn, install, update)
		if err := runSSHStream(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, rsc.target, argv), nil, opts.StreamOut); err != nil {
			return res, fmt.Errorf("odoo run failed: %w", err)
		}
		ran := make(map[string]LockModule, len(install)+len(update))
		for _, name := range append(append([]string(nil), install...), update...) {
			ran[name] = LockModule{}
		}
		updateDeployLock(ctx, rsc, opts.Log, func(l *DeployLock) { l.markVerified(ran) })
	} else {
		opts.log("INFO", "odoo", "no install/update needed", db)
	}

	// Recorded only now, so a failed run re-syncs next time.
	syncedHashes := make(map[string]string, len(toSync))
	for _, a := range toSync {
		syncedHashes[a.name] = newHashes[a.name]
	}
	if err := config.RecordSideloadHashes(projectKey, targetKey, syncedHashes); err == nil && len(syncedHashes) > 0 {
		opts.log("INFO", "history", "recorded synced addons", db,
			[2]string{"n", strconv.Itoa(len(syncedHashes))})
	}

	opts.log("INFO", "", "sideload complete", db,
		[2]string{"synced", strconv.Itoa(len(toSync))},
		[2]string{"install", strconv.Itoa(len(install))},
		[2]string{"update", strconv.Itoa(len(update))})
	return res, nil
}

// runSideloadList reports the addons the target's deploy lock records as
// sideloaded. Read-only.
func runSideloadList(ctx context.Context, opts SideloadOpts, p sideloadArgs) (SideloadResult, error) {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
	if err != nil {
		return SideloadResult{}, err
	}
	db := rsc.target.dbName
	res := SideloadResult{Target: rsc.fromName, DB: db, JSON: p.jsonOut, Addons: []SideloadModule{}}
	lock, _ := readDeployLock(ctx, rsc, opts.Log)
	for _, name := range sortedLockModules(lock) {
		e := lock.Modules[name]
		if e.Source != lockSourceSideload {
			continue
		}
		verified := e.Verified
		m := SideloadModule{
			Name: name, Version: e.Version, Hash: strings.TrimPrefix(e.Tree, sideloadHashPrefix),
			At: e.At, Verified: &verified, Dest: e.Dest,
		}
		res.Addons = append(res.Addons, m)
		opts.log("INFO", "lock", "addon", db,
			[2]string{"name", name}, [2]string{"version", m.Version}, [2]string{"hash", shortSHA(m.Hash)},
			[2]string{"at", m.At}, [2]string{"verified", strconv.FormatBool(verified)})
	}
	if len(res.Addons) == 0 {
		opts.log("INFO", "lock", "nothing sideloaded on this target", db)
	}
	return res, nil
}

// writeSideloadOverride writes the Echo-managed compose override on the
// remote, refusing to overwrite a client-authored one. Unchanged content is
// left untouched (no needless recreate signal).
func writeSideloadOverride(ctx context.Context, opts SideloadOpts, rsc remoteShellContext, service, mountBase string, addons []string) (written bool, err error) {
	db := rsc.target.dbName
	overridePath := rsc.remotePath + "/" + sideloadOverrideFile

	readCmd := "cat " + shellQuote(overridePath) + " 2>/dev/null || true"
	existing, _ := runSSH(ctx, rsc.sshHost, readCmd, nil)
	if strings.TrimSpace(string(existing)) != "" && !strings.Contains(string(existing), sideloadOverrideMarker) {
		opts.log("ERROR", "mount", "a foreign docker-compose.override.yml exists — refusing to overwrite", db,
			[2]string{"path", overridePath})
		want := renderSideloadOverride(service, mountBase, addons)
		opts.log("WARNING", "mount", "add these volume mounts to the odoo service manually", db)
		for _, line := range strings.Split(strings.TrimRight(want, "\n"), "\n") {
			opts.log("WARNING", "mount", line, db)
		}
		return false, fmt.Errorf("foreign %s present — mounts not written", sideloadOverrideFile)
	}

	content := renderSideloadOverride(service, mountBase, addons)
	if strings.TrimSpace(string(existing)) == strings.TrimSpace(content) {
		opts.log("INFO", "mount", "override already current", db)
		return false, nil
	}
	writeCmd := "cat > " + shellQuote(overridePath)
	if _, err := runSSH(ctx, rsc.sshHost, writeCmd, []byte(content)); err != nil {
		return false, fmt.Errorf("write %s: %w", sideloadOverrideFile, err)
	}
	opts.log("INFO", "mount", "override written (container recreate on up -d)", db,
		[2]string{"mounts", strconv.Itoa(len(addons))})
	return true, nil
}

// resolveOdooService finds the compose service name of the profile's Odoo
// container by matching it against the remote `compose ps`. Falls back to
// the container name when the structured read fails or no row matches.
func resolveOdooService(ctx context.Context, rsc remoteShellContext) string {
	fallback := rsc.target.odooContainer
	jsonCmd := remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "ps", "--format", "json")
	out, err := runSSH(ctx, rsc.sshHost, jsonCmd, nil)
	if err != nil {
		return fallback
	}
	rows, perr := docker.ParsePS(out)
	if perr != nil {
		return fallback
	}
	for _, r := range rows {
		if r.Name == rsc.target.odooContainer && r.Service != "" {
			return r.Service
		}
	}
	// Single-row fallback: if only the Odoo service is up, use it.
	if len(rows) == 1 && rows[0].Service != "" {
		return rows[0].Service
	}
	return fallback
}

// remoteContainerAddonsPaths reads the container's odoo.conf addons_path
// when the profile did not carry it. Best-effort: an empty result lets
// pickMountBase fall back to its default.
func remoteContainerAddonsPaths(ctx context.Context, rsc remoteShellContext) []string {
	confPath := rsc.prof.ConfPath
	if confPath == "" {
		confPath = "/etc/odoo/odoo.conf"
	}
	out, err := runSSH(ctx, rsc.sshHost, remoteContainerCmd(rsc.remotePath, rsc.target, odoo.Cmd{"cat", confPath}), nil)
	if err != nil {
		return nil
	}
	return parseAddonsPath(string(out))
}

// unionKeys returns the sorted union of the keys of two string maps.
func unionKeys(a, b map[string]string) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
