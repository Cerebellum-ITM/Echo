package cmd

import (
	"context"
	"crypto/md5"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/odoo"
)

// File statuses in a whole-module compare. Ordered for the table sort:
// changed first, then added, then missing (equal is counted, not listed).
const (
	statusChanged = "changed"
	statusAdded   = "added"
	statusMissing = "missing"
)

// statusRank fixes the table's primary sort key.
var statusRank = map[string]int{statusChanged: 0, statusAdded: 1, statusMissing: 2}

// FileStatus is one module-relative file's sync status vs the container.
type FileStatus struct {
	Rel    string
	Status string
}

// CompareAllResult is the outcome of a whole-module compare: the non-equal
// rows (sorted), the equal-file count, and the labels the REPL needs to
// render the table, verdict frame, and drill-down.
type CompareAllResult struct {
	Module string
	From   string
	Rows   []FileStatus
	Equal  int
	Copy   bool
}

// RunCompareAll hashes an entire local module and its container copy and
// reports each file's status (changed/added/missing/equal). Read-only on
// both sides — no prod gate. The interactive drill-down over the differing
// files lives in the REPL layer.
func RunCompareAll(ctx context.Context, opts CompareOpts) (CompareAllResult, error) {
	module, copyFlag, _, from, remote, err := parseCompareArgs(opts.Args)
	if err != nil {
		return CompareAllResult{}, err
	}
	isRemote := from != "" || remote

	if !isRemote && opts.Cfg.OdooContainer == "" {
		return CompareAllResult{}, ErrNoOdooContainer
	}

	vopts := ViewOpts{Cfg: opts.Cfg, Root: opts.Root, Args: opts.Args, Palette: opts.Palette}
	if module == "" {
		module, err = pickViewModule(ctx, vopts)
		if err != nil {
			return CompareAllResult{}, err
		}
	}

	addonsDir, err := resolveModuleDir(opts.Cfg, opts.Root, module)
	if err != nil {
		return CompareAllResult{}, fmt.Errorf("module %q not found in local addons paths", module)
	}
	moduleDir := filepath.Join(addonsDir, module)
	local, err := localModuleHashes(moduleDir)
	if err != nil {
		return CompareAllResult{}, err
	}
	if len(local) == 0 {
		return CompareAllResult{}, fmt.Errorf("no files found for module %q", module)
	}

	var (
		container map[string]string
		fromLabel string
	)
	if isRemote {
		rv, rerr := resolveRemoteView(ctx, vopts, from)
		if rerr != nil {
			return CompareAllResult{}, rerr
		}
		fromLabel = rv.displayFrom()
		container, _, err = remoteModuleHashes(ctx, rv, module)
	} else {
		fromLabel = "docker"
		container, _, err = containerModuleHashes(ctx, vopts, module)
	}
	if err != nil {
		return CompareAllResult{}, err
	}

	rows, equal := diffModuleSets(local, container)
	return CompareAllResult{
		Module: module,
		From:   fromLabel,
		Rows:   rows,
		Equal:  equal,
		Copy:   copyFlag,
	}, nil
}

// CompareModuleFile diffs a single module-relative file against its
// container copy — the drill-down entry point after RunCompareAll. A file
// absent locally (a `missing` row) diffs an empty local side; one absent in
// the container (`added`) diffs an empty container side.
func CompareModuleFile(ctx context.Context, opts CompareOpts, module, rel string) (CompareResult, error) {
	_, _, _, from, remote, err := parseCompareArgs(opts.Args)
	if err != nil {
		return CompareResult{}, err
	}
	isRemote := from != "" || remote

	vopts := ViewOpts{Cfg: opts.Cfg, Root: opts.Root, Args: opts.Args, Palette: opts.Palette}
	addonsDir, err := resolveModuleDir(opts.Cfg, opts.Root, module)
	if err != nil {
		return CompareResult{}, fmt.Errorf("module %q not found in local addons paths", module)
	}
	moduleDir := filepath.Join(addonsDir, module)

	local := ""
	if b, rerr := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(rel))); rerr == nil {
		local = string(b)
	}

	container, found, fromLabel, err := compareFetchContainer(ctx, vopts, module, rel, from, isRemote)
	if err != nil {
		return CompareResult{}, err
	}

	fromFile := fromLabel + "/" + module + "/" + rel
	toFile := "local/" + module + "/" + rel
	diff := unifiedDiff(container, local, fromFile, toFile)
	return CompareResult{
		Module:             module,
		RelPath:            rel,
		From:               fromLabel,
		Diff:               diff,
		Identical:          diff == "",
		MissingInContainer: !found,
	}, nil
}

