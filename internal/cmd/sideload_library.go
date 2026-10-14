package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
)

// libraryModule is one module the sideload library offers for a target
// series. branch and sha are set for a versioned repository (the module is
// read from the branch named after the series); a folder root leaves them
// empty and the module ships from its working tree.
type libraryModule struct {
	name    string
	version string
	root    string // the library entry as configured
	absRoot string
	branch  string
	sha     string
	relPath string // module directory relative to the root, slash-separated
}

func (m libraryModule) source() string {
	base := filepath.Base(m.absRoot)
	if m.branch != "" {
		return base + "@" + m.branch
	}
	return base
}

var seriesBranchRe = regexp.MustCompile(`^\d+\.0$`)

// librarySkipDirs are never searched for modules.
var librarySkipDirs = map[string]bool{"node_modules": true, "__pycache__": true}

func librarySkipped(name string) bool {
	return librarySkipDirs[name] || strings.HasPrefix(name, ".")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// seriesBranches returns the local branches of root named like an Odoo
// series (`17.0`, `18.0`, …). Empty for a folder or a repository without them.
func seriesBranches(ctx context.Context, root string) map[string]bool {
	out, err := gitOutput(ctx, root, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	branches := map[string]bool{}
	for _, b := range nonEmptyLines(string(out)) {
		if seriesBranchRe.MatchString(b) {
			branches[b] = true
		}
	}
	return branches
}

// manifestPathsAtDepth keeps the module directories (depth 1 or 2) among
// slash-separated `<dir>/__manifest__.py` paths, skipping hidden and build
// directories.
func manifestPathsAtDepth(paths []string) []string {
	var dirs []string
	for _, p := range paths {
		if path.Base(p) != "__manifest__.py" {
			continue
		}
		dir := path.Dir(p)
		parts := strings.Split(dir, "/")
		if dir == "." || len(parts) > 2 {
			continue
		}
		skip := false
		for _, part := range parts {
			if librarySkipped(part) {
				skip = true
			}
		}
		if !skip {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// discoverLibrary lists the modules of every library root that fit serie
// (`19.0`). A versioned repository offers the modules of its serie branch; a
// folder offers its working-tree modules whose manifest does not name another
// series. hidden counts the folder modules left out for their series; problems
// with a root are returned as warnings, never as an error.
func discoverLibrary(ctx context.Context, roots []string, serie string) (mods []libraryModule, hidden int, warnings []string) {
	for _, root := range roots {
		abs := expandHome(root)
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("library root %s is not a directory", root))
			continue
		}
		if branches := seriesBranches(ctx, abs); len(branches) > 0 {
			if !branches[serie] {
				continue
			}
			found, err := discoverBranch(ctx, root, abs, serie)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("library root %s: %v", root, err))
				continue
			}
			mods = append(mods, found...)
			continue
		}
		found, skipped := discoverFolder(root, abs, serie)
		mods = append(mods, found...)
		hidden += skipped
	}
	sort.Slice(mods, func(i, j int) bool {
		if mods[i].name != mods[j].name {
			return mods[i].name < mods[j].name
		}
		return mods[i].root < mods[j].root
	})
	return mods, hidden, warnings
}

func discoverBranch(ctx context.Context, root, abs, branch string) ([]libraryModule, error) {
	shaOut, err := gitOutput(ctx, abs, "rev-parse", branch)
	if err != nil {
		return nil, err
	}
	sha := firstLine(string(shaOut))
	listing, err := gitOutput(ctx, abs, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	var mods []libraryModule
	for _, dir := range manifestPathsAtDepth(nonEmptyLines(string(listing))) {
		manifest, err := gitOutput(ctx, abs, "show", sha+":"+dir+"/__manifest__.py")
		if err != nil {
			return nil, err
		}
		mods = append(mods, libraryModule{
			name: path.Base(dir), version: manifestVersion(string(manifest)),
			root: root, absRoot: abs, branch: branch, sha: sha, relPath: dir,
		})
	}
	return mods, nil
}

func discoverFolder(root, abs, serie string) (mods []libraryModule, hidden int) {
	var manifests []string
	_ = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if d.IsDir() && p != abs && (librarySkipped(d.Name()) || depth > 2) {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "__manifest__.py" {
			manifests = append(manifests, filepath.ToSlash(rel))
		}
		return nil
	})
	for _, dir := range manifestPathsAtDepth(manifests) {
		b, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(dir), "__manifest__.py"))
		if err != nil {
			continue
		}
		version := manifestVersion(string(b))
		if checkAddonSeries(path.Base(dir), version, serie) != nil {
			hidden++
			continue
		}
		mods = append(mods, libraryModule{
			name: path.Base(dir), version: version, root: root, absRoot: abs, relPath: dir,
		})
	}
	return mods, hidden
}

