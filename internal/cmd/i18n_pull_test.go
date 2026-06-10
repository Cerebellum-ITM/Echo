package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

func TestParseI18nPullArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantMod  string
		wantLang string
		wantFrom string
		wantAll  bool
		wantErr  bool
	}{
		{"defaults", nil, "", "es_MX", "", false, false},
		{"module only", []string{"sale"}, "sale", "es_MX", "", false, false},
		{"module + lang", []string{"sale", "fr_FR"}, "sale", "fr_FR", "", false, false},
		{"from flag", []string{"sale", "--from", "prod"}, "sale", "es_MX", "prod", false, false},
		{"from equals", []string{"sale", "--from=prod"}, "sale", "es_MX", "prod", false, false},
		{"all", []string{"--all"}, "", "es_MX", "", true, false},
		{"all + lang", []string{"--all", "fr_FR"}, "", "fr_FR", "", true, false},
		{"all + extra positional", []string{"--all", "sale", "fr_FR"}, "", "", "", true, true},
		{"too many positionals", []string{"a", "b", "c"}, "", "", "", false, true},
		{"unknown flag", []string{"--bogus"}, "", "", "", false, true},
		{"from without value", []string{"--from"}, "", "", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseI18nPullArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.module != tc.wantMod || got.lang != tc.wantLang ||
				got.from != tc.wantFrom || got.all != tc.wantAll {
				t.Errorf("got %+v, want mod=%q lang=%q from=%q all=%v",
					got, tc.wantMod, tc.wantLang, tc.wantFrom, tc.wantAll)
			}
		})
	}
}

func TestResolvePullRemote(t *testing.T) {
	cfg := &config.Config{
		ConnectSSHHost:    "erp",
		ConnectRemotePath: "/opt/odoo",
		ConnectTargets: []config.ConnectTarget{
			{Name: "prod", SSHHost: "prod-host", RemotePath: "/srv/prod"},
			{Name: "broken", SSHHost: "h"}, // no remote_path
		},
	}

	// Default: project's own [connect].
	host, path, err := resolvePullRemote(cfg, "")
	if err != nil || host != "erp" || path != "/opt/odoo" {
		t.Fatalf("default = (%q,%q,%v), want (erp,/opt/odoo,nil)", host, path, err)
	}
	// Named target.
	host, path, err = resolvePullRemote(cfg, "prod")
	if err != nil || host != "prod-host" || path != "/srv/prod" {
		t.Fatalf("--from prod = (%q,%q,%v)", host, path, err)
	}
	// Unknown target.
	if _, _, err := resolvePullRemote(cfg, "nope"); err == nil {
		t.Error("unknown target should error")
	}
	// Target missing remote_path.
	if _, _, err := resolvePullRemote(cfg, "broken"); err == nil {
		t.Error("target without remote_path should error")
	}
	// No connect config at all.
	if _, _, err := resolvePullRemote(&config.Config{}, ""); !errors.Is(err, ErrNoPullRemote) {
		t.Errorf("empty cfg err = %v, want ErrNoPullRemote", err)
	}
}

func TestPickPullTarget(t *testing.T) {
	// Zero targets → ErrNoPullRemote.
	if _, err := pickPullTarget(&config.Config{}, theme.Palette{}, nil); !errors.Is(err, ErrNoPullRemote) {
		t.Errorf("0 targets err = %v, want ErrNoPullRemote", err)
	}

	// Exactly one target → auto-used, streamed.
	var streamed string
	cfg1 := &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "prod", SSHHost: "erp", RemotePath: "/opt/odoo"},
	}}
	name, err := pickPullTarget(cfg1, theme.Palette{}, func(s string) { streamed = s })
	if err != nil || name != "prod" {
		t.Fatalf("1 target = (%q, %v), want (prod, nil)", name, err)
	}
	if streamed != "using connect target prod" {
		t.Errorf("stream = %q, want the auto-use line", streamed)
	}

	// Several targets without a TTY → fails closed (picker is guarded).
	cfg2 := &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "prod", SSHHost: "erp", RemotePath: "/opt/odoo"},
		{Name: "stg", SSHHost: "stg", RemotePath: "/opt/stg"},
	}}
	if _, err := pickPullTarget(cfg2, theme.Palette{}, nil); err == nil {
		t.Error("multiple targets without TTY should error, got nil")
	}
}

func TestPullDest(t *testing.T) {
	root := t.TempDir()
	// A module present on the host lands in its real addons dir.
	if err := os.MkdirAll(filepath.Join(root, "addons", "sale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "addons", "sale", "__manifest__.py"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AddonsPaths: []string{"addons"}}

	gotHost := pullDest(cfg, root, "sale", "es_MX")
	wantHost := filepath.Join(root, "addons", "sale", "i18n", "es_MX.po")
	if gotHost != wantHost {
		t.Errorf("host-mode dest = %q, want %q", gotHost, wantHost)
	}

	// A module NOT on the host (conf-mode) falls back to cwd-relative.
	gotConf := pullDest(cfg, root, "habita_report_customs", "es_MX")
	wantConf := filepath.Join(root, "habita_report_customs", "i18n", "es_MX.po")
	if gotConf != wantConf {
		t.Errorf("conf-mode dest = %q, want %q", gotConf, wantConf)
	}
}

func TestRemoteContainerCmd(t *testing.T) {
	t.Run("two-word compose", func(t *testing.T) {
		got := remoteContainerCmd("/srv/odoo",
			connectTarget{composeCmd: "docker compose", odooContainer: "odoo"},
			odoo.Cmd{"cat", "/tmp/x.po"})
		want := "cd '/srv/odoo' && docker compose exec -T 'odoo' 'cat' '/tmp/x.po'"
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	})
	t.Run("db variant targets the db container", func(t *testing.T) {
		got := remoteDBCmd("/srv/odoo",
			connectTarget{composeCmd: "docker compose", odooContainer: "odoo", dbContainer: "db"},
			odoo.Cmd{"psql", "-U", "odoo", "-d", "prod", "-At", "-c", "SELECT 1"})
		want := "cd '/srv/odoo' && docker compose exec -T 'db' 'psql' '-U' 'odoo' '-d' 'prod' '-At' '-c' 'SELECT 1'"
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	})
	t.Run("quotes a path with spaces", func(t *testing.T) {
		got := remoteContainerCmd("/srv/my odoo",
			connectTarget{composeCmd: "docker-compose", odooContainer: "web"},
			odoo.Cmd{"rm", "-f", "/tmp/a b.po"})
		want := "cd '/srv/my odoo' && docker-compose exec -T 'web' 'rm' '-f' '/tmp/a b.po'"
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	})
}
