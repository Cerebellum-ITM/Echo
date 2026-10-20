package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/theme"
)

// ModinfoOpts configures a `modinfo` inspection.
type ModinfoOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	Log     func(level, sub, msg, db string, fields ...[2]string)
}

// ModinfoResult is the outcome of comparing a module's DB-registered
// version against its manifest version.
type ModinfoResult struct {
	Module    string
	DBVersion string // raw latest_version, "" if none/NULL
	DBState   string // ir_module_module.state, "" if no row
	DBFound   bool
	Manifest  string // raw manifest version, "" if absent
	Adapted   string // manifest version normalized via Odoo's adapt_version
	Deployed  string // version the target's deploy lock records; remote only
	Status    string // in sync | update pending | db ahead | not installed | no version
}

// ModinfoReport is the outcome of one `modinfo` run over one or more modules.
type ModinfoReport struct {
	Modules []ModinfoResult
	Remote  bool
	DB      string // the database that answered, local or remote
	JSON    bool
	Copy    bool
}

type modinfoArgs struct {
	modules         []string
	from            string
	remote          bool
	all, json, copy bool
}

func parseModinfoArgs(args []string) (modinfoArgs, error) {
	var p modinfoArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--copy":
			p.copy = true
		case a == "--json":
			p.json = true
		case a == "--all":
			p.all = true
		case a == "--remote":
			p.remote = true
		case a == "--from":
			if i+1 >= len(args) {
				return p, fmt.Errorf("%w: --from needs a target", ErrUsage)
			}
			i++
			p.from = args[i]
		case strings.HasPrefix(a, "--from="):
			p.from = strings.TrimPrefix(a, "--from=")
		case strings.HasPrefix(a, "-"):
			return p, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			p.modules = append(p.modules, a)
		}
	}
	if p.all && len(p.modules) > 0 {
		return p, fmt.Errorf("%w: --all takes no module names", ErrUsage)
	}
	return p, nil
}

// manifestVersionRe extracts the `version` value from a __manifest__.py
// Python dict, single- or double-quoted.
var manifestVersionRe = regexp.MustCompile(`['"]version['"]\s*:\s*['"]([^'"]+)['"]`)

