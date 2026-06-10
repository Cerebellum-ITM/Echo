package cmd

import (
	"errors"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
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
