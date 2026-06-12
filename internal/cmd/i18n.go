package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

type I18nOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	StreamOut func(string)
}

var (
	ErrModuleNotFound     = errors.New("module not found in configured addons paths")
	ErrTranslationMissing = errors.New("no .po file found for this lang — run i18n-export first")
)

const defaultI18nLang = "es_MX"

// i18nArgs is the parsed shape of the user input for both i18n commands.
type i18nArgs struct {
	module      string
	lang        string
	outOverride string // export only
	force       bool   // update only
}

// parseI18nArgs walks args extracting --out, --force, and up to two
// positionals (module then lang). Unknown flags return an error.
func parseI18nArgs(args []string, allowOut, allowForce bool) (i18nArgs, error) {
	out := i18nArgs{lang: defaultI18nLang}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--out" && allowOut:
			if i+1 >= len(args) {
				return out, fmt.Errorf("--out requires a path")
			}
			out.outOverride = args[i+1]
			i++
		case strings.HasPrefix(a, "--out=") && allowOut:
			out.outOverride = strings.TrimPrefix(a, "--out=")
		case a == "--force" && allowForce:
			out.force = true
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("unknown flag: %s", a)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 2 {
		return out, fmt.Errorf("too many positional args; expected <mod> [lang]")
	}
	if len(positional) >= 1 {
		out.module = positional[0]
	}
	if len(positional) == 2 && positional[1] != "" {
		out.lang = positional[1]
	}
	return out, nil
}

