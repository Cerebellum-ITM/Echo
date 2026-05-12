package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

const (
	iconFolder     = "\U000f024b" // md-folder
	iconFolderStar = "\U000f069d" // md-folder-star (auto-detected)
)

// Folder names always skipped when scanning the project root.
var skipDirs = map[string]bool{
	"node_modules": true, "bin": true, "__pycache__": true, ".git": true,
	".venv": true, "venv": true, "vendor": true, ".idea": true, ".vscode": true,
}

type ModulesOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	StreamOut func(string)
}

var (
	ErrNoOdooContainer = errors.New("no Odoo container configured — run `init` first")
	ErrNoDB            = errors.New("no database configured — run `init` first")
	ErrNoModulesGiven  = errors.New("no module names given")
)

func RunInstall(ctx context.Context, opts ModulesOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	withDemo := false
	modules := make([]string, 0, len(opts.Args))
	for _, a := range opts.Args {
		switch a {
		case "--with-demo":
			withDemo = true
		default:
			modules = append(modules, a)
		}
	}
	if len(modules) == 0 {
		return ErrNoModulesGiven
	}
	return runOdoo(ctx, opts, odoo.Install(opts.Cfg.DBName, modules, withDemo))
}

func RunUpdate(ctx context.Context, opts ModulesOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	all := false
	modules := make([]string, 0, len(opts.Args))
	for _, a := range opts.Args {
		switch a {
		case "--all":
			all = true
		default:
			modules = append(modules, a)
		}
	}
	if all {
		return runOdoo(ctx, opts, odoo.UpdateAll(opts.Cfg.DBName))
	}
	if len(modules) == 0 {
		return ErrNoModulesGiven
	}
	return runOdoo(ctx, opts, odoo.Update(opts.Cfg.DBName, modules))
}

func RunUninstall(ctx context.Context, opts ModulesOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	if len(opts.Args) == 0 {
		return ErrNoModulesGiven
	}
	return runOdoo(ctx, opts, odoo.Uninstall(opts.Cfg.DBName, opts.Args))
}

// RunModules lists modules from the configured addons paths. With
// --config, opens an interactive picker to choose which folders count as
// addons paths.
func RunModules(ctx context.Context, opts ModulesOpts) error {
	for _, a := range opts.Args {
		if a == "--config" {
			return runModulesConfig(opts)
		}
	}

	paths := opts.Cfg.AddonsPaths
	if len(paths) == 0 {
		// fallback: legacy hardcoded scan
		paths = []string{".", "addons", "custom"}
	}

	var found []string
	for _, sub := range paths {
		dir := filepath.Join(opts.Root, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			manifest := filepath.Join(dir, e.Name(), "__manifest__.py")
			if _, err := os.Stat(manifest); err == nil {
				found = append(found, e.Name())
			}
		}
	}
	if len(found) == 0 {
		if opts.StreamOut != nil {
			opts.StreamOut("(no modules found — run `modules --config` to set addons paths)")
		}
		return nil
	}
	sort.Strings(found)
	dedupe := make([]string, 0, len(found))
	last := ""
	for _, m := range found {
		if m != last {
			dedupe = append(dedupe, m)
			last = m
		}
	}
	for _, m := range dedupe {
		opts.StreamOut(m)
	}
	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("(%d modules)", len(dedupe)))
	}
	return nil
}

// runModulesConfig opens a multiselect form to pick which folders at the
// project root are addons paths. Auto-detected paths are pre-selected and
// marked with a star icon.
func runModulesConfig(opts ModulesOpts) error {
	candidates, err := scanRootFolders(opts.Root)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		if opts.StreamOut != nil {
			opts.StreamOut("(no candidate folders in project root)")
		}
		return nil
	}

	autoSet := autoDetectAddons(opts.Root, candidates)

	// Pre-selection: previously configured ∪ auto-detected.
	selected := make(map[string]bool)
	for _, p := range opts.Cfg.AddonsPaths {
		selected[p] = true
	}
	for p := range autoSet {
		selected[p] = true
	}

	options := make([]huh.Option[string], len(candidates))
	for i, c := range candidates {
		icon := iconFolder
		if autoSet[c] {
			icon = iconFolderStar
		}
		opt := huh.NewOption(icon+"  "+displayName(c), c)
		if selected[c] {
			opt = opt.Selected(true)
		}
		options[i] = opt
	}

	picked := []string{}
	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title(iconFolderStar + "  Addons paths").
			Description("Pick folders that contain Odoo modules.\nStar = auto-detected (name starts with 'addons' or contains a module).\nSpace toggles, Enter confirms.").
			Options(options...).
			Value(&picked),
	)).
		WithTheme(BuildHuhTheme(opts.Palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)

	if err := form.Run(); err != nil {
		return err
	}

	opts.Cfg.AddonsPaths = picked
	if err := config.SaveProject(opts.Cfg); err != nil {
		return err
	}
	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("✓ saved %d addons paths", len(picked)))
		for _, p := range picked {
			opts.StreamOut("  " + iconFolder + "  " + displayName(p))
		}
	}
	return nil
}

// scanRootFolders returns visible directories at the project root,
// filtering out hidden and vendored folders. The project root itself is
// included as ".".
func scanRootFolders(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	folders := []string{"."}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		folders = append(folders, name)
	}
	return folders, nil
}

// autoDetectAddons returns the candidates that look like addons paths:
// name matches "addons*" (case-insensitive) or contains at least one
// subdir with __manifest__.py.
func autoDetectAddons(root string, candidates []string) map[string]bool {
	out := make(map[string]bool)
	for _, c := range candidates {
		dir := filepath.Join(root, c)
		if strings.HasPrefix(strings.ToLower(filepath.Base(c)), "addons") {
			out[c] = true
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "__manifest__.py")); err == nil {
				out[c] = true
				break
			}
		}
	}
	return out
}

func displayName(p string) string {
	if p == "." {
		return ". (project root)"
	}
	return p
}

func runOdoo(ctx context.Context, opts ModulesOpts, argv odoo.Cmd) error {
	return docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, argv, opts.StreamOut)
}

func requireOdooConfig(cfg *config.Config) error {
	if cfg.OdooContainer == "" {
		return ErrNoOdooContainer
	}
	if cfg.DBName == "" {
		return ErrNoDB
	}
	return nil
}
