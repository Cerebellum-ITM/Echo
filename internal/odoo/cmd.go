// Package odoo builds the argv for invoking the Odoo CLI inside the
// project's Odoo container.
package odoo

import "strings"

// Cmd is the argv that goes after `<compose> exec -T <container>`.
type Cmd []string

// Install builds the argv to install one or more modules.
func Install(db string, modules []string, withDemo bool) Cmd {
	args := Cmd{"odoo", "-d", db, "-i", strings.Join(modules, ","), "--stop-after-init"}
	if !withDemo {
		args = append(args, "--without-demo=all")
	}
	return args
}

// Update builds the argv to update one or more modules.
func Update(db string, modules []string) Cmd {
	return Cmd{"odoo", "-d", db, "-u", strings.Join(modules, ","), "--stop-after-init"}
}

// UpdateAll builds the argv to update every installed module.
func UpdateAll(db string) Cmd {
	return Cmd{"odoo", "-d", db, "-u", "all", "--stop-after-init"}
}

// Uninstall builds the argv to uninstall one or more modules.
func Uninstall(db string, modules []string) Cmd {
	return Cmd{"odoo", "-d", db, "--uninstall", strings.Join(modules, ","), "--stop-after-init"}
}
