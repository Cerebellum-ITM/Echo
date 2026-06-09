package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// buildConn constructs the Odoo DB connection flags from cfg + project
// .env. Host defaults to the configured DB container's compose service
// name (resolved on the docker network); user/password/port come from
// POSTGRES_USER / POSTGRES_PASSWORD / POSTGRES_PORT in .env.
func buildConn(opts ModulesOpts) odoo.Conn {
	envVars := env.Load(opts.Root)
	return odoo.Conn{
		DB:       opts.Cfg.DBName,
		Host:     opts.Cfg.DBContainer,
		Port:     envVars["POSTGRES_PORT"],
		User:     envVars["POSTGRES_USER"],
		Password: envVars["POSTGRES_PASSWORD"],
	}
}

// tabToggleKeymap rebinds the multiselect so Tab toggles selection
// (instead of Space) and Enter alone submits.
func tabToggleKeymap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.MultiSelect.Toggle = key.NewBinding(
		key.WithKeys("tab", " "),
		key.WithHelp("tab", "toggle"),
	)
	km.MultiSelect.Next = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	)
	return km
}

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
	ErrNoOdooContainer    = errors.New("no Odoo container configured — run `init` first")
	ErrNoDB               = errors.New("no database configured — run `init` first")
	ErrNoModulesGiven     = errors.New("no module names given")
	ErrNoModulesAvailable = errors.New("no modules found — configure addons paths with `modules --config`")
)

// RunInstall returns the modules that were actually targeted (after
// flag stripping and picker resolution) along with any error from the
// Odoo subprocess. The caller uses the returned slice to label the
// finalize line / auto-copy log so the report always names the real
// modules, even when the user invoked the command with no args.
func RunInstall(ctx context.Context, opts ModulesOpts) ([]string, error) {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return nil, err
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
		picked, err := pickModulesInteractive(ctx, opts, "Modules to install")
		if err != nil {
			return nil, err
		}
		modules = picked
	}
	return modules, runOdoo(ctx, opts, odoo.Install(buildConn(opts), modules, withDemo))
}

// RunUpdate returns the resolved modules along with the run error.
// With --all the slice is the sentinel []string{"--all"} so the caller
// can render "all modules" in the summary.
func RunUpdate(ctx context.Context, opts ModulesOpts) ([]string, error) {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return nil, err
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
		return []string{"--all"}, runOdoo(ctx, opts, odoo.UpdateAll(buildConn(opts)))
	}
	if len(modules) == 0 {
		picked, err := pickModulesInteractive(ctx, opts, "Modules to update")
		if err != nil {
			return nil, err
		}
		modules = picked
	}
	return modules, runOdoo(ctx, opts, odoo.Update(buildConn(opts), modules))
}

// RunUninstall returns the resolved modules along with the run error.
func RunUninstall(ctx context.Context, opts ModulesOpts) ([]string, error) {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return nil, err
	}
	modules := opts.Args
	if len(modules) == 0 {
		picked, err := pickModulesInteractive(ctx, opts, "Modules to uninstall")
		if err != nil {
			return nil, err
		}
		modules = picked
	}
	return modules, runOdoo(ctx, opts, odoo.Uninstall(buildConn(opts), modules))
}

// RunTest runs the Odoo test suite for the given modules.
//
// Default mode filters via `--test-tags /<mod1>,/<mod2>` against the
// already-installed modules and does NOT pass `-u`, so Python test
// code is picked up fast (a fresh process imports the latest disk
// state under --stop-after-init). The `--update` flag opts into the
// `-u <mods>` reload for when views/schema changed. The `--tags`
// flag overrides the auto-generated filter with a user-supplied
// spec (e.g. `:TestClass.test_method`).
//
// Returns the resolved module list so the REPL layer can build the
// hierarchical logger name (echo.test.module.<mod>).
func RunTest(ctx context.Context, opts ModulesOpts) ([]string, error) {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return nil, err
	}

	var (
		modules []string
		tags    string
		update  bool
	)
	args := opts.Args
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--tags":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--tags requires a value")
			}
			tags = args[i+1]
			i++
		case strings.HasPrefix(a, "--tags="):
			tags = strings.TrimPrefix(a, "--tags=")
		case a == "--update":
			update = true
		case strings.HasPrefix(a, "-"):
			// forward-compat: ignore unknown flags instead of failing
		default:
			modules = append(modules, a)
		}
	}

	if len(modules) == 0 {
		picked, err := pickModulesInteractive(ctx, opts, "Modules to test")
		if err != nil {
			return nil, err
		}
		modules = picked
	}
	return modules, runOdoo(ctx, opts, odoo.Test(buildConn(opts), odoo.TestOpts{
		Modules: modules,
		Tags:    tags,
		Update:  update,
	}))
}

// pickModulesInteractive opens an fzf-style fuzzy picker with always-on
// filter for the available modules (resolved from host folders or, in
// conf mode / as a fallback, from the instance's odoo.conf).
func pickModulesInteractive(ctx context.Context, opts ModulesOpts, title string) ([]string, error) {
	available, err := resolveModules(ctx, opts)
	if err != nil {
		return nil, err
	}
	if len(available) == 0 {
		return nil, ErrNoModulesAvailable
	}
	return runFuzzyPicker(title, available, opts.Palette)
}

// addons modes recorded in the per-project config.
const (
	addonsModeHost = "host"
	addonsModeConf = "conf"
)

