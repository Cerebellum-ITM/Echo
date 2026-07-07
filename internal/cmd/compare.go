package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/theme"
	"github.com/pmezard/go-difflib/difflib"
)

// CompareOpts configures a `compare` invocation.
type CompareOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
}

// CompareResult is the computed diff of a local module file against its
// copy inside Docker.
type CompareResult struct {
	Module             string
	RelPath            string
	From               string // container source label: "docker" or the remote target
	Diff               string // unified diff; "" when the two sides are identical
	Identical          bool
	MissingInContainer bool // file absent in the container → all-`+` diff
	Copy               bool
}

// parseCompareArgs pulls the module positional and flags out of a `compare`
// argument list. The remote-mode switches (`--from <t>` / `--from=t` /
// `--remote`) are consumed here so the value token after a bare `--from` is
// not mistaken for the module name; any other `-`-prefixed token errors.
func parseCompareArgs(args []string) (module string, copyFlag bool, from string, remote bool, err error) {
	from, remote = remoteFlagsIn(args)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--copy":
			copyFlag = true
		case a == "--from":
			i++ // skip the target value; captured by remoteFlagsIn
		case strings.HasPrefix(a, "--from="), a == "--remote":
			// consumed by remoteFlagsIn
		case strings.HasPrefix(a, "-"):
			return "", false, "", false, fmt.Errorf("unknown flag: %s", a)
		default:
			if module == "" {
				module = a
			}
		}
	}
	return module, copyFlag, from, remote, nil
}

// unifiedDiff renders the git-style unified diff of oldText → newText with
// the given labels. The container copy is the old (left, `---`) side and
// the local checkout the new (right, `+++`) side, so `+` lines read as
// "local changes not yet in the container". Returns "" when identical.
func unifiedDiff(oldText, newText, fromFile, toFile string) string {
	out, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(oldText),
		B:        difflib.SplitLines(newText),
		FromFile: fromFile,
		ToFile:   toFile,
		Context:  3,
	})
	return out
}

// hostModuleFiles lists a module directory's files as module-relative,
// sorted paths, skipping build/VCS noise — the local-checkout counterpart
// of moduleFiles, taking an absolute module dir.
func hostModuleFiles(moduleDir string) ([]string, error) {
	var files []string
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
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// containerAddonsPathsFor returns the addons paths to probe inside the
// LOCAL Odoo container: the stored conf-mode paths when present, else the
// container's real odoo.conf addons_path (host-mode dev keeps only
// host-relative paths in config, which are meaningless inside the image).
func containerAddonsPathsFor(ctx context.Context, opts ViewOpts) []string {
	if opts.Cfg.AddonsMode == addonsModeConf && len(opts.Cfg.AddonsPaths) > 0 {
		return opts.Cfg.AddonsPaths
	}
	confPath := opts.Cfg.ConfPath
	if confPath == "" {
		confPath = "/etc/odoo/odoo.conf"
	}
	if conf, err := catContainer(ctx, opts.Cfg, opts.Root, confPath); err == nil {
		if cp := parseAddonsPath(conf); len(cp) > 0 {
			return cp
		}
	}
	return opts.Cfg.AddonsPaths
}

// localContainerRead reads a module file from inside the LOCAL Odoo
// container. found is false (with no error) when the module or the file is
// absent in the container — the caller renders that as an all-`+` diff.
func localContainerRead(ctx context.Context, opts ViewOpts, module, rel string) (content string, found bool, err error) {
	exists := func(p string) bool {
		return docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer,
			[]string{"test", "-f", p}, func(string) {}) == nil
	}
	for _, b := range containerAddonsPathsFor(ctx, opts) {
		dir := strings.TrimRight(b, "/") + "/" + module
		if !exists(dir + "/__manifest__.py") {
			continue
		}
		filePath := dir + "/" + rel
		if !exists(filePath) {
			return "", false, nil // module present, this file isn't
		}
		c, e := catContainer(ctx, opts.Cfg, opts.Root, filePath)
		if e != nil {
			return "", false, e
		}
		return c, true, nil
	}
	return "", false, nil // module not found in the container
}

// RunCompare resolves a local module file and diffs it against its Docker
// copy: the local Odoo container by default, or a remote target's container
// with `--from <t>` / `--remote`. Read-only on both sides — no prod gate.
func RunCompare(ctx context.Context, opts CompareOpts) (CompareResult, error) {
	module, copyFlag, from, remote, err := parseCompareArgs(opts.Args)
	if err != nil {
		return CompareResult{}, err
	}
	isRemote := from != "" || remote

	if !isRemote && opts.Cfg.OdooContainer == "" {
		return CompareResult{}, ErrNoOdooContainer
	}

	vopts := ViewOpts{Cfg: opts.Cfg, Root: opts.Root, Args: opts.Args, Palette: opts.Palette}
	if module == "" {
		module, err = pickViewModule(ctx, vopts)
		if err != nil {
			return CompareResult{}, err
		}
	}

	// The local checkout is the subject: resolve it, list its files, pick one.
	addonsDir, err := resolveModuleDir(opts.Cfg, opts.Root, module)
	if err != nil {
		return CompareResult{}, fmt.Errorf("module %q not found in local addons paths", module)
	}
	moduleDir := filepath.Join(addonsDir, module)
	files, err := hostModuleFiles(moduleDir)
	if err != nil {
		return CompareResult{}, err
	}
	if len(files) == 0 {
		return CompareResult{}, fmt.Errorf("no files found for module %q", module)
	}
	rel, err := runSingleFuzzyPicker("File in "+module, files, opts.Palette)
	if err != nil {
		return CompareResult{}, err
	}
	localBytes, err := os.ReadFile(filepath.Join(moduleDir, filepath.FromSlash(rel)))
	if err != nil {
		return CompareResult{}, err
	}
	local := string(localBytes)

	// Fetch the container counterpart (old side).
	var (
		container string
		found     bool
		fromLabel string
	)
	if isRemote {
		rv, rerr := resolveRemoteView(ctx, vopts, from)
		if rerr != nil {
			return CompareResult{}, rerr
		}
		fromLabel = rv.displayFrom()
		if base, inContainer, berr := remoteModuleBase(ctx, rv, module); berr == nil {
			if c, cerr := remoteReadModuleFile(ctx, rv, base, module, inContainer, rel); cerr == nil {
				container, found = c, true
			}
		}
	} else {
		fromLabel = "docker"
		container, found, err = localContainerRead(ctx, vopts, module, rel)
		if err != nil {
			return CompareResult{}, err
		}
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
		Copy:               copyFlag,
	}, nil
}
