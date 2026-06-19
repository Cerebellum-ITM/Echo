package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseDeployArgs(t *testing.T) {
	cases := []struct {
		in      []string
		want    deployArgs
		wantErr bool
	}{
		{nil, deployArgs{limit: 20}, false},
		{[]string{"--from", "prod"}, deployArgs{from: "prod", limit: 20}, false},
		{[]string{"--from=prod", "--dry-run"}, deployArgs{from: "prod", limit: 20, dryRun: true}, false},
		{[]string{"--limit", "50", "--force"}, deployArgs{limit: 50, force: true}, false},
		{[]string{"--limit=5"}, deployArgs{limit: 5}, false},
		{[]string{"--limit", "0"}, deployArgs{}, true},
		{[]string{"--limit", "x"}, deployArgs{}, true},
		{[]string{"--from"}, deployArgs{}, true},
		{[]string{"--bogus"}, deployArgs{}, true},
		{[]string{"some_module"}, deployArgs{}, true},
		{[]string{"--i18n"}, deployArgs{limit: 20, i18n: true}, false},
		{[]string{"--no-i18n"}, deployArgs{limit: 20, noI18n: true}, false},
		{[]string{"--i18n", "--no-i18n"}, deployArgs{}, true}, // mutually exclusive
	}
	for _, tc := range cases {
		got, err := parseDeployArgs(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDeployArgs(%v): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDeployArgs(%v): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDeployArgs(%v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// addonsRepo builds a temp repo layout with the given addon modules (each
// gets a __manifest__.py) plus a non-addon `docs/` folder.
func addonsRepo(t *testing.T, modules ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range modules {
		dir := filepath.Join(root, m)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "__manifest__.py"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestModuleFromSubject(t *testing.T) {
	root := addonsRepo(t, "sale_extra")

	cases := []struct {
		subject string
		want    string
	}{
		{"[FIX] sale_extra: correct tax rounding", "sale_extra"},
		{"[IMP]sale_extra:no spaces", "sale_extra"},
		{"[ADD] docs: not an addon", ""},       // names a non-addon dir
		{"[FIX] missing_mod: not on disk", ""}, // module doesn't exist
		{"plain subject without scheme", ""},   // no scheme at all
		{"[REL] sale_extra bump", ""},          // missing the colon
	}
	for _, tc := range cases {
		if got := moduleFromSubject(root, tc.subject); got != tc.want {
			t.Errorf("moduleFromSubject(%q) = %q, want %q", tc.subject, got, tc.want)
		}
	}
}

func TestModulesFromPaths(t *testing.T) {
	root := addonsRepo(t, "sale_extra", "stock_extra")

	cases := []struct {
		paths []string
		want  []string
	}{
		{[]string{"sale_extra/models/sale.py", "sale_extra/views/sale.xml"}, []string{"sale_extra"}},
		{[]string{"sale_extra/models/sale.py", "stock_extra/models/stock.py"}, []string{"sale_extra", "stock_extra"}},
		{[]string{"docs/readme.md", "README.md"}, nil},
		{nil, nil},
	}
	for _, tc := range cases {
		if got := modulesFromPaths(root, tc.paths); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("modulesFromPaths(%v) = %v, want %v", tc.paths, got, tc.want)
		}
	}
}

func TestSplitInstallUpdate(t *testing.T) {
	states := map[string]string{
		"sale_extra":  "installed",
		"stock_extra": "to upgrade",
		"old_mod":     "uninstalled",
	}
	install, update := splitInstallUpdate(
		[]string{"brand_new", "old_mod", "sale_extra", "stock_extra"}, states)
	if !reflect.DeepEqual(update, []string{"sale_extra", "stock_extra"}) {
		t.Errorf("update = %v", update)
	}
	if !reflect.DeepEqual(install, []string{"brand_new", "old_mod"}) {
		t.Errorf("install = %v", install)
	}
}

func TestPathsTouchI18n(t *testing.T) {
	cases := []struct {
		module string
		paths  []string
		want   bool
	}{
		{"sale_extra", []string{"sale_extra/i18n/es.po"}, true},
		{"sale_extra", []string{"sale_extra/i18n/sale_extra.pot"}, true},
		{"sale_extra", []string{"sale_extra/models/sale.py"}, false},
		{"sale_extra", []string{"sale_extra/i18n_helpers/x.py"}, false}, // not the i18n/ dir
		{"sale_extra", []string{"stock_extra/i18n/es.po"}, false},       // another module's i18n
		{"sale_extra", nil, false},
	}
	for _, tc := range cases {
		if got := pathsTouchI18n(tc.module, tc.paths); got != tc.want {
			t.Errorf("pathsTouchI18n(%q, %v) = %v, want %v", tc.module, tc.paths, got, tc.want)
		}
	}
}

func TestI18nOverwriteDecision(t *testing.T) {
	cases := []struct {
		name                      string
		force, no, detectedUpdate bool
		wantState                 string
		wantOverwrite             bool
	}{
		{"auto on", false, false, true, "on", true},
		{"auto off", false, false, false, "off", false},
		{"forced no detection", true, false, false, "forced", true},
		{"forced with detection", true, false, true, "forced", true},
		{"suppressed detection", false, true, true, "suppressed", false},
		{"no-i18n without detection", false, true, false, "off", false},
	}
	for _, tc := range cases {
		state, ov := i18nOverwriteDecision(tc.force, tc.no, tc.detectedUpdate)
		if state != tc.wantState || ov != tc.wantOverwrite {
			t.Errorf("%s: i18nOverwriteDecision(%v,%v,%v) = (%q,%v), want (%q,%v)",
				tc.name, tc.force, tc.no, tc.detectedUpdate, state, ov, tc.wantState, tc.wantOverwrite)
		}
	}
}

func TestIsAddonDirRejectsPathTricks(t *testing.T) {
	root := addonsRepo(t, "sale_extra")
	if isAddonDir(root, "sale_extra/../sale_extra") {
		t.Fatal("path separators in a module name must be rejected")
	}
	if isAddonDir(root, "") {
		t.Fatal("empty name must be rejected")
	}
}
