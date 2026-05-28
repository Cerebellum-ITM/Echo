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

// TestHTTPPort is the fallback HTTP port the test process binds to.
// Chosen high and uncommon so it is unlikely to clash with anything
// else running in the Odoo container.
const TestHTTPPort = "8189"

// Test builds the argv to run unit tests for one or more modules.
// Uses `-u` (update) so an already-installed module picks up code
// changes before running its suite, then toggles --test-enable. When
// tags is non-empty we pass --test-tags <spec> instead, which already
// implies --test-enable per Odoo's CLI behavior — emitting both would
// be redundant.
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
func Test(c Conn, modules []string, tags string) Cmd {
	args := append(Cmd{"odoo"}, c.flags()...)
	args = append(args, "--no-http", "--http-port="+TestHTTPPort)
	if tags != "" {
		args = append(args, "--test-tags", tags)
	} else {
		args = append(args, "--test-enable")
	}
	return append(args, "-u", strings.Join(modules, ","), "--stop-after-init")
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
