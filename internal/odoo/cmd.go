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
