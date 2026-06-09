// Package odoo builds the argv for invoking the Odoo CLI inside the
// project's Odoo container.
//
// Because `compose exec` bypasses the official Odoo image entrypoint,
// the wrapper that converts HOST/USER/PASSWORD env vars into
// `--db-host/--db-user/--db-password` flags never runs. We pass those
// flags explicitly via Conn so commands work regardless of the
// container's runtime env.
package odoo

import "strings"

// Cmd is the argv that goes after `<compose> exec -T <container>`.
type Cmd []string

// Conn holds the database connection details Odoo needs as CLI flags.
// Empty fields are skipped so Odoo can still fall back to its conf or
// env defaults when desired.
type Conn struct {
	DB       string
	Host     string
	Port     string
	User     string
	Password string
}

// Flags returns the connection arguments (`-d`, `--db_host`, …)
// omitting any field left empty so Odoo's own defaults apply. Exported
// so callers outside this package (e.g. the shell command) can build
// argvs with the same conventions.
func (c Conn) Flags() Cmd { return c.flags() }

func (c Conn) flags() Cmd {
	args := Cmd{}
	if c.DB != "" {
		args = append(args, "-d", c.DB)
	}
	if c.Host != "" {
		args = append(args, "--db_host", c.Host)
	}
	if c.Port != "" {
		args = append(args, "--db_port", c.Port)
	}
	if c.User != "" {
		args = append(args, "--db_user", c.User)
	}
	if c.Password != "" {
		args = append(args, "--db_password", c.Password)
	}
	return args
}

// LogLevels are the values Odoo's `--log-level` accepts. The set is
// identical across Odoo 17 / 18 / 19.
var LogLevels = []string{
	"debug_rpc_answer", "debug_rpc", "debug", "debug_sql",
	"info", "warn", "error", "critical", "test", "notset",
}

// WithLogLevel appends `--log-level=<level>` to an argv when level is
// non-empty; a no-op otherwise. Keeps the Odoo flag spelling in the odoo
// package while the cmd layer decides when to apply it (e.g. the module
// commands' `--level` flag).
func WithLogLevel(cmd Cmd, level string) Cmd {
	if level == "" {
		return cmd
	}
	return append(cmd, "--log-level="+level)
}

// Install builds the argv to install one or more modules.
func Install(c Conn, modules []string, withDemo bool) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	args = append(args, "-i", strings.Join(modules, ","), "--stop-after-init")
	if !withDemo {
		args = append(args, "--without-demo=all")
	}
	return args
}

// Update builds the argv to update one or more modules.
func Update(c Conn, modules []string) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	return append(args, "-u", strings.Join(modules, ","), "--stop-after-init")
}

// UpdateAll builds the argv to update every installed module.
func UpdateAll(c Conn) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	return append(args, "-u", "all", "--stop-after-init")
}

// Uninstall builds the argv to uninstall one or more modules.
func Uninstall(c Conn, modules []string) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	return append(args, "--uninstall", strings.Join(modules, ","), "--stop-after-init")
}

// Neutralize builds the argv for `odoo neutralize`, which applies the
// per-module data/neutralize.sql to the target DB (disabling crons,
// mail/fetchmail servers, payment providers, the environment ribbon, …).
// `neutralize` is a CLI subcommand, so it goes immediately after `odoo`,
// before the connection flags. The subcommand exits on its own — no
// --stop-after-init needed. Flag set is identical across Odoo 17/18/19.
func Neutralize(c Conn) Cmd {
	return append(Cmd{"odoo", "neutralize"}, c.flags()...)
}

// TestHTTPPort is the fallback HTTP port the test process binds to.
// Chosen high and uncommon so it is unlikely to clash with anything
// else running in the Odoo container.
const TestHTTPPort = "8189"

// TestOpts gathers the variations of how `test` is invoked.
//
//   - Modules: target module names (always at least one after picker).
//   - Tags:    optional --test-tags spec. If empty, Test auto-builds
//     `--test-tags /<mod1>,/<mod2>` to filter execution to
//     just those modules' tests.
//   - Update:  when true, also pass `-u <mods>` so the modules are
//     reloaded before tests run (needed when views/schema
//     changed; not needed for Python-only test iteration
//     because --stop-after-init spawns a fresh process that
//     imports the latest code from disk).
type TestOpts struct {
	Modules []string
	Tags    string
	Update  bool
}

// Test builds the argv to run unit tests for one or more modules.
//
// Default mode runs the tests against the already-installed modules
// without a reload (no `-u`), which is the fast path for iterating on
// Python test code. Passing `--update` (Opts.Update = true) adds
// `-u <mods>` so XML / model changes are picked up before the suite
// runs.
//
// `--test-tags` implies `--test-enable` per the Odoo CLI docs, and we
// always emit one or the other so the test framework is active. When
// the caller does not supply Tags, we auto-construct
// `--test-tags /<mod1>,/<mod2>` so only the target modules' tests run.
//
// `--log-level=test` is legacy in Odoo 19 (the dedicated TEST level
// was replaced by the `openerp.tests` logger at INFO), but the flag
// is still accepted in 17 / 18 / 19 without warning. Emitting it
// gives consistent, focused test output across versions.
//
// Defensive HTTP isolation: the test process runs in the same
// container as the live Odoo server already bound to 8069. We pass
// both `--no-http` (skip the HTTP bind entirely) AND
// `--http-port=8189` (redirect the bind elsewhere if the build /
// distribution silently ignores --no-http — observed on Odoo 19
// Enterprise where --no-http alone did not prevent the 8069 bind).
// Either one alone is enough on a compliant Odoo; together they
// survive both quirks. HttpCase suites spin up their own ephemeral
// server independently of these flags.
//
// Flag set is identical across Odoo 17, 18 and 19.
func Test(c Conn, opts TestOpts) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	args = append(args, "--no-http", "--http-port="+TestHTTPPort)

	tags := opts.Tags
	if tags == "" {
		parts := make([]string, len(opts.Modules))
		for i, m := range opts.Modules {
			parts[i] = "/" + m
		}
		tags = strings.Join(parts, ",")
	}
	args = append(args, "--test-tags", tags)

	if opts.Update {
		args = append(args, "-u", strings.Join(opts.Modules, ","))
	}
	return append(args, "--stop-after-init", "--log-level=test")
}

// ExportI18n builds the argv to extract a module's translations to a
// .po file at outPath inside the container.
func ExportI18n(c Conn, module, lang, outPath string) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	return append(args,
		"--modules="+module,
		"-l", lang,
		"--i18n-export="+outPath,
		"--stop-after-init",
	)
}

// UpdateI18n builds the argv to import a .po file at inPath into the DB
// with --i18n-overwrite.
func UpdateI18n(c Conn, module, lang, inPath string) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	return append(args,
		"--modules="+module,
		"-l", lang,
		"--i18n-import="+inPath,
		"--i18n-overwrite",
		"--stop-after-init",
	)
}
