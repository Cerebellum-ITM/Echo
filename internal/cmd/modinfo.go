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
	Status    string // in sync | update pending | db ahead | not installed | no version
	Copy      bool
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

// RunModinfo compares a module's DB-installed version against its manifest
// version and returns the verdict. With no module name a single-select
// picker chooses one; a non-TTY caller without a module fails closed.
func RunModinfo(ctx context.Context, opts ModinfoOpts) (ModinfoResult, error) {
	if opts.Cfg.OdooContainer == "" {
		return ModinfoResult{}, ErrNoOdooContainer
	}
	if opts.Cfg.DBName == "" || opts.Cfg.DBContainer == "" {
		return ModinfoResult{}, ErrNoDB
	}

	var module string
	copyFlag := false
	for _, a := range opts.Args {
		switch {
		case a == "--copy":
			copyFlag = true
		case strings.HasPrefix(a, "-"):
			return ModinfoResult{}, fmt.Errorf("unknown flag: %s", a)
		default:
			if module == "" {
				module = a
			}
		}
	}

	if module == "" {
		names, err := resolveModules(ctx, ModulesOpts{Cfg: opts.Cfg, Root: opts.Root, Palette: opts.Palette})
		if err != nil || len(names) == 0 {
			return ModinfoResult{}, ErrNoModulesAvailable
		}
		picked, err := runSingleFuzzyPicker("Module to inspect", names, opts.Palette)
		if err != nil {
			return ModinfoResult{}, err
		}
		module = picked
	}

	res := ModinfoResult{Module: module, Copy: copyFlag}

	user := env.Load(opts.Root)["POSTGRES_USER"]
	_, ver, state, found, err := docker.ModuleVersion(ctx,
		opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, user, opts.Cfg.DBName, module)
	if err != nil {
		return ModinfoResult{}, fmt.Errorf("query ir_module_module: %w", err)
	}
	if found {
		res.DBFound = true
		res.DBVersion = ver
		res.DBState = state
	}

	manifest, err := moduleManifest(ctx, opts, module)
	if err != nil {
		return ModinfoResult{}, fmt.Errorf("read manifest: %w", err)
	}
	res.Manifest = manifestVersion(manifest)
	res.Adapted = adaptVersion(res.Manifest, odooSerie(opts.Cfg.OdooVersion))
	res.computeStatus()
	return res, nil
}