// manifestVersion returns the version declared in manifest text, or "".
func manifestVersion(text string) string {
	m := manifestVersionRe.FindStringSubmatch(text)
	if len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// odooSerie derives the major series ("17.0") from a configured Odoo
// version ("17"). A version already carrying a dot is used as-is; an
// empty version yields "".
func odooSerie(v string) string {
	if v == "" {
		return ""
	}
	if strings.Contains(v, ".") {
		return v
	}
	return v + ".0"
}

// adaptVersion reproduces Odoo's modules.adapt_version: it prepends the
// major series to a manifest version that doesn't already start with it,
// matching how ir_module_module.latest_version is stored. With an empty
// serie or version it is a no-op.
func adaptVersion(version, serie string) string {
	if serie == "" || version == "" {
		return version
	}
	if version == serie || !strings.HasPrefix(version, serie+".") {
		return serie + "." + version
	}
	return version
}

// compareVersions compares two dotted version strings segment-wise,
// numerically where both segments parse as ints (missing/empty segments
// count as 0), falling back to a string compare for non-numeric segments.
// Returns -1, 0, or 1.
func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x == "" {
			x = "0"
		}
		if y == "" {
			y = "0"
		}
		xi, xerr := strconv.Atoi(x)
		yi, yerr := strconv.Atoi(y)
		if xerr == nil && yerr == nil {
			if xi != yi {
				if xi < yi {
					return -1
				}
				return 1
			}
			continue
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// computeStatus fills r.Status from the DB row + version comparison.
func (r *ModinfoResult) computeStatus() {
	if !r.DBFound || r.DBState != "installed" {
		r.Status = "not installed"
		return
	}
	if r.DBVersion == "" || r.Manifest == "" {
		r.Status = "no version"
		return
	}
	switch compareVersions(r.Adapted, r.DBVersion) {
	case 0:
		r.Status = "in sync"
	case 1:
		r.Status = "update pending"
	default:
		r.Status = "db ahead"
	}
}

// moduleManifest returns the __manifest__.py text for a module, reading
// from the host addons paths in host mode, or from inside the Odoo
// container in conf mode (matching resolveModules' source of truth). A
// module whose manifest can't be located yields ("", nil).
func moduleManifest(ctx context.Context, opts ModinfoOpts, module string) (string, error) {
	if opts.Cfg.AddonsMode == addonsModeConf {
		for _, base := range opts.Cfg.AddonsPaths {
			p := strings.TrimRight(base, "/") + "/" + module + "/__manifest__.py"
			if txt, err := catContainer(ctx, opts.Cfg, opts.Root, p); err == nil {
				return txt, nil
			}
		}
		return "", nil
	}
	paths := opts.Cfg.AddonsPaths
	if len(paths) == 0 {
		paths = []string{".", "addons", "custom"}
	}
	for _, base := range paths {
		p := filepath.Join(opts.Root, base, module, "__manifest__.py")
		if data, err := os.ReadFile(p); err == nil {
			return string(data), nil
		}
	}
	return "", nil
}

// RunModinfo compares each module's version in the local database (or, with
// --from/--remote, the target's) against the local manifest version and
// returns one verdict per module. Without names, --all inspects every module
// of the project's addons and no flag opens a single-select picker, which
// fails closed without a TTY. A remote run also reports the version the
// target's deploy lock records.
func RunModinfo(ctx context.Context, opts ModinfoOpts) (ModinfoReport, error) {
	p, err := parseModinfoArgs(opts.Args)
	if err != nil {
		return ModinfoReport{}, err
	}
	rep := ModinfoReport{DB: opts.Cfg.DBName, JSON: p.json, Copy: p.copy}
	if p.from != "" || p.remote {
		return runModinfoRemote(ctx, opts, p, rep)
	}

	if opts.Cfg.OdooContainer == "" {
		return rep, ErrNoOdooContainer
	}
	if opts.Cfg.DBName == "" || opts.Cfg.DBContainer == "" {
		return rep, ErrNoDB
	}
	modules, err := modinfoModules(ctx, opts, p)
	if err != nil {
		return rep, err
	}
	user := env.Load(opts.Root)["POSTGRES_USER"]
	rows, err := docker.ModuleStates(ctx,
		opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, user, opts.Cfg.DBName, false)
	if err != nil {
		return rep, fmt.Errorf("query ir_module_module: %w", err)
	}
	rep.Modules, err = inspectModules(ctx, opts, modules, rows, odooSerie(opts.Cfg.OdooVersion))
	return rep, err
}

func runModinfoRemote(ctx context.Context, opts ModinfoOpts, p modinfoArgs, rep ModinfoReport) (ModinfoReport, error) {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
	if err != nil {
		return rep, err
	}
	rep.Remote, rep.DB = true, rsc.target.dbName
	modules, err := modinfoModules(ctx, opts, p)
	if err != nil {
		return rep, err
	}
	states, versions, err := remoteModuleStates(ctx, rsc.sshHost, rsc.remotePath, rsc.target, rsc.conn.User, rsc.target.dbName)
	if err != nil {
		return rep, fmt.Errorf("query ir_module_module: %w", err)
	}
	rep.Modules, err = inspectModules(ctx, opts, modules,
		remoteModuleRows(states, versions, false), odooSerie(rsc.target.odooVersion))
	if err != nil {
		return rep, err
	}
	lock, _ := readDeployLock(ctx, rsc, opts.Log)
	for i := range rep.Modules {
		rep.Modules[i].Deployed = lock.Modules[rep.Modules[i].Module].Version
	}
	return rep, nil
}

// modinfoModules is the list a run inspects: the names given, every module of
// the local project's addons with --all, or the one picked.
func modinfoModules(ctx context.Context, opts ModinfoOpts, p modinfoArgs) ([]string, error) {
	if len(p.modules) > 0 {
		return p.modules, nil
	}
	names, err := resolveModules(ctx, ModulesOpts{Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette})
	if err != nil || len(names) == 0 {
		return nil, ErrNoModulesAvailable
	}
	if p.all {
		return names, nil
	}
	picked, err := runSingleFuzzyPicker("Module to inspect", names, opts.Palette)
	if err != nil {
		return nil, err
	}
	return []string{picked}, nil
}

// inspectModules builds each module's verdict from the database rows and the
// local manifest, normalized to the database's series. A module without a
// manifest is not an error: its status comes from the database alone.
func inspectModules(ctx context.Context, opts ModinfoOpts, modules []string, rows []docker.ModuleStateRow, serie string) ([]ModinfoResult, error) {
	byName := make(map[string]docker.ModuleStateRow, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	out := make([]ModinfoResult, 0, len(modules))
	for _, m := range modules {
		res := ModinfoResult{Module: m}
		if row, ok := byName[m]; ok {
			res.DBFound, res.DBVersion, res.DBState = true, row.Version, row.State
		}
		manifest, err := moduleManifest(ctx, opts, m)
		if err != nil {
			return nil, fmt.Errorf("read manifest: %w", err)
		}
		res.Manifest = manifestVersion(manifest)
		res.Adapted = adaptVersion(res.Manifest, serie)
		res.computeStatus()
		out = append(out, res)
	}
	return out, nil
}
