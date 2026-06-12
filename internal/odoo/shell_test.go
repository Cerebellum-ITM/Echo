package odoo

import (
	"strings"
	"testing"
)

func TestShellBuildsArgv(t *testing.T) {
	conn := Conn{DB: "mydb", Host: "db", Port: "5432", User: "odoo", Password: "odoo"}
	got := strings.Join(Shell(conn), " ")
	want := "odoo shell -d mydb --db_host db --db_port 5432 --db_user odoo --db_password odoo --no-http"
	if got != want {
		t.Fatalf("Shell argv\n got: %q\nwant: %q", got, want)
	}
}

func TestShellOmitsEmptyConnFields(t *testing.T) {
	got := strings.Join(Shell(Conn{DB: "mydb"}), " ")
	want := "odoo shell -d mydb --no-http"
	if got != want {
		t.Fatalf("Shell argv\n got: %q\nwant: %q", got, want)
	}
}