// diffModuleSets compares two hash maps (module-relative path → md5) and
// returns the non-equal rows (sorted by status then path) plus the count of
// files identical on both sides.
func diffModuleSets(local, container map[string]string) (rows []FileStatus, equal int) {
	for rel, lh := range local {
		ch, ok := container[rel]
		switch {
		case !ok:
			rows = append(rows, FileStatus{rel, statusAdded})
		case ch != lh:
			rows = append(rows, FileStatus{rel, statusChanged})
		default:
			equal++
		}
	}
	for rel := range container {
		if _, ok := local[rel]; !ok {
			rows = append(rows, FileStatus{rel, statusMissing})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if statusRank[rows[i].Status] != statusRank[rows[j].Status] {
			return statusRank[rows[i].Status] < statusRank[rows[j].Status]
		}
		return rows[i].Rel < rows[j].Rel
	})
	return rows, equal
}

// localModuleHashes walks a module directory and MD5s each file in-process,
// keyed by module-relative slash path, skipping build/VCS noise
// (__pycache__/.git and skipViewPath) exactly like hostModuleFiles.
func localModuleHashes(moduleDir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(moduleDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" || d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(moduleDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skipViewPath(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", md5.Sum(b))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// containerModuleHashes hashes a module's files inside the LOCAL Odoo
// container in one `find -exec md5sum` exec. present is false (empty map,
// no error) when the module isn't in the container — every local file then
// reads as `added`.
func containerModuleHashes(ctx context.Context, vopts ViewOpts, module string) (map[string]string, bool, error) {
	exists := func(p string) bool {
		return docker.Exec(ctx, vopts.Cfg.ComposeCmd, vopts.Root, vopts.Cfg.OdooContainer,
			[]string{"test", "-f", p}, func(string) {}) == nil
	}
	for _, b := range containerAddonsPathsFor(ctx, vopts) {
		dir := strings.TrimRight(b, "/") + "/" + module
		if !exists(dir + "/__manifest__.py") {
			continue
		}
		var sb strings.Builder
		err := docker.Exec(ctx, vopts.Cfg.ComposeCmd, vopts.Root, vopts.Cfg.OdooContainer,
			[]string{"sh", "-c", "find " + shellQuote(dir) + " -type f -exec md5sum {} +"},
			func(line string) { sb.WriteString(line); sb.WriteByte('\n') })
		if err != nil {
			return nil, false, err
		}
		return parseMD5Sums(sb.String(), dir+"/"), true, nil
	}
	return map[string]string{}, false, nil
}

// remoteModuleHashes hashes a module's files on the remote target in one
// SSH command, over the host/container transport Unit 79 resolves. present
// is false when the module isn't on the target.
func remoteModuleHashes(ctx context.Context, rv remoteView, module string) (map[string]string, bool, error) {
	base, inContainer, err := remoteModuleBase(ctx, rv, module)
	if err != nil {
		return map[string]string{}, false, nil // absent → all local files are `added`
	}
	dir := remoteModuleDir(rv, base, module, inContainer)
	findCmd := "find " + shellQuote(dir) + " -type f -exec md5sum {} +"
	var out []byte
	if inContainer {
		out, err = runSSH(ctx, rv.rsc.sshHost,
			remoteContainerCmd(rv.rsc.remotePath, rv.rsc.target, odoo.Cmd{"sh", "-c", findCmd}), nil)
	} else {
		out, err = runSSH(ctx, rv.rsc.sshHost, findCmd, nil)
	}
	if err != nil {
		return nil, false, err
	}
	return parseMD5Sums(string(out), dir+"/"), true, nil
}

// parseMD5Sums parses `md5sum` output (hash, whitespace, absolute path) into
// a module-relative path → hash map: it trims prefix from each path and
// drops build/VCS noise via skipViewPath. Handles both coreutils and
// BusyBox spacing.
func parseMD5Sums(out, prefix string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.IndexAny(line, " \t")
		if idx <= 0 {
			continue
		}
		hash := line[:idx]
		path := strings.TrimLeft(line[idx:], " \t*") // '*' = md5sum binary-mode marker
		rel := strings.TrimPrefix(path, prefix)
		if rel == path || rel == "" || skipViewPath(rel) {
			continue
		}
		m[rel] = hash
	}
	return m
}
