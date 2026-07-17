package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

func TestParseI18nPullToWorktree(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantPick bool
		wantTo   string
		wantMods []string
		wantErr  bool
	}{
		{"bare picker", []string{"sale", "--to-worktree"}, true, "", []string{"sale"}, false},
		{"explicit branch", []string{"sale", "--to-worktree=feat/x"}, false, "feat/x", []string{"sale"}, false},
		{"bare form keeps next token as module", []string{"--to-worktree", "sale"}, true, "", []string{"sale"}, false},
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
			if got.pickWorktree != tc.wantPick || got.toWorktree != tc.wantTo ||
				!reflect.DeepEqual(got.modules, tc.wantMods) {
				t.Errorf("got pick=%v to=%q mods=%q, want pick=%v to=%q mods=%q",
					got.pickWorktree, got.toWorktree, got.modules, tc.wantPick, tc.wantTo, tc.wantMods)
			}
		})
	}
}

func TestParseI18nPullArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantMods []string
		wantLang string
		wantFrom string
		wantAll  bool
		wantErr  bool
	}{
		{"defaults", nil, nil, "es_MX", "", false, false},
		{"module only", []string{"sale"}, []string{"sale"}, "es_MX", "", false, false},
		{"module + lang", []string{"sale", "fr_FR"}, []string{"sale"}, "fr_FR", "", false, false},
		{"two modules + lang", []string{"sale", "account", "es_MX"}, []string{"sale", "account"}, "es_MX", "", false, false},
		{"two modules no lang", []string{"sale", "account"}, []string{"sale", "account"}, "es_MX", "", false, false},
		{"explicit lang flag makes all positionals modules", []string{"sale", "account", "--lang", "fr_FR"}, []string{"sale", "account"}, "fr_FR", "", false, false},
		{"explicit lang keeps locale-shaped module", []string{"sale", "es_MX", "account", "--lang=pt_BR"}, []string{"sale", "es_MX", "account"}, "pt_BR", "", false, false},
		{"from flag", []string{"sale", "--from", "prod"}, []string{"sale"}, "es_MX", "prod", false, false},
		{"from equals", []string{"sale", "--from=prod"}, []string{"sale"}, "es_MX", "prod", false, false},
		{"all", []string{"--all"}, nil, "es_MX", "", true, false},
		{"all + lang", []string{"--all", "fr_FR"}, nil, "fr_FR", "", true, false},
		{"all + extra positional", []string{"--all", "sale", "fr_FR"}, nil, "", "", true, true},
		{"all + lang flag rejects positional", []string{"--all", "sale", "--lang", "fr_FR"}, nil, "", "", true, true},
		{"unknown flag", []string{"--bogus"}, nil, "", "", false, true},
		{"from without value", []string{"--from"}, nil, "", "", false, true},
		{"lang without value", []string{"--lang"}, nil, "", "", false, true},
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
			if !reflect.DeepEqual(got.modules, tc.wantMods) || got.lang != tc.wantLang ||
				got.from != tc.wantFrom || got.all != tc.wantAll {
				t.Errorf("got %+v, want mods=%q lang=%q from=%q all=%v",
					got, tc.wantMods, tc.wantLang, tc.wantFrom, tc.wantAll)
			}
		})
	}
}

func TestIsLocale(t *testing.T) {
	for _, s := range []string{"es", "es_MX", "pt_BR", "sr@latin"} {
		if !isLocale(s) {
			t.Errorf("isLocale(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"sale", "account", "Sale", "account_move", ""} {
		if isLocale(s) {
			t.Errorf("isLocale(%q) = true, want false", s)
		}
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

func TestParseI18nPullInstalled(t *testing.T) {
	got, err := parseI18nPullArgs([]string{"sale", "--installed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.modules) != 1 || got.modules[0] != "sale" || !got.installed {
		t.Errorf("got %+v, want modules=[sale] installed=true", got)
	}
	def, _ := parseI18nPullArgs([]string{"sale"})
	if def.installed {
		t.Error("installed should default to false")
	}
}

func TestDedupeSortedLines(t *testing.T) {
	got := dedupeSortedLines("real_estate_bits\nccima\n\n real_estate_bits \nccima_crm\n")
	want := []string{"ccima", "ccima_crm", "real_estate_bits"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

func TestPickPullTarget(t *testing.T) {
	// Zero targets → ErrNoPullRemote.
	if _, err := pickPullTarget(I18nPullOpts{Cfg: &config.Config{}}); !errors.Is(err, ErrNoPullRemote) {
		t.Errorf("0 targets err = %v, want ErrNoPullRemote", err)
	}

	// Exactly one target → auto-used, logged.
	var loggedMsg string
	cfg1 := &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "prod", SSHHost: "erp", RemotePath: "/opt/odoo"},
	}}
	name, err := pickPullTarget(I18nPullOpts{Cfg: cfg1, Log: func(level, sub, msg, db string, fields ...[2]string) {
		loggedMsg = msg
	}})
	if err != nil || name != "prod" {
		t.Fatalf("1 target = (%q, %v), want (prod, nil)", name, err)
	}
	if loggedMsg != "using connect target" {
		t.Errorf("logged msg = %q, want the auto-use line", loggedMsg)
	}

	// Several targets without a TTY → fails closed (picker is guarded).
	cfg2 := &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "prod", SSHHost: "erp", RemotePath: "/opt/odoo"},
		{Name: "stg", SSHHost: "stg", RemotePath: "/opt/stg"},
	}}
	if _, err := pickPullTarget(I18nPullOpts{Cfg: cfg2}); err == nil {
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