// resolveModules returns the available module names for the project,
// dispatching on the addons mode:
//
//   - conf mode: re-read addons_path from the instance's odoo.conf inside
//     the container (live, so edits are picked up), refreshing the saved
//     paths when they changed, and list modules there.
//   - host mode (default): scan the configured host folders. If that
//     yields modules, return them.
//   - fallback: when the host scan is empty, probe the container's
//     odoo.conf. If it yields modules, switch the project to conf mode and
//     persist it, so subsequent runs skip the host scan.
//
// On any conf-read failure the host result is returned unchanged, so the
// existing ErrNoModulesAvailable / "(no modules found…)" paths are
// preserved verbatim.
func resolveModules(ctx context.Context, opts ModulesOpts) ([]string, error) {
	if opts.Cfg.AddonsMode == addonsModeConf {
		paths, mods, err := modulesFromConf(ctx, opts)
		if err != nil {
			return nil, err
		}
		if !equalStrings(paths, opts.Cfg.AddonsPaths) {
			opts.Cfg.AddonsPaths = paths
			_ = config.SaveProject(opts.Cfg)
		}
		return mods, nil
	}

	host := listAvailableModules(opts.Cfg, opts.Root)
	if len(host) > 0 {
		return host, nil
	}

	// Fallback: the host scan found nothing — try the instance's conf.
	paths, mods, err := modulesFromConf(ctx, opts)
	if err != nil || len(mods) == 0 {
		return host, nil
	}
	opts.Cfg.AddonsMode = addonsModeConf
	opts.Cfg.AddonsPaths = paths
	_ = config.SaveProject(opts.Cfg)
	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("(addons paths read from %s — %d paths, %d modules)",
			opts.Cfg.ConfPath, len(paths), len(mods)))
	}
	return mods, nil
}

// modulesFromConf reads odoo.conf from the Odoo container, parses its
// addons_path, and lists the modules in those container directories.
// Returns the parsed paths and the sorted module names.
func modulesFromConf(ctx context.Context, opts ModulesOpts) (paths, modules []string, err error) {
	conf, err := readContainerFile(ctx, opts, opts.Cfg.ConfPath)
	if err != nil {
		return nil, nil, err
	}
	paths = parseAddonsPath(conf)
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no addons_path in %s", opts.Cfg.ConfPath)
	}
	modules, err = listModulesInContainer(ctx, opts, paths)
	if err != nil {
		return nil, nil, err
	}
	return paths, modules, nil
}

// readContainerFile cats a file inside the Odoo container and returns its
// full contents. A missing file (non-zero exit) surfaces as an error.
func readContainerFile(ctx context.Context, opts ModulesOpts, path string) (string, error) {
	var b strings.Builder
	err := docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer,
		[]string{"cat", path}, func(line string) {
			b.WriteString(line)
			b.WriteByte('\n')
		})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

// parseAddonsPath extracts the addons_path entries from odoo.conf text.
// It finds the (first) line whose trimmed form starts with `addons_path`,
// splits the value after `=` on commas, and trims each entry. Comment
// lines (`#` / `;`) and section headers are ignored. Entries whose base
// name starts with "enterprise" (e.g. `enterprise`, `enterprise-addons`)
// are skipped by default — the Enterprise addons are noise in the module
// picker for most update/install workflows.
func parseAddonsPath(conf string) []string {
	for _, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if !strings.HasPrefix(line, "addons_path") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		var out []string
		for _, p := range strings.Split(line[eq+1:], ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(filepath.Base(p)), "enterprise") {
				continue // skip Enterprise addons dirs by default
			}
			out = append(out, p)
		}
		return out
	}
	return nil
}

// listModulesInContainer lists, inside the Odoo container, the directories
// under each addons path that contain a __manifest__.py — the same rule
// the host scan uses, one level deep. Names are deduplicated and sorted.
func listModulesInContainer(ctx context.Context, opts ModulesOpts, paths []string) ([]string, error) {
	const script = `for d in "$@"; do for m in "$d"/*/__manifest__.py; do [ -f "$m" ] && basename "$(dirname "$m")"; done; done`
	argv := append([]string{"sh", "-c", script, "_"}, paths...)

	seen := map[string]bool{}
	var found []string
	err := docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, argv,
		func(line string) {
			name := strings.TrimSpace(line)
			if name == "" || seen[name] {
				return
			}
			seen[name] = true
			found = append(found, name)
		})
	if err != nil {
		return nil, err
	}
	sort.Strings(found)
	return found, nil
}

// equalStrings reports whether two string slices are element-wise equal.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// listAvailableModules walks the configured addons paths (or defaults)
// one level deep and returns the names of directories containing
// __manifest__.py. Sorted and deduplicated.
func listAvailableModules(cfg *config.Config, root string) []string {
	paths := cfg.AddonsPaths
	if len(paths) == 0 {
		paths = []string{".", "addons", "custom"}
	}
	seen := map[string]bool{}
	var found []string
	for _, sub := range paths {
		dir := filepath.Join(root, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if seen[name] {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, name, "__manifest__.py")); err == nil {
				seen[name] = true
				found = append(found, name)
			}
		}
	}
	sort.Strings(found)
	return found
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

	found, err := resolveModules(ctx, opts)
	if err != nil {
		found = nil
	}
	if len(found) == 0 {
		if opts.StreamOut != nil {
			opts.StreamOut("(no modules found — run `modules --config` to set addons paths)")
		}
		return nil
	}
	for _, m := range found {
		opts.StreamOut(m)
	}
	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("(%d modules)", len(found)))
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
	opts.Cfg.AddonsMode = addonsModeHost // picking host folders pins host mode
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
