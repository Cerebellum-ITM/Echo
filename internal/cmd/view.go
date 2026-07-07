package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/theme"
)

// lookPath is the exec.LookPath seam, overridable in tests.
var lookPath = exec.LookPath

// ViewOpts configures a `view` invocation.
type ViewOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
}

// ViewResult is the resolved file to display or copy.
type ViewResult struct {
	Module  string
	RelPath string // path within the module, e.g. "models/sale.py"
	Content string
	Copy    bool
	From    string // resolved remote target label; "" for a local view
}

// skipViewPath reports whether a module-relative path is build/VCS noise
// that shouldn't appear in the file picker.
func skipViewPath(rel string) bool {
	return strings.Contains(rel, "__pycache__/") ||
		strings.Contains(rel, ".git/") ||
		strings.HasSuffix(rel, ".pyc")
}

// moduleBase returns the addons path holding <module>/__manifest__.py and
// whether it lives inside the container (conf mode) or on the host.
func moduleBase(ctx context.Context, opts ViewOpts, module string) (base string, inContainer bool, err error) {
	if opts.Cfg.AddonsMode == addonsModeConf {
		for _, b := range opts.Cfg.AddonsPaths {
			p := strings.TrimRight(b, "/") + "/" + module + "/__manifest__.py"
			if e := docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer,
				[]string{"test", "-f", p}, func(string) {}); e == nil {
				return b, true, nil
			}
		}
		return "", false, fmt.Errorf("module %q not found in addons paths", module)
	}
	paths := opts.Cfg.AddonsPaths
	if len(paths) == 0 {
		paths = []string{".", "addons", "custom"}
	}
	for _, b := range paths {
		if _, e := os.Stat(filepath.Join(opts.Root, b, module, "__manifest__.py")); e == nil {
			return b, false, nil
		}
	}
	return "", false, fmt.Errorf("module %q not found in addons paths", module)
}

// moduleFiles lists a module's files as module-relative, sorted paths,
// reading from the host or the container per inContainer.
func moduleFiles(ctx context.Context, opts ViewOpts, base, module string, inContainer bool) ([]string, error) {
	var files []string
	if inContainer {
		dir := strings.TrimRight(base, "/") + "/" + module
		prefix := dir + "/"
		err := docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer,
			[]string{"find", dir, "-type", "f"}, func(line string) {
				rel := strings.TrimPrefix(strings.TrimSpace(line), prefix)
				if rel == "" || skipViewPath(rel) {
					return
				}
				files = append(files, rel)
			})
		if err != nil {
			return nil, err
		}
	} else {
		root := filepath.Join(opts.Root, base, module)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "__pycache__" || d.Name() == ".git" {
					return fs.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, p)
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
	}
	sort.Strings(files)
	return files, nil
}

