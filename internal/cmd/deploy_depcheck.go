package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/depcheck"
	"github.com/pascualchavez/echo/internal/theme"
)

// DependencyFinding is one use, in a module staying on the server, of a
// symbol a module this deploy ships no longer defines.
type DependencyFinding struct {
	Module string `json:"module"`
	Symbol string `json:"symbol"`
	Kind   string `json:"kind"`
	UsedBy string `json:"used_by"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// dependencyReport is the outcome of one dependency check: how many shipped
// modules already existed on the server, how many symbols they drop, and the
// uses of those symbols found in the modules that stay.
type dependencyReport struct {
	compared int
	removed  int
	findings []DependencyFinding
}

// depRunSSH is the transport of the dependency check, a seam for the tests.
var depRunSSH = runSSH

const (
	depGrepCap   = 100
	depShownRefs = 5
)

// checkDeployDependencies runs the dependency check over the modules this
// deploy ships and logs what it found in the plan. It never fails the deploy:
// when the check cannot run it says so and reports nothing.
func checkDeployDependencies(ctx context.Context, opts DeployOpts, rsc remoteShellContext, g gitDeployConfig, gitTip string, branchMods, worktreeMods []string, archived map[string]moduleSource, archiveDir string) []DependencyFinding {
	db := rsc.prof.DBName
	var report dependencyReport
	dests, newRoots, cleanup, err := shippedModuleTrees(ctx, opts, rsc, g, gitTip, branchMods, worktreeMods, archived, archiveDir)
	if err == nil {
		defer cleanup()
		shipped := slices.Sorted(maps.Keys(dests))
		report, err = runDependencyCheck(ctx, rsc, shipped, dests, newRoots)
	}
	if err != nil {
		opts.log("WARNING", "plan", "dependency check skipped", db, [2]string{"reason", err.Error()})
		return nil
	}
	if len(report.findings) == 0 {
		opts.log("INFO", "plan", "dependency check clean", db,
			[2]string{"modules", strconv.Itoa(report.compared)},
			[2]string{"removed", strconv.Itoa(report.removed)})
		return nil
	}
	logDependencyFindings(opts.Log, db, report.findings)
	return report.findings
}

// shippedModuleTrees resolves, for every module the deploy ships, the
// directory the push writes on the server and the local tree that replaces
// it. Modules riding the git deploy branch are archived at gitTip for the
// check only; the returned cleanup removes that scratch copy.
func shippedModuleTrees(ctx context.Context, opts DeployOpts, rsc remoteShellContext, g gitDeployConfig, gitTip string, branchMods, worktreeMods []string, archived map[string]moduleSource, archiveDir string) (dests, newRoots map[string]string, cleanup func(), err error) {
	dests, newRoots, cleanup = map[string]string{}, map[string]string{}, func() {}
	destBase := ""
	if dest, _, _ := resolvePushDest(pushArgs{}, rsc.prof, opts.Cfg); dest != "" {
		destBase = resolveDestPath(rsc.remotePath, dest)
	}
	pushOpts := PushOpts{Cfg: opts.Cfg, Root: opts.Root}
	rv := remoteView{rsc: rsc}
	for _, m := range worktreeMods {
		if dests[m], err = moduleDestDir(ctx, rv, pushOpts, destBase, m); err != nil {
			return nil, nil, cleanup, err
		}
		if newRoots[m], err = moduleSrcDir(opts.Cfg, opts.Root, m); err != nil {
			return nil, nil, cleanup, err
		}
	}
	for m, src := range archived {
		if dests[m], err = moduleDestDir(ctx, rv, pushOpts, destBase, m); err != nil {
			return nil, nil, cleanup, err
		}
		newRoots[m] = filepath.Join(archiveDir, filepath.FromSlash(src.path))
	}
	if gitTip == "" || len(branchMods) == 0 {
		return dests, newRoots, cleanup, nil
	}
	checkout := absGitDir(rsc.remotePath, g.path)
	atTip := map[string]moduleSource{}
	for _, m := range branchMods {
		dir, lerr := locateModuleAt(ctx, opts.Cfg, opts.Root, gitTip, m)
		if lerr != nil {
			return nil, nil, cleanup, lerr
		}
		atTip[m] = moduleSource{sha: gitTip, path: dir}
		dests[m] = path.Join(checkout, dir)
	}
	tipDir, tipCleanup, aerr := archiveModuleSources(ctx, opts.Root, atTip)
	if aerr != nil {
		return nil, nil, cleanup, aerr
	}
	for m, src := range atTip {
		newRoots[m] = filepath.Join(tipDir, filepath.FromSlash(src.path))
	}
	return dests, newRoots, tipCleanup, nil
}

// depQuery is one removed symbol of one shipped module, searched for in the
// staying modules.
type depQuery struct {
	module string
	symbol depcheck.Symbol
}

// runDependencyCheck compares each shipped module as it is on the server
// (dests) with the tree that replaces it (newRoots) and greps the modules
// staying next to them for the symbols that disappear. Two SSH round trips:
// one tar of the server's trees, one grep.
func runDependencyCheck(ctx context.Context, rsc remoteShellContext, shipped []string, dests, newRoots map[string]string) (dependencyReport, error) {
	scratch, err := os.MkdirTemp("", "echo-depcheck-*")
	if err != nil {
		return dependencyReport{}, err
	}
	defer os.RemoveAll(scratch)
	mirror := func(remote string) string {
		return filepath.Join(scratch, filepath.FromSlash(strings.TrimPrefix(remote, "/")))
	}

	isShipped := toStringSet(shipped)
	var parents []string
	for _, d := range dests {
		parents = append(parents, path.Dir(d))
	}
	slices.Sort(parents)
	parents = slices.Compact(parents)
	out, err := depRunSSH(ctx, rsc.sshHost, serverTreesScript(dests, parents), nil)
	if err != nil {
		return dependencyReport{}, fmt.Errorf("read the server's module trees: %w", err)
	}
	if err := extractTar(out, scratch); err != nil {
		return dependencyReport{}, fmt.Errorf("read the server's module trees: %w", err)
	}

	next := make(map[string]depcheck.Set, len(shipped))
	for _, m := range shipped {
		if next[m], err = depcheck.Symbols(newRoots[m]); err != nil {
			return dependencyReport{}, err
		}
	}
	var report dependencyReport
	var queries []depQuery
	for _, m := range shipped {
		oldRoot := mirror(dests[m])
		if info, serr := os.Stat(oldRoot); serr != nil || !info.IsDir() {
			continue
		}
		report.compared++
		old, serr := depcheck.Symbols(oldRoot)
		if serr != nil {
			return dependencyReport{}, serr
		}
		keep := depcheck.Set{}
		for _, other := range shipped {
			if other != m {
				for s := range next[other] {
					keep[s] = true
				}
			}
		}
		for _, s := range depcheck.Removed(old, next[m], keep) {
			queries = append(queries, depQuery{module: m, symbol: s})
		}
	}
	report.removed = len(queries)

	staying := map[string]string{}
	for _, parent := range parents {
		entries, _ := os.ReadDir(mirror(parent))
		for _, e := range entries {
			if _, serr := os.Stat(filepath.Join(mirror(parent), e.Name(), "__manifest__.py")); serr == nil && !isShipped[e.Name()] {
				staying[path.Join(parent, e.Name())] = e.Name()
			}
		}
	}
	if len(queries) == 0 || len(staying) == 0 {
		return report, nil
	}
	out, err = depRunSSH(ctx, rsc.sshHost, usesGrepScript(queries, staying), nil)
	if err != nil {
		return dependencyReport{}, fmt.Errorf("search the staying modules: %w", err)
	}
	sections := grepSections(string(out))
	for i, q := range queries {
		report.findings = append(report.findings, findingsFor(q, depcheck.ParseGrep(sections[i], staying))...)
	}
	return report, nil
}

// serverTreesScript tars, relative to /, the *.py and *.xml files of every
// shipped module's server directory plus the manifests of the modules next to
// them, which is how the check learns which modules stay. A directory that
// does not exist (a module being installed) is simply absent from the tar.
func serverTreesScript(dests map[string]string, parents []string) string {
	rel := func(paths []string) string {
		quoted := make([]string, len(paths))
		for i, p := range paths {
			quoted[i] = shellQuote(strings.TrimPrefix(p, "/"))
		}
		return strings.Join(quoted, " ")
	}
	trees := make([]string, 0, len(dests))
	for _, d := range dests {
		trees = append(trees, d)
	}
	sort.Strings(trees)
	return "cd / && { find -H " + rel(trees) + ` -type f \( -name '*.py' -o -name '*.xml' \); ` +
		"find -H " + rel(parents) + " -mindepth 2 -maxdepth 2 -name __manifest__.py; } 2>/dev/null | tar -cf - -T -"
}

// usesGrepScript runs one capped grep per removed symbol over the staying
// modules, each preceded by an @@<index> marker line so the output can be
// split back per symbol (grep's own lines start with an absolute path).
func usesGrepScript(queries []depQuery, staying map[string]string) string {
	dirs := make([]string, 0, len(staying))
	for d := range staying {
		dirs = append(dirs, shellQuote(d))
	}
	sort.Strings(dirs)
	var b strings.Builder
	for i, q := range queries {
		fmt.Fprintf(&b, "printf '@@%d\\n'; grep -rnwHE --include='*.py' --include='*.xml' -e %s -- %s 2>/dev/null | head -n %d; ",
			i, shellQuote(depcheck.GrepPattern([]depcheck.Symbol{q.symbol}, q.module)), strings.Join(dirs, " "), depGrepCap)
	}
	b.WriteString("exit 0")
	return b.String()
}

// grepSections splits usesGrepScript's output by its @@<index> markers.
func grepSections(out string) map[int]string {
	sections := map[int]string{}
	current := -1
	for _, line := range strings.Split(out, "\n") {
		if idx, ok := strings.CutPrefix(line, "@@"); ok {
			if n, err := strconv.Atoi(idx); err == nil {
				current = n
				continue
			}
		}
		if current >= 0 {
			sections[current] += line + "\n"
		}
	}
	return sections
}

// findingsFor turns the grep hits of one removed symbol into findings,
// dropping every module that defines the method or field itself.
func findingsFor(q depQuery, refs []depcheck.Ref) []DependencyFinding {
	provides := map[string]bool{}
	for _, r := range refs {
		if depcheck.Defines(q.symbol, r.Text) {
			provides[r.Module] = true
		}
	}
	var out []DependencyFinding
	for _, r := range refs {
		if provides[r.Module] {
			continue
		}
		out = append(out, DependencyFinding{
			Module: q.module, Symbol: q.symbol.Use(q.module), Kind: string(q.symbol.Kind),
			UsedBy: r.Module, File: r.File, Line: r.Line,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// logDependencyFindings prints one WARNING per removed symbol still in use,
// with its first locations and a count of the rest.
func logDependencyFindings(log logFn, db string, findings []DependencyFinding) {
	type key struct{ module, symbol, kind string }
	var order []key
	byKey := map[key][]DependencyFinding{}
	for _, f := range findings {
		k := key{f.Module, f.Symbol, f.Kind}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], f)
	}
	for _, k := range order {
		uses := byKey[k]
		var users, at []string
		for _, f := range uses {
			users = append(users, f.UsedBy)
			if len(at) < depShownRefs {
				at = append(at, f.File+":"+strconv.Itoa(f.Line))
			}
		}
		slices.Sort(users)
		fields := [][2]string{
			{"removed", k.symbol}, {"kind", k.kind}, {"from", k.module},
			{"used_by", strings.Join(slices.Compact(users), ",")}, {"at", strings.Join(at, ",")},
		}
		if more := len(uses) - len(at); more > 0 {
			fields = append(fields, [2]string{"more", strconv.Itoa(more)})
		}
		ckptLog(log, "WARNING", "plan", "dependency", db, fields...)
	}
}

// removedInUse counts the distinct removed symbols the findings cover.
func removedInUse(findings []DependencyFinding) int {
	seen := map[[2]string]bool{}
	for _, f := range findings {
		seen[[2]string{f.Module, f.Symbol}] = true
	}
	return len(seen)
}

// confirmDependencyRisk is the gate a staging or prod deploy passes when it
// removes symbols other modules still use: a confirm in a TTY, a fail-closed
// error without one.
func confirmDependencyRisk(palette theme.Palette, db string, removed int) error {
	if err := requireTTY("the deploy removes symbols modules on the server still use: pass --force to deploy anyway"); err != nil {
		return err
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("⚠  Deploy anyway? %d symbols removed while still used", removed)).
			Description("Modules staying on " + red + " still reference them: see the dependency lines above.").
			Affirmative("Deploy").
			Negative("Cancel").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}
