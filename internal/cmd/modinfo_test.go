package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestManifestVersion(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"double quotes", `{"name": "Sale", "version": "17.0.1.3.0"}`, "17.0.1.3.0"},
		{"single quotes", `{'name': 'Sale', 'version': '1.2.3'}`, "1.2.3"},
		{"spaced colon", `{ "version" :  "2.0" }`, "2.0"},
		{"absent", `{"name": "Sale"}`, ""},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := manifestVersion(c.text); got != c.want {
				t.Fatalf("manifestVersion(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

func TestOdooSerie(t *testing.T) {
	cases := map[string]string{"17": "17.0", "18": "18.0", "17.0": "17.0", "": ""}
	for in, want := range cases {
		if got := odooSerie(in); got != want {
			t.Fatalf("odooSerie(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdaptVersion(t *testing.T) {
	cases := []struct {
		version, serie, want string
	}{
		{"1.3.0", "17.0", "17.0.1.3.0"},
		{"17.0.1.3.0", "17.0", "17.0.1.3.0"},
		{"17.0", "17.0", "17.0.17.0"}, // Odoo's literal adapt_version behavior
		{"2.0", "", "2.0"},            // empty serie → unchanged
		{"", "17.0", ""},              // empty version → unchanged
	}
	for _, c := range cases {
		if got := adaptVersion(c.version, c.serie); got != c.want {
			t.Fatalf("adaptVersion(%q,%q) = %q, want %q", c.version, c.serie, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"17.0.1.0", "17.0.1.0", 0},
		{"17.0.1", "17.0.1.0", 0}, // missing trailing segment counts as 0
		{"17.0.2.0", "17.0.1.0", 1},
		{"17.0.1.0", "17.0.2.0", -1},
		{"17.0.10.0", "17.0.9.0", 1}, // numeric, not lexicographic
		{"1.0.0", "1.0.0a", -1},      // non-numeric segment string fallback
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Fatalf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestComputeStatus(t *testing.T) {
	cases := []struct {
		name string
		res  ModinfoResult
		want string
	}{
		{"no row", ModinfoResult{DBFound: false}, "not installed"},
		{"uninstalled state", ModinfoResult{DBFound: true, DBState: "uninstalled"}, "not installed"},
		{"missing versions", ModinfoResult{DBFound: true, DBState: "installed"}, "no version"},
		{"in sync", ModinfoResult{DBFound: true, DBState: "installed", DBVersion: "17.0.1.3.0", Manifest: "1.3.0", Adapted: "17.0.1.3.0"}, "in sync"},
		{"update pending", ModinfoResult{DBFound: true, DBState: "installed", DBVersion: "17.0.1.2.0", Manifest: "1.3.0", Adapted: "17.0.1.3.0"}, "update pending"},
		{"db ahead", ModinfoResult{DBFound: true, DBState: "installed", DBVersion: "17.0.1.4.0", Manifest: "1.3.0", Adapted: "17.0.1.3.0"}, "db ahead"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.res
			r.computeStatus()
			if r.Status != c.want {
				t.Fatalf("computeStatus() = %q, want %q", r.Status, c.want)
			}
		})
	}
}

func TestRunModinfoRemote(t *testing.T) {
	remote := newFakeRemote(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "addons/sale/__manifest__.py"), "{'name': 'sale', 'version': '18.0.1.0'}")
	mustWrite(t, filepath.Join(root, "addons/stock/__manifest__.py"), "{'name': 'stock', 'version': '1.1'}")
	mustWrite(t, filepath.Join(remote.dir, ".echo/lock.json"),
		`{"schema": 1, "modules": {"sale": {"source": "worktree", "version": "18.0.1.0", "via": "push", "at": "2026-10-20T10:00:00Z", "verified": true}}}`)

	rep, err := RunModinfo(context.Background(), ModinfoOpts{
		Cfg: remote.cfg(), Root: root, Args: []string{"sale", "stock", "ghost", "--from", "stg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Remote || rep.DB != "stg" {
		t.Errorf("report remote=%v db=%q, want a remote run on stg", rep.Remote, rep.DB)
	}
	type verdict struct{ module, status, deployed string }
	var got []verdict
	for _, m := range rep.Modules {
		got = append(got, verdict{m.Module, m.Status, m.Deployed})
	}
	want := []verdict{
		{"sale", "in sync", "18.0.1.0"},
		{"stock", "update pending", ""},
		{"ghost", "not installed", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("verdicts = %v, want %v", got, want)
	}
}

func TestParseModinfoArgs(t *testing.T) {
	if _, err := parseModinfoArgs([]string{"--all", "sale"}); !errors.Is(err, ErrUsage) {
		t.Errorf("--all with a name: err = %v, want ErrUsage", err)
	}
	p, err := parseModinfoArgs([]string{"sale", "--from", "stg", "stock", "--json", "--copy"})
	if err != nil {
		t.Fatal(err)
	}
	want := modinfoArgs{modules: []string{"sale", "stock"}, from: "stg", json: true, copy: true}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("parsed %+v, want %+v", p, want)
	}
}
