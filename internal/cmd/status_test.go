package cmd

import (
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestStatusProjectName(t *testing.T) {
	cfg := &config.Config{ProjectPath: "/home/u/diverza/dvz_ny_odoo_19"}

	// Remote: --from alias wins, else the remote path basename.
	if got := statusProjectName(cfg, true, "/srv/odoo/erp", "develop"); got != "develop" {
		t.Errorf("remote w/ from = %q, want develop", got)
	}
	if got := statusProjectName(cfg, true, "/srv/odoo/erp", ""); got != "erp" {
		t.Errorf("remote basename = %q, want erp", got)
	}
	if got := statusProjectName(cfg, true, "", ""); got != "-" {
		t.Errorf("remote empty = %q, want -", got)
	}

	// Local: compose override wins, else project path basename.
	if got := statusProjectName(cfg, false, "", ""); got != "dvz_ny_odoo_19" {
		t.Errorf("local basename = %q, want dvz_ny_odoo_19", got)
	}
	cfg.ComposeProject = "custom"
	if got := statusProjectName(cfg, false, "", ""); got != "custom" {
		t.Errorf("local override = %q, want custom", got)
	}
	if got := statusProjectName(&config.Config{}, false, "", ""); got != "-" {
		t.Errorf("local empty = %q, want -", got)
	}
}

func TestStatusFields(t *testing.T) {
	defer func(prev string) { EchoVersion = prev }(EchoVersion)

	EchoVersion = "0.10.0+abc1234.dirty"
	got := statusFields("19.0", "prod", "dvz_ny_odoo_19", "develop")
	want := [][2]string{
		{"cli", "0.10.0+abc1234.dirty"},
		{"odoo", "19.0"},
		{"env", "prod"},
		{"project", "dvz_ny_odoo_19"},
		{"db", "develop"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statusFields = %v, want %v", got, want)
	}

	// Empty values render loud, never blank.
	EchoVersion = ""
	got = statusFields("", "", "", "")
	want = [][2]string{
		{"cli", "unknown"},
		{"odoo", "unknown"},
		{"env", "unknown"},
		{"project", "-"},
		{"db", "-"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statusFields empty = %v, want %v", got, want)
	}
}
