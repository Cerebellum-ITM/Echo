package odoo

import (
	"reflect"
	"testing"
)

func TestWithLogLevel(t *testing.T) {
	base := Cmd{"odoo", "-u", "sale", "--stop-after-init"}

	if got := WithLogLevel(base, ""); !reflect.DeepEqual(got, base) {
		t.Errorf("empty level should be a no-op, got %v", got)
	}

	got := WithLogLevel(base, "debug")
	want := Cmd{"odoo", "-u", "sale", "--stop-after-init", "--log-level=debug"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithLogLevel(base, debug) = %v, want %v", got, want)
	}
}

func TestWithI18nOverwrite(t *testing.T) {
	base := Cmd{"odoo", "-u", "sale", "--stop-after-init"}

	if got := WithI18nOverwrite(base, false); !reflect.DeepEqual(got, base) {
		t.Errorf("off should be a no-op, got %v", got)
	}

	got := WithI18nOverwrite(base, true)
	want := Cmd{"odoo", "-u", "sale", "--stop-after-init", "--i18n-overwrite"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithI18nOverwrite(base, true) = %v, want %v", got, want)
	}
}

func TestWithTests(t *testing.T) {
	base := Cmd{"odoo", "-u", "sale", "--stop-after-init"}

	if got := WithTests(base, nil); !reflect.DeepEqual(got, base) {
		t.Errorf("empty modules should be a no-op, got %v", got)
	}

	got := WithTests(base, []string{"sale", "stock"})
	want := Cmd{"odoo", "-u", "sale", "--stop-after-init",
		"--test-enable", "--test-tags", "/sale,/stock",
		"--no-http", "--http-port=" + TestHTTPPort, "--log-level=test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithTests = %v, want %v", got, want)
	}
}

func TestMajor(t *testing.T) {
	cases := map[string]int{
		"19": 19, "19.0": 19, "18.0": 18, "17": 17,
		"": 0, "abc": 0, " 19 ": 19, "saas~17.2": 0,
	}
	for in, want := range cases {
		if got := Major(in); got != want {
			t.Errorf("Major(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRenderConf(t *testing.T) {
	got := string(RenderConf(Conn{DB: "develop", Host: "db", Port: "5432", User: "odoo", Password: "secret"}, "/mnt/extra-addons,/mnt/enterprise"))
	want := "[options]\ndb_host = db\ndb_port = 5432\ndb_user = odoo\ndb_password = secret\naddons_path = /mnt/extra-addons,/mnt/enterprise\n"
	if got != want {
		t.Errorf("RenderConf full = %q, want %q", got, want)
	}
	// db_name is never emitted (callers pass it via -d); empty fields skip,
	// and an empty addonsPath omits the addons_path line.
	got = string(RenderConf(Conn{DB: "develop", Host: "db"}, ""))
	if want := "[options]\ndb_host = db\n"; got != want {
		t.Errorf("RenderConf sparse = %q, want %q", got, want)
	}
}

func TestExportI18nLegacy(t *testing.T) {
	c := Conn{DB: "dev", Host: "db", User: "odoo"}
	got := ExportI18n(c, "18.0", "sale", "es_MX", "/tmp/x.po", "/tmp/ignored.conf")
	want := Cmd{"odoo", "-d", "dev", "--db_host", "db", "--db_user", "odoo",
		"--modules=sale", "-l", "es_MX", "--i18n-export=/tmp/x.po", "--stop-after-init"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legacy export = %v, want %v", got, want)
	}
}

func TestExportI18nV19(t *testing.T) {
	c := Conn{DB: "dev", Host: "db", User: "odoo", Password: "secret"}
	got := ExportI18n(c, "19.0", "sale", "es_MX", "/tmp/x.po", "/tmp/echo.conf")
	want := Cmd{"odoo", "i18n", "export", "-c", "/tmp/echo.conf", "-d", "dev",
		"-l", "es_MX", "-o", "/tmp/x.po", "sale"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("v19 export = %v, want %v", got, want)
	}
	// No --db_* / --no-http / --stop-after-init leak onto the v19 argv.
	for _, a := range got {
		switch a {
		case "--db_host", "--db_user", "--db_password", "--db_port", "--no-http", "--stop-after-init":
			t.Errorf("v19 export must not contain %q", a)
		}
	}
}

func TestUpdateI18nV19(t *testing.T) {
	c := Conn{DB: "dev", Host: "db"}
	got := UpdateI18n(c, "19", "sale", "es_MX", "/tmp/x.po", "/tmp/echo.conf")
	want := Cmd{"odoo", "i18n", "import", "-c", "/tmp/echo.conf", "-d", "dev",
		"-l", "es_MX", "-w", "/tmp/x.po"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("v19 import = %v, want %v", got, want)
	}
}

func TestUpdateI18nLegacy(t *testing.T) {
	c := Conn{DB: "dev"}
	got := UpdateI18n(c, "17.0", "sale", "es_MX", "/tmp/x.po", "")
	want := Cmd{"odoo", "-d", "dev", "--modules=sale", "-l", "es_MX",
		"--i18n-import=/tmp/x.po", "--i18n-overwrite", "--stop-after-init"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legacy import = %v, want %v", got, want)
	}
}
