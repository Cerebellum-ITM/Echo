package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoolint"
)

// LintOpts configures a `lint` run.
type LintOpts struct {
	Cfg  *config.Config
	Root string
	Args []string
}

// LintResult is a lint run's outcome, ready to render.
type LintResult struct {
	Findings []odoolint.Finding
	Files    int
	// Modules names the modules that were linted, empty for a whole-tree
	// or explicit-file run.
	Modules []string
	// GrammarMajor is the Odoo major whose grammar was used, and
	// GrammarExact reports whether it matched the configured version.
	GrammarMajor string
	GrammarExact bool
	// SkippedPasses names passes that did not run. Rendering these is not
	// optional: a degraded run that reports "clean" is a lie.
	SkippedPasses []string
	JSON          bool
}

// Errors returns the count of blocking findings, which is what the exit
// status maps from.
func (r LintResult) Errors() int {
	n := 0
	for _, f := range r.Findings {
		if f.Kind == odoolint.KindErr {
			n++
		}
	}
	return n
}

// Warnings returns the count of non-blocking findings.
func (r LintResult) Warnings() int { return len(r.Findings) - r.Errors() }

// lintArgs is the parsed form of `lint`'s arguments.
type lintArgs struct {
	jsonOut bool
	// modules are positional module names; files are positional paths.
	// They are mutually compatible — a hook passes a path, a human
	// usually passes a module.
	modules []string
	files   []string
}

// parseLintArgs splits `lint`'s arguments into flags, module names and
// paths. A positional that exists on disk is a path; anything else is
// taken as a module name and validated later.
func parseLintArgs(args []string) (lintArgs, error) {
	var p lintArgs
	for _, a := range args {
		switch {
		case a == "--json":
			p.jsonOut = true
		case strings.HasPrefix(a, "-"):
			return lintArgs{}, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			if _, err := os.Stat(a); err == nil {
				p.files = append(p.files, a)
				continue
			}
			p.modules = append(p.modules, a)
		}
	}
	return p, nil
}

// hostAddonsRoots returns the absolute addons directories to scan on the
// host.
//
// Conf mode's AddonsPaths are paths *inside* the container and cannot be
// walked here, so that mode falls back to the conventional host layout.
// Linting is about the files you are going to deploy, which live on the
// host by definition.
func hostAddonsRoots(cfg *config.Config, root string) []string {
	paths := cfg.AddonsPaths
	if cfg.AddonsMode == addonsModeConf || len(paths) == 0 {
		paths = []string{".", "addons", "custom"}
	}
	var out []string
	for _, sub := range paths {
		dir := filepath.Join(root, sub)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

// moduleDir resolves a module name to its directory under the addons
// roots, or "" when no such module exists.
func moduleDir(roots []string, name string) string {
	for _, r := range roots {
		dir := filepath.Join(r, name)
		if _, err := os.Stat(filepath.Join(dir, "__manifest__.py")); err == nil {
			return dir
		}
	}
	return ""
}

// RunLint checks Odoo XML the way the server does and returns the
// findings. It touches no container, no database and no network, so it is
// safe to run on every file save.
func RunLint(opts LintOpts) (LintResult, error) {
	p, err := parseLintArgs(opts.Args)
	if err != nil {
		return LintResult{}, err
	}

	roots := hostAddonsRoots(opts.Cfg, opts.Root)

	// Resolve every target before doing any work. An unknown module name
	// must be a usage error, never an empty run that reports "0 problems"
	// and reads as success.
	var targets []string
	var modules []string
	for _, m := range p.modules {
		dir := moduleDir(roots, m)
		if dir == "" {
			return LintResult{}, fmt.Errorf("%w: module %q is not an addon in %s (no __manifest__.py)",
				ErrUsage, m, opts.Root)
		}
		targets = append(targets, dir)
		modules = append(modules, m)
	}
	targets = append(targets, p.files...)

	if len(targets) == 0 {
		if len(roots) == 0 {
			return LintResult{}, fmt.Errorf("%w: no addons paths to lint under %s", ErrUsage, opts.Root)
		}
		targets = roots
	}
	sort.Strings(modules)

	grammar, major, exact := odoolint.Grammar(opts.Cfg.OdooVersion)

	// The severity set covers the modules of every root, not just the
	// linted targets: an explicit file path may well belong to a module
	// the caller did not name, and it still needs the right severity.
	manifestRoots := roots
	if len(manifestRoots) == 0 {
		manifestRoots = targets
	}

	res, err := odoolint.Check(targets, odoolint.Options{
		Grammar:     grammar,
		Xmllint:     odoolint.FindXmllint(),
		ManifestSet: odoolint.ManifestSet(odoolint.ModuleDirs(manifestRoots)),
	})
	if err != nil {
		return LintResult{}, err
	}

	return LintResult{
		Findings:      res.Findings,
		Files:         res.Files,
		Modules:       modules,
		GrammarMajor:  major,
		GrammarExact:  exact,
		SkippedPasses: res.SkippedPasses,
		JSON:          p.jsonOut,
	}, nil
}

// LintModules runs the lint over a named set of modules and returns the
// findings, for callers that already know their scope — `deploy`'s
// pre-flight, which lints exactly what it is about to send.
//
// It reports the modules it could not locate rather than failing: a
// deploy selection can legitimately name a module that only exists on
// the server (a rename in flight), and refusing to deploy over that
// would be the linter inventing a new failure mode.
func LintModules(cfg *config.Config, root string, modules []string) (LintResult, []string, error) {
	roots := hostAddonsRoots(cfg, root)

	var targets, found, missing []string
	for _, m := range modules {
		dir := moduleDir(roots, m)
		if dir == "" {
			missing = append(missing, m)
			continue
		}
		targets = append(targets, dir)
		found = append(found, m)
	}
	if len(targets) == 0 {
		return LintResult{}, missing, nil
	}

	grammar, major, exact := odoolint.Grammar(cfg.OdooVersion)
	res, err := odoolint.Check(targets, odoolint.Options{
		Grammar:     grammar,
		Xmllint:     odoolint.FindXmllint(),
		ManifestSet: odoolint.ManifestSet(targets),
	})
	if err != nil {
		return LintResult{}, missing, err
	}

	return LintResult{
		Findings:      res.Findings,
		Files:         res.Files,
		Modules:       found,
		GrammarMajor:  major,
		GrammarExact:  exact,
		SkippedPasses: res.SkippedPasses,
	}, missing, nil
}

// RelPath renders p relative to root when it is inside it, so findings
// read as the paths the user typed rather than absolute noise.
func RelPath(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}