// resolveModuleDir walks the configured addons paths one-deep looking
// for <mod>/__manifest__.py and returns the addons directory (the
// parent of the module folder). Returns ErrModuleNotFound on miss.
func resolveModuleDir(cfg *config.Config, root, mod string) (string, error) {
	paths := cfg.AddonsPaths
	if len(paths) == 0 {
		paths = []string{".", "addons", "custom"}
	}
	for _, sub := range paths {
		dir := filepath.Join(root, sub)
		manifest := filepath.Join(dir, mod, "__manifest__.py")
		if _, err := os.Stat(manifest); err == nil {
			abs, err := filepath.Abs(dir)
			if err != nil {
				return dir, nil
			}
			return abs, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrModuleNotFound, mod)
}

// defaultExportDest returns <addonsDir>/<mod>/i18n/<lang>.po. The
// caller is responsible for MkdirAll on the parent.
func defaultExportDest(addonsDir, mod, lang string) string {
	return filepath.Join(addonsDir, mod, "i18n", lang+".po")
}

// tmpPathInContainer returns a unique /tmp/echo-i18n-*.po path. Pure
// function — caller is responsible for creating and deleting the file
// inside the container.
func tmpPathInContainer() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("/tmp/echo-i18n-%d-%s.po", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

// tmpConfInContainer returns a unique /tmp/echo-i18n-*.conf path. Used on
// Odoo 19 to hold the db connection for the `odoo i18n` subcommand (which
// takes no --db_* flags). Pure function — caller creates and deletes it
// inside the container.
func tmpConfInContainer() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("/tmp/echo-i18n-%d-%s.conf", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

// pickModuleSingle opens the fuzzy picker over the locally available
// modules and returns the chosen one. ErrNoModulesAvailable when there
// are no candidates, ErrCancelled on Esc.
func pickModuleSingle(cfg *config.Config, root string, palette theme.Palette, title string) (string, error) {
	available := listAvailableModules(cfg, root)
	if len(available) == 0 {
		return "", ErrNoModulesAvailable
	}
	return runSingleFuzzyPickerStaged(title, available, palette, cfg.Stage)
}

// RunI18nExport extracts <mod>'s translations into a .po file. By
// default the file lands at <addons>/<mod>/i18n/<lang>.po on the host;
// --out <path> overrides the destination.
func RunI18nExport(ctx context.Context, opts I18nOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	parsed, err := parseI18nArgs(opts.Args, true, false)
	if err != nil {
		return err
	}
	if parsed.module == "" {
		picked, err := pickModuleSingle(opts.Cfg, opts.Root, opts.Palette, "Module to export")
		if err != nil {
			return err
		}
		parsed.module = picked
	}

	hostDest, err := resolveExportDest(opts.Cfg, opts.Root, parsed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(hostDest), 0o755); err != nil {
		return fmt.Errorf("create i18n dir: %w", err)
	}

	id, err := docker.ContainerID(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer)
	if err != nil {
		return err
	}

	containerTmp := tmpPathInContainer()
	defer cleanupContainerTmp(ctx, opts, containerTmp)

	conn := buildI18nConn(opts)
	confPath, confCleanup, err := writeContainerConf(ctx, opts, conn)
	if err != nil {
		return err
	}
	defer confCleanup()

	argv := odoo.ExportI18n(conn, opts.Cfg.OdooVersion, parsed.module, parsed.lang, containerTmp, confPath)
	if err := runOdooI18n(ctx, opts, argv); err != nil {
		return err
	}

	if err := docker.CopyFromContainer(ctx, id, containerTmp, hostDest); err != nil {
		return err
	}
	if opts.StreamOut != nil {
		opts.StreamOut("→ " + hostDest)
	}
	return nil
}

// RunI18nUpdate imports the module's <lang>.po back into the DB with
// --i18n-overwrite. Shows a prod-confirm in stage=prod unless --force.
func RunI18nUpdate(ctx context.Context, opts I18nOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	parsed, err := parseI18nArgs(opts.Args, false, true)
	if err != nil {
		return err
	}
	if parsed.module == "" {
		picked, err := pickModuleSingle(opts.Cfg, opts.Root, opts.Palette, "Module to update")
		if err != nil {
			return err
		}
		parsed.module = picked
	}

	addonsDir, err := resolveModuleDir(opts.Cfg, opts.Root, parsed.module)
	if err != nil {
		return err
	}
	hostSrc := defaultExportDest(addonsDir, parsed.module, parsed.lang)
	if _, err := os.Stat(hostSrc); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrTranslationMissing, hostSrc)
		}
		return err
	}

	if strings.EqualFold(opts.Cfg.Stage, "prod") && !parsed.force {
		if err := confirmI18nProd(opts.Palette, opts.Cfg.DBName, parsed.lang); err != nil {
			return err
		}
	}

	id, err := docker.ContainerID(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer)
	if err != nil {
		return err
	}
	containerTmp := tmpPathInContainer()
	defer cleanupContainerTmp(ctx, opts, containerTmp)

	if err := docker.CopyToContainer(ctx, id, hostSrc, containerTmp); err != nil {
		return err
	}

	conn := buildI18nConn(opts)
	confPath, confCleanup, err := writeContainerConf(ctx, opts, conn)
	if err != nil {
		return err
	}
	defer confCleanup()

	argv := odoo.UpdateI18n(conn, opts.Cfg.OdooVersion, parsed.module, parsed.lang, containerTmp, confPath)
	return runOdooI18n(ctx, opts, argv)
}

// resolveExportDest computes the host destination for i18n-export.
// With --out, it returns the literal path. Without, it walks addons
// paths to find the module and returns <addons>/<mod>/i18n/<lang>.po.
func resolveExportDest(cfg *config.Config, root string, parsed i18nArgs) (string, error) {
	if parsed.outOverride != "" {
		if filepath.IsAbs(parsed.outOverride) {
			return parsed.outOverride, nil
		}
		return filepath.Join(root, parsed.outOverride), nil
	}
	addonsDir, err := resolveModuleDir(cfg, root, parsed.module)
	if err != nil {
		return "", err
	}
	return defaultExportDest(addonsDir, parsed.module, parsed.lang), nil
}

// buildI18nConn mirrors buildConn from modules.go but accepts I18nOpts.
func buildI18nConn(opts I18nOpts) odoo.Conn {
	return buildConn(ModulesOpts{Cfg: opts.Cfg, Root: opts.Root})
}

// runOdooI18n streams an odoo argv through `compose exec -T` like
// runOdoo does for modules.
func runOdooI18n(ctx context.Context, opts I18nOpts, argv odoo.Cmd) error {
	return docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, argv, opts.StreamOut)
}

