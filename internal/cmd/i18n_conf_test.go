package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

// On Odoo < 19 the i18n flow carries the DB connection as `--db_*` flags, so
// writeContainerConf must be a no-op: no conf path, no container call, no
// error. (The 19+ path needs a live container and is verified manually.)
func TestWriteContainerConfLegacyNoop(t *testing.T) {
	opts := I18nOpts{Cfg: &config.Config{OdooVersion: "18.0"}}
	path, cleanup, err := writeContainerConf(context.Background(), opts, odoo.Conn{DB: "dev"})
	if err != nil {
		t.Fatalf("legacy writeContainerConf err = %v, want nil", err)
	}
	if path != "" {
		t.Fatalf("legacy writeContainerConf path = %q, want empty", path)
	}
	if cleanup == nil {
		t.Fatal("cleanup func must not be nil")
	}
	cleanup() // must be safe to call
}

func TestExtractAddonsPath(t *testing.T) {
	conf := `[options]
; a comment
db_host = db
# addons_path = /should/be/ignored/commented
admin_passwd = x
addons_path = /usr/lib/python3/dist-packages/odoo/addons,/mnt/extra-addons,/mnt/enterprise-addons
data_dir = /var/lib/odoo
`
	got := extractAddonsPath(conf)
	want := "/usr/lib/python3/dist-packages/odoo/addons,/mnt/extra-addons,/mnt/enterprise-addons"
	if got != want {
		t.Fatalf("extractAddonsPath = %q, want %q", got, want)
	}
	// Enterprise entries are kept (unlike parseAddonsPath), since a module
	// being exported may depend on them.
	if !strings.Contains(got, "enterprise-addons") {
		t.Fatalf("expected enterprise path retained, got %q", got)
	}
	if extractAddonsPath("[options]\ndb_host = db\n") != "" {
		t.Fatal("expected empty when no addons_path line")
	}
}