// findLibraryModule resolves a module name against the library: exactly one
// match, or ErrUsage naming where it looked or where it is ambiguous.
func findLibraryModule(mods []libraryModule, name string, roots []string, serie string) (libraryModule, error) {
	var hits []libraryModule
	for _, m := range mods {
		if m.name == name {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if len(roots) == 0 {
			return libraryModule{}, fmt.Errorf("%w: %q is not a path and the sideload library is empty (add one with sideload --library-add <path>)", ErrUsage, name)
		}
		return libraryModule{}, fmt.Errorf("%w: no module %q for Odoo %s in the sideload library (%s)",
			ErrUsage, name, serie, strings.Join(roots, ", "))
	default:
		sources := make([]string, len(hits))
		for i, h := range hits {
			sources[i] = h.source()
		}
		return libraryModule{}, fmt.Errorf("%w: module %q is in several library roots: %s",
			ErrUsage, name, strings.Join(sources, ", "))
	}
}

// materialize returns the local directory to pack for m: the working-tree
// module of a folder root, or the module extracted from its branch into
// scratch.
func (m libraryModule) materialize(ctx context.Context, scratch string) (string, error) {
	if m.branch == "" {
		return filepath.Join(m.absRoot, filepath.FromSlash(m.relPath)), nil
	}
	if err := extractCommitTrees(ctx, m.absRoot, scratch, m.sha, []string{m.relPath}); err != nil {
		return "", err
	}
	return filepath.Join(scratch, filepath.FromSlash(m.relPath)), nil
}

// dirtyOnBranch reports whether the repository has the module's branch
// checked out with uncommitted changes under the module, which a versioned
// sideload does not ship.
func (m libraryModule) dirtyOnBranch(ctx context.Context) bool {
	if m.branch == "" {
		return false
	}
	head, err := gitOutput(ctx, m.absRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || firstLine(string(head)) != m.branch {
		return false
	}
	out, err := gitOutput(ctx, m.absRoot, "status", "--porcelain", "--", m.relPath)
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// runSideloadLibrary handles the config-only library flags: print the roots,
// or add/remove one in global.toml. No SSH.
func runSideloadLibrary(opts SideloadOpts, p sideloadArgs) (SideloadResult, error) {
	roots := append([]string(nil), opts.Cfg.SideloadLibrary...)
	switch {
	case p.libraryAdd != "":
		abs := expandHome(p.libraryAdd)
		if !filepath.IsAbs(abs) {
			cwd, err := os.Getwd()
			if err != nil {
				return SideloadResult{}, err
			}
			abs = filepath.Join(cwd, abs)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return SideloadResult{}, fmt.Errorf("%w: library root %s is not a directory", ErrUsage, p.libraryAdd)
		}
		entry := homeShorthand(filepath.Clean(abs))
		for _, r := range roots {
			if expandHome(r) == expandHome(entry) {
				opts.log("INFO", "library", "already in the library", "", [2]string{"root", entry})
				return SideloadResult{}, nil
			}
		}
		roots = append(roots, entry)
		if err := config.SaveSideloadLibrary(roots); err != nil {
			return SideloadResult{}, err
		}
		opts.log("INFO", "library", "root added", "", [2]string{"root", entry})
	case p.libraryRm != "":
		target := expandHome(p.libraryRm)
		kept := roots[:0]
		removed := false
		for _, r := range roots {
			if expandHome(r) == target || r == p.libraryRm {
				removed = true
				continue
			}
			kept = append(kept, r)
		}
		if !removed {
			return SideloadResult{}, fmt.Errorf("%w: %s is not in the sideload library", ErrUsage, p.libraryRm)
		}
		if err := config.SaveSideloadLibrary(kept); err != nil {
			return SideloadResult{}, err
		}
		opts.log("INFO", "library", "root removed", "", [2]string{"root", p.libraryRm})
	default:
		if len(roots) == 0 {
			opts.log("INFO", "library", "the sideload library is empty", "",
				[2]string{"hint", "sideload --library-add <path>"})
		}
		for _, r := range roots {
			kind := "folder"
			if len(seriesBranches(context.Background(), expandHome(r))) > 0 {
				kind = "versioned"
			}
			level := "INFO"
			if info, err := os.Stat(expandHome(r)); err != nil || !info.IsDir() {
				level, kind = "WARNING", "missing"
			}
			opts.log(level, "library", "root", "", [2]string{"path", r}, [2]string{"kind", kind})
		}
	}
	return SideloadResult{}, nil
}

func homeShorthand(abs string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return abs
	}
	if rel, err := filepath.Rel(home, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return abs
}

// isSideloadPath tells a path positional from a library module name: a path
// separator, a relative dot path, or an existing directory.
func isSideloadPath(s string) bool {
	if strings.ContainsRune(s, '/') || strings.ContainsRune(s, filepath.Separator) || s == "." || s == ".." || strings.HasPrefix(s, "~") {
		return true
	}
	info, err := os.Stat(s)
	return err == nil && info.IsDir()
}

// libraryStatus describes each library module against what the target runs:
// `new`, `sideloaded · current` (the lock holds the same content hash),
// `sideloaded · changed`, or `on target · <source>` when something other than
// sideload shipped a module of that name. Branch modules are extracted into
// scratch to be hashed.
func libraryStatus(ctx context.Context, opts SideloadOpts, rsc remoteShellContext, lib []libraryModule, scratch string) ([]SideloadModule, error) {
	lock, _ := readDeployLock(ctx, rsc, opts.Log)
	rows := make([]SideloadModule, 0, len(lib))
	for _, m := range lib {
		dest, err := os.MkdirTemp(scratch, "s-*")
		if err != nil {
			return nil, err
		}
		dir, err := m.materialize(ctx, dest)
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", m.name, m.source(), err)
		}
		hash, err := hashAddonTree(dir)
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", m.name, err)
		}
		status := "new"
		if e, ok := lock.Modules[m.name]; ok {
			switch {
			case e.Source != lockSourceSideload:
				status = "on target · " + e.Source
			case e.Tree == sideloadHashPrefix+hash:
				status = "sideloaded · current"
			default:
				status = "sideloaded · changed"
			}
		}
		rows = append(rows, SideloadModule{
			Name: m.name, Version: m.version, Hash: hash, Source: m.source(), Status: status,
		})
	}
	return rows, nil
}

// sideloadPicker is the multi-select the library picker runs; a seam for tests.
var sideloadPicker = runFuzzyPickerCore

// pickLibraryModules opens the multi-select over the library, one aligned row
// per module, with modules already current on the target muted.
func pickLibraryModules(ctx context.Context, opts SideloadOpts, rsc remoteShellContext, lib []libraryModule, scratch string) ([]libraryModule, error) {
	rows, err := libraryStatus(ctx, opts, rsc, lib, scratch)
	if err != nil {
		return nil, err
	}
	widths := [3]int{}
	for _, r := range rows {
		for i, v := range []string{r.Name, r.Version, r.Source} {
			if len(v) > widths[i] {
				widths[i] = len(v)
			}
		}
	}
	labels := make([]string, len(rows))
	byLabel := make(map[string]libraryModule, len(rows))
	var muted []string
	for i, r := range rows {
		labels[i] = fmt.Sprintf("%-*s  %-*s  %-*s  %s",
			widths[0], r.Name, widths[1], r.Version, widths[2], r.Source, r.Status)
		byLabel[labels[i]] = lib[i]
		if r.Status == "sideloaded · current" {
			muted = append(muted, labels[i])
		}
	}
	picked, _, canceled, err := sideloadPicker("Modules to sideload", labels, nil, muted, nil, opts.Palette, rsc.target.stage)
	if err != nil {
		return nil, err
	}
	if canceled || len(picked) == 0 {
		return nil, ErrCancelled
	}
	chosen := make([]libraryModule, len(picked))
	for i, l := range picked {
		chosen[i] = byLabel[l]
	}
	return chosen, nil
}

// SideloadLibraryNames lists every module name the configured library holds,
// across all series, for Tab completion. Best-effort: errors yield fewer names.
func SideloadLibraryNames(ctx context.Context) []string {
	cfg, err := config.LoadGlobal()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, root := range cfg.SideloadLibrary {
		abs := expandHome(root)
		branches := seriesBranches(ctx, abs)
		if len(branches) == 0 {
			mods, _ := discoverFolder(root, abs, "")
			for _, m := range mods {
				seen[m.name] = true
			}
			continue
		}
		for b := range branches {
			mods, _ := discoverBranch(ctx, root, abs, b)
			for _, m := range mods {
				seen[m.name] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
