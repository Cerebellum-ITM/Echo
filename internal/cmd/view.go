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

// RunView resolves a module file to display: it picks the module (if not
// given), lists its files, picks one, and reads its content. A non-TTY
// caller without a module/file fails closed via the picker guard.
func RunView(ctx context.Context, opts ViewOpts) (ViewResult, error) {
	if opts.Cfg.OdooContainer == "" {
		return ViewResult{}, ErrNoOdooContainer
	}

	var module string
	copyFlag := false
	for _, a := range opts.Args {
		switch {
		case a == "--copy":
			copyFlag = true
		case strings.HasPrefix(a, "-"):
			return ViewResult{}, fmt.Errorf("unknown flag: %s", a)
		default:
			if module == "" {
				module = a
			}
		}
	}

	if module == "" {
		names, err := resolveModules(ctx, ModulesOpts{Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette})
		if err != nil || len(names) == 0 {
			return ViewResult{}, ErrNoModulesAvailable
		}
		picked, err := runSingleFuzzyPicker("Module to view", names, opts.Palette)
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

// RunViewLast re-reads a previously viewed module file directly, without
// the pickers — used by `view --last` to replay a file first reached
// interactively (e.g. to copy it). The content is read fresh.
func RunViewLast(ctx context.Context, opts ViewOpts, module, file string, copy bool) (ViewResult, error) {
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
