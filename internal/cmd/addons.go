package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
)

// defaultAddonsPaths is the conventional layout probed when a project has
// no configured addons paths.
var defaultAddonsPaths = []string{".", "addons", "custom"}

// discoveryMaxDepth bounds the recursive walk that backs up the configured
// roots. Three levels reach the common `<repo>/<group>/<module>` layouts
// without turning a miss into a full-tree scan.
const discoveryMaxDepth = 3

// addonsRoots returns the absolute host directories that hold addons,
// taken from the project config and falling back to the conventional
// layout. Conf mode's paths live inside the container and cannot be walked
// here, so that mode uses the conventional layout too. Directories that do
// not exist are dropped.
func addonsRoots(cfg *config.Config, root string) []string {
	paths := cfg.AddonsPaths
	if cfg.AddonsMode == addonsModeConf || len(paths) == 0 {
		paths = defaultAddonsPaths
	}
	var out []string
	for _, sub := range paths {
		dir := filepath.Join(root, sub)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, absOrSelf(dir))
		}
	}
	return out
}

// discoverAddonsRoots walks root looking for directories that contain an
// addon, so a repo whose modules live in a subfolder works with no
// configuration — the way Odoo itself takes several addons_path entries.
// Hidden and vendored directories are skipped, and an addon is never
// descended into.
func discoverAddonsRoots(root string) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > discoveryMaxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || skipDirs[name] {
				continue
			}
			sub := filepath.Join(dir, name)
			if isManifestDir(sub) {
				if !seen[dir] {
					seen[dir] = true
					out = append(out, absOrSelf(dir))
				}
				continue
			}
			walk(sub, depth+1)
		}
	}
	walk(root, 1)
	sort.Strings(out)
	return out
}

// resolveAddon maps a module argument to the directory holding it and to
// the bare module name. The configured roots win; the recursive walk only
// runs when they miss, so a project that resolves today keeps its exact
// behavior and never pays for the scan.
//
// An argument carrying a path separator is read as a path relative to root
// and reduced to its basename, so a shell-completed `sub/mod` never leaks
// the separator into a remote destination. The same module name under two
// roots is ErrUsage: choosing one silently is how a push lands somewhere
// the addons_path shadows.
func resolveAddon(cfg *config.Config, root, name string) (dir, module string, err error) {
	if name == "" {
		return "", "", fmt.Errorf("%w: empty module name", ErrUsage)
	}
	if strings.ContainsAny(name, `/\`) {
		return resolveAddonPath(root, name)
	}

	roots := addonsRoots(cfg, root)
	if dir, ok := lookupAddon(roots, name); ok {
		return dir, name, nil
	}

	var hits []string
	for _, r := range discoverAddonsRoots(root) {
		if isManifestDir(filepath.Join(r, name)) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return "", "", fmt.Errorf("%w: %s", ErrModuleNotFound, name)
	case 1:
		return hits[0], name, nil
	default:
		return "", "", fmt.Errorf("%w: module %q is ambiguous — found in %s (name one addons path with `modules --addons-path`)",
			ErrUsage, name, strings.Join(relTo(root, hits), ", "))
	}
}

// addonError turns a lookup miss into the usage error the commands report,
// and passes through the resolver's own usage errors (ambiguity, bad path)
// unchanged.
func addonError(root, name string, err error) error {
	if errors.Is(err, ErrModuleNotFound) {
		return fmt.Errorf("%w: module %q is not an addon in %s (no __manifest__.py)", ErrUsage, name, root)
	}
	return err
}

// resolveAddonPath handles an argument that carries a separator, e.g. the
// result of shell tab-completion.
func resolveAddonPath(root, name string) (dir, module string, err error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) {
		return "", "", fmt.Errorf("%w: module %q must be a name or a path inside %s", ErrUsage, name, root)
	}
	full := filepath.Join(root, clean)
	rel, rerr := filepath.Rel(root, full)
	if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: module %q escapes %s", ErrUsage, name, root)
	}
	if !isManifestDir(full) {
		return "", "", fmt.Errorf("%w: module %q is not an addon in %s (no __manifest__.py)", ErrUsage, name, root)
	}
	return absOrSelf(filepath.Dir(full)), filepath.Base(full), nil
}

// addonFromPath returns the addon owning a repo-relative path: its module
// name and its repo-relative directory. Ancestors are probed shallowest
// first, so `group/mod/models/x.py` resolves to `mod` under `group/mod`
// just as `mod/x.py` resolves to `mod` under `mod`. Both are empty when no
// ancestor is an addon.
func addonFromPath(root, p string) (module, dir string) {
	parts := strings.Split(filepath.ToSlash(p), "/")
	if len(parts) > discoveryMaxDepth {
		parts = parts[:discoveryMaxDepth]
	}
	for i := range parts {
		if parts[i] == "" {
			return "", ""
		}
		rel := strings.Join(parts[:i+1], "/")
		if isManifestDir(filepath.Join(root, filepath.FromSlash(rel))) {
			return parts[i], rel
		}
	}
	return "", ""
}

// hasAddon reports whether an addon by that name exists anywhere the
// project resolves modules from. The conventional roots are stated first
// so the common layout costs a stat instead of a walk.
func hasAddon(root, name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, sub := range defaultAddonsPaths {
		if isManifestDir(filepath.Join(root, sub, name)) {
			return true
		}
	}
	_, ok := lookupAddon(discoverAddonsRoots(root), name)
	return ok
}

// listAddons returns the module names available to the project, sorted and
// deduplicated. Like resolveAddon, the walk is a fallback: it runs only
// when the configured roots hold nothing.
func listAddons(cfg *config.Config, root string) []string {
	found := modulesUnder(addonsRoots(cfg, root))
	if len(found) == 0 {
		found = modulesUnder(discoverAddonsRoots(root))
	}
	return found
}

// modulesUnder returns the sorted, deduplicated addon names directly under
// the given directories.
func modulesUnder(roots []string) []string {
	seen := map[string]bool{}
	var found []string
	for _, dir := range roots {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || seen[name] {
				continue
			}
			if isManifestDir(filepath.Join(dir, name)) {
				seen[name] = true
				found = append(found, name)
			}
		}
	}
	sort.Strings(found)
	return found
}

// lookupAddon returns the first root directly holding the named addon.
func lookupAddon(roots []string, name string) (string, bool) {
	for _, r := range roots {
		if isManifestDir(filepath.Join(r, name)) {
			return r, true
		}
	}
	return "", false
}

func isManifestDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "__manifest__.py"))
	return err == nil
}

func absOrSelf(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// relTo renders dirs relative to root for display, keeping the absolute
// path when it lies outside.
func relTo(root string, dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		if rel, err := filepath.Rel(root, d); err == nil {
			out[i] = filepath.ToSlash(rel)
			continue
		}
		out[i] = d
	}
	return out
}