// readModuleFile returns the contents of a module-relative file.
func readModuleFile(ctx context.Context, opts ViewOpts, base, module string, inContainer bool, rel string) (string, error) {
	if inContainer {
		p := strings.TrimRight(base, "/") + "/" + module + "/" + rel
		return catContainer(ctx, opts.Cfg, opts.Root, p)
	}
	data, err := os.ReadFile(filepath.Join(opts.Root, base, module, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// parseViewArgs pulls the module positional and the flags out of a `view`
// argument list. The remote-mode switches (`--from <t>` / `--from=t` /
// `--remote`) are consumed here — the value token after a bare `--from`
// must not be mistaken for the module name — and returned via from/remote;
// any other `-`-prefixed token is an error.
func parseViewArgs(args []string) (module string, copyFlag bool, from string, remote bool, err error) {
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

// pickViewModule resolves the module to view: it lists the local checkout's
// modules (the code that corresponds to the deployment, same as remote
// `test`) and opens the single-select fuzzy picker.
func pickViewModule(ctx context.Context, opts ViewOpts) (string, error) {
	names, err := resolveModules(ctx, ModulesOpts{Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette})
	if err != nil || len(names) == 0 {
		return "", ErrNoModulesAvailable
	}
	return runSingleFuzzyPicker("Module to view", names, opts.Palette)
}

// RunView resolves a module file to display: it picks the module (if not
// given), lists its files, picks one, and reads its content. A non-TTY
// caller without a module/file fails closed via the picker guard. With
// `--from <t>` / `--remote` the file is browsed and read from the remote
// target over SSH instead of the local container.
func RunView(ctx context.Context, opts ViewOpts) (ViewResult, error) {
	module, copyFlag, from, remote, err := parseViewArgs(opts.Args)
	if err != nil {
		return ViewResult{}, err
	}
	if from != "" || remote {
		return runViewRemote(ctx, opts, from, module, copyFlag)
	}

	if opts.Cfg.OdooContainer == "" {
		return ViewResult{}, ErrNoOdooContainer
	}

	if module == "" {
		picked, err := pickViewModule(ctx, opts)
		if err != nil {
			return ViewResult{}, err
		}
		module = picked
	}

	base, inContainer, err := moduleBase(ctx, opts, module)
	if err != nil {
		return ViewResult{}, err
	}
	files, err := moduleFiles(ctx, opts, base, module, inContainer)
	if err != nil {
		return ViewResult{}, err
	}
	if len(files) == 0 {
		return ViewResult{}, fmt.Errorf("no files found for module %q", module)
	}

	rel, err := runSingleFuzzyPicker("File in "+module, files, opts.Palette)
	if err != nil {
		return ViewResult{}, err
	}

	content, err := readModuleFile(ctx, opts, base, module, inContainer, rel)
	if err != nil {
		return ViewResult{}, err
	}

	return ViewResult{Module: module, RelPath: rel, Content: content, Copy: copyFlag}, nil
}

// runViewRemote is RunView's remote branch: resolve the target, pick the
// module (from the local checkout) if none was given, then list and read
// the file from the remote deployment over SSH. No prod gate — view is
// read-only.
func runViewRemote(ctx context.Context, opts ViewOpts, from, module string, copyFlag bool) (ViewResult, error) {
	rv, err := resolveRemoteView(ctx, opts, from)
	if err != nil {
		return ViewResult{}, err
	}
	if module == "" {
		module, err = pickViewModule(ctx, opts)
		if err != nil {
			return ViewResult{}, err
		}
	}
	base, inContainer, err := remoteModuleBase(ctx, rv, module)
	if err != nil {
		return ViewResult{}, err
	}
	files, err := remoteModuleFiles(ctx, rv, base, module, inContainer)
	if err != nil {
		return ViewResult{}, err
	}
	if len(files) == 0 {
		return ViewResult{}, fmt.Errorf("no files found for module %q", module)
	}
	rel, err := runSingleFuzzyPicker("File in "+module, files, opts.Palette)
	if err != nil {
		return ViewResult{}, err
	}
	content, err := remoteReadModuleFile(ctx, rv, base, module, inContainer, rel)
	if err != nil {
		return ViewResult{}, err
	}
	return ViewResult{Module: module, RelPath: rel, Content: content, Copy: copyFlag, From: rv.displayFrom()}, nil
}

// RunViewLast re-reads a previously viewed module file directly, without
// the pickers — used by `view --last` to replay a file first reached
// interactively (e.g. to copy it). The content is read fresh, from the
// same source: local by default, or the remote target when from/remote is
// set.
func RunViewLast(ctx context.Context, opts ViewOpts, module, file string, copy bool, from string, remote bool) (ViewResult, error) {
	if from != "" || remote {
		rv, err := resolveRemoteView(ctx, opts, from)
		if err != nil {
			return ViewResult{}, err
		}
		base, inContainer, err := remoteModuleBase(ctx, rv, module)
		if err != nil {
			return ViewResult{}, err
		}
		content, err := remoteReadModuleFile(ctx, rv, base, module, inContainer, file)
		if err != nil {
			return ViewResult{}, err
		}
		return ViewResult{Module: module, RelPath: file, Content: content, Copy: copy, From: rv.displayFrom()}, nil
	}
	if opts.Cfg.OdooContainer == "" {
		return ViewResult{}, ErrNoOdooContainer
	}
	base, inContainer, err := moduleBase(ctx, opts, module)
	if err != nil {
		return ViewResult{}, err
	}
	content, err := readModuleFile(ctx, opts, base, module, inContainer, file)
	if err != nil {
		return ViewResult{}, err
	}
	return ViewResult{Module: module, RelPath: file, Content: content, Copy: copy}, nil
}

// batBinary returns "bat" or "batcat" when one is on PATH, else "".
func batBinary() string {
	for _, name := range []string{"bat", "batcat"} {
		if _, err := lookPath(name); err == nil {
			return name
		}
	}
	return ""
}

// ShowWithBat displays content through bat/batcat when available, piping
// the content on stdin and letting bat handle highlighting + paging.
// Returns shown=false when neither binary is on PATH, so the caller can
// fall back to an internal print. name is passed as --file-name so bat
// picks the syntax from its extension.
func ShowWithBat(name, content string) (shown bool, err error) {
	bin := batBinary()
	if bin == "" {
		return false, nil
	}
	c := exec.Command(bin, "--style=plain,header", "--paging=auto", "--file-name="+name)
	c.Stdin = strings.NewReader(content)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return true, c.Run()
}
