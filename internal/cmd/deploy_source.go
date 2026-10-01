package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
)

// lockSourceRef marks a module pinned to an explicit ref (`mod@ref`, `--at`).
const lockSourceRef = "ref"

// moduleSource is a module shipped from a commit's tree instead of the working
// tree: kind is lockSourceCommit or lockSourceRef, path the module directory
// inside that commit.
type moduleSource struct {
	kind string
	sha  string
	ref  string
	path string
}

// splitModuleRefs separates `--modules` entries of the form `mod@ref` into the
// bare names and the refs they are pinned to; at pins every entry that names
// no ref of its own.
func splitModuleRefs(entries []string, at string) ([]string, map[string]string, error) {
	var names []string
	var refs map[string]string
	for _, e := range entries {
		name, ref, pinned := strings.Cut(e, "@")
		name, ref = strings.TrimSpace(name), strings.TrimSpace(ref)
		if pinned && ref == "" {
			return nil, nil, fmt.Errorf("%w: %s: empty ref after @", ErrUsage, e)
		}
		if !pinned {
			ref = at
		}
		if ref != "" {
			if refs == nil {
				refs = map[string]string{}
			}
			if prev, dup := refs[name]; dup && prev != ref {
				return nil, nil, fmt.Errorf("%w: %s is pinned to both %s and %s", ErrUsage, name, prev, ref)
			}
			refs[name] = ref
		}
		names = append(names, name)
	}
	return names, refs, nil
}

// resolveModuleRefs resolves every pinned module to the commit its ref names
// and to the module's directory in that commit's tree. Each distinct ref is
// resolved (and fetched, per --fetch/--no-fetch) once.
func resolveModuleRefs(ctx context.Context, opts DeployOpts, p deployArgs) (map[string]moduleSource, error) {
	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	names := make([]string, 0, len(p.moduleRefs))
	for name := range p.moduleRefs {
		names = append(names, name)
	}
	sort.Strings(names)
	resolved := map[string]refResolution{}
	out := make(map[string]moduleSource, len(names))
	for _, name := range names {
		ref := p.moduleRefs[name]
		r, ok := resolved[ref]
		if !ok {
			var err error
			if r, err = resolveLocalRef(ctx, logFn, opts.Root, ref, p.fetch, p.noFetch); err != nil {
				return nil, err
			}
			resolved[ref] = r
		}
		dir, err := locateModuleAt(ctx, opts.Cfg, opts.Root, r.sha, name)
		if err != nil {
			return nil, err
		}
		out[name] = moduleSource{kind: lockSourceRef, sha: r.sha, ref: ref, path: dir}
		opts.log("INFO", "", "resolved", "",
			[2]string{"module", name}, [2]string{"via", "ref"},
			[2]string{"ref", ref}, [2]string{"sha", shortSHA(r.sha)})
	}
	return out, nil
}

// locateModuleAt finds a module's directory in the tree of sha: the path it
// has on disk when that path holds the module there too, otherwise the one
// directory at depth one or two named after it that holds a __manifest__.py.
func locateModuleAt(ctx context.Context, cfg *config.Config, root, sha, module string) (string, error) {
	if p, err := moduleRepoPath(cfg, root, module); err == nil {
		if _, err := gitOutput(ctx, root, "cat-file", "-e", sha+":"+p+"/__manifest__.py"); err == nil {
			return p, nil
		}
	}
	out, err := gitOutput(ctx, root, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return "", fmt.Errorf("list the tree of %s: %w", shortSHA(sha), err)
	}
	var found []string
	for _, f := range nonEmptyLines(string(out)) {
		dir, file := path.Split(f)
		dir = strings.TrimSuffix(dir, "/")
		if file == "__manifest__.py" && path.Base(dir) == module && strings.Count(dir, "/") <= 1 {
			found = append(found, dir)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("%w: module %s does not exist at %s", ErrUsage, module, shortSHA(sha))
	default:
		return "", fmt.Errorf("%w: module %s is in several places at %s: %s",
			ErrUsage, module, shortSHA(sha), strings.Join(found, ", "))
	}
}

// extractCommitTrees unpacks the given paths as committed at sha into dir,
// mirroring the repository layout so several commits can share one dir.
func extractCommitTrees(ctx context.Context, root, dir, sha string, paths []string) error {
	tarBytes, err := gitOutput(ctx, root, append([]string{"archive", "--format=tar", sha, "--"}, paths...)...)
	if err != nil {
		return fmt.Errorf("git archive %s: %w", shortSHA(sha), err)
	}
	return extractTar(tarBytes, dir)
}

// archiveModuleSources extracts every module shipped from a commit into one
// scratch dir and returns it with its cleanup.
func archiveModuleSources(ctx context.Context, root string, sources map[string]moduleSource) (string, func(), error) {
	dir, err := os.MkdirTemp("", "echo-ship-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	bySHA := map[string][]string{}
	for _, s := range sources {
		bySHA[s.sha] = append(bySHA[s.sha], s.path)
	}
	for sha, paths := range bySHA {
		if err := extractCommitTrees(ctx, root, dir, sha, paths); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}

// refTouchesI18n reports whether a module's i18n/ tree differs between the
// commit the lock says the target runs and sha. known is false when the lock
// offers nothing comparable: no entry, a working-tree entry (its content is
// not a commit), or a commit this repository does not have.
func refTouchesI18n(ctx context.Context, root string, prev LockModule, hasPrev bool, sha, modulePath string) (touched, known bool) {
	if !hasPrev || prev.Source == lockSourceWorktree || prev.SHA == "" {
		return false, false
	}
	if _, err := gitOutput(ctx, root, "cat-file", "-e", prev.SHA+"^{commit}"); err != nil {
		return false, false
	}
	treeAt := func(commit string) string {
		out, err := gitOutput(ctx, root, "rev-parse", "--verify", "--quiet", commit+":"+modulePath+"/i18n")
		if err != nil {
			return ""
		}
		return firstLine(string(out))
	}
	return treeAt(prev.SHA) != treeAt(sha), true
}