// writeContainerConf, on Odoo 19+, renders conn into a temp odoo.conf and
// writes it into the Odoo container, returning its in-container path and a
// best-effort cleanup. It exists because the `odoo i18n` subcommand takes
// no --db_* flags, so the connection must ride in a `-c` config file. On
// older series it is a no-op: it returns ("", noop, nil) and the legacy
// argv carries the connection as flags instead.
//
// The conf is streamed in via `sh -c 'cat > path'` so the in-container file
// is owned by the (non-root) Odoo user — readable by the `-c` flag and
// removable on cleanup. A plain `docker cp` would land it root-owned and
// 0600, which the Odoo process can neither read (the "config file … is not
// readable" error) nor remove from sticky /tmp. This mirrors how the remote
// i18n-pull path already writes its conf.
func writeContainerConf(ctx context.Context, opts I18nOpts, conn odoo.Conn) (string, func(), error) {
	noop := func() {}
	if odoo.Major(opts.Cfg.OdooVersion) < 19 {
		return "", noop, nil
	}
	host, err := os.CreateTemp("", "echo-i18n-*.conf")
	if err != nil {
		return "", noop, fmt.Errorf("create temp conf: %w", err)
	}
	hostPath := host.Name()
	_, werr := host.Write(odoo.RenderConf(conn, containerAddonsPath(ctx, opts)))
	cerr := host.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(hostPath)
		return "", noop, fmt.Errorf("write temp conf: %w", errors.Join(werr, cerr))
	}
	defer os.Remove(hostPath)

	containerPath := tmpConfInContainer()
	writeArgv := []string{"sh", "-c", "cat > " + containerPath}
	if err := docker.ExecWithStdin(ctx, opts.Cfg.ComposeCmd, opts.Root,
		opts.Cfg.OdooContainer, writeArgv, hostPath, nil); err != nil {
		return "", noop, fmt.Errorf("write conf in container: %w", err)
	}
	return containerPath, func() { cleanupContainerTmp(ctx, opts, containerPath) }, nil
}

// containerAddonsPath returns the raw addons_path value from the container's
// real odoo.conf, so the generated i18n conf loads the project's modules.
// Best-effort: "" when the conf can't be read or has no addons_path (the
// export still runs, just with Odoo's default addons path).
func containerAddonsPath(ctx context.Context, opts I18nOpts) string {
	confPath := opts.Cfg.ConfPath
	if confPath == "" {
		confPath = "/etc/odoo/odoo.conf"
	}
	conf, err := catContainer(ctx, opts.Cfg, opts.Root, confPath)
	if err != nil {
		return ""
	}
	return extractAddonsPath(conf)
}

// extractAddonsPath returns the raw, comma-joined addons_path value from
// odoo.conf text (the first `addons_path = …` line), unfiltered — unlike
// parseAddonsPath it keeps enterprise entries, since a module being exported
// may depend on them. Returns "" when absent.
func extractAddonsPath(conf string) string {
	for _, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if !strings.HasPrefix(line, "addons_path") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq >= 0 {
			return strings.TrimSpace(line[eq+1:])
		}
	}
	return ""
}

// cleanupContainerTmp removes a /tmp/echo-i18n-*.po inside the Odoo
// container. Best-effort: failures are surfaced as a stream warning,
// never returned.
func cleanupContainerTmp(ctx context.Context, opts I18nOpts, path string) {
	err := docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer,
		[]string{"rm", "-f", path}, nil)
	if err != nil && opts.StreamOut != nil {
		opts.StreamOut("(warn) failed to remove " + path + ": " + err.Error())
	}
}

// confirmI18nProd renders the red prod confirm specific to i18n-update.
func confirmI18nProd(palette theme.Palette, db, lang string) error {
	if err := requireTTY("pass --force to import into prod"); err != nil {
		return err
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Importing " + lang + " translations into prod database " + red).
			Description("Existing " + lang + " translations in the DB will be replaced (--i18n-overwrite).").
			Affirmative("Import").
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
