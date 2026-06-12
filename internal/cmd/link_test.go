package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseLinkArgs(t *testing.T) {
	cases := []struct {
		in      []string
		want    linkArgs
		wantErr bool
	}{
		{nil, linkArgs{}, false},
		{[]string{"prod"}, linkArgs{target: "prod"}, false},
		{[]string{"--show"}, linkArgs{show: true}, false},
		{[]string{"--rm"}, linkArgs{rm: true}, false},
		{[]string{"--show", "--rm"}, linkArgs{}, true},
		{[]string{"prod", "--show"}, linkArgs{}, true},
		{[]string{"prod", "--rm"}, linkArgs{}, true},
		{[]string{"a", "b"}, linkArgs{}, true},
		{[]string{"--bogus"}, linkArgs{}, true},
	}
	for _, tc := range cases {
		got, err := parseLinkArgs(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseLinkArgs(%v): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseLinkArgs(%v): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseLinkArgs(%v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestResolveLinkTargetExplicit(t *testing.T) {
	cfg := &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "prod", SSHHost: "erp.example.com", RemotePath: "/srv/odoo/erp"},
		{Name: "broken", SSHHost: "", RemotePath: ""},
	}}
	opts := LinkOpts{Cfg: cfg}

	got, err := resolveLinkTarget(opts, "prod")
	if err != nil || got.SSHHost != "erp.example.com" {
		t.Fatalf("resolveLinkTarget(prod) = %+v, %v", got, err)
	}

	if _, err := resolveLinkTarget(opts, "broken"); err == nil {
		t.Fatal("target without ssh_host/remote_path must error")
	}

	_, err = resolveLinkTarget(opts, "nope")
	if err == nil || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("unknown target error must list available names, got %v", err)
	}
}

func TestResolveLinkTargetImplicit(t *testing.T) {
	// No targets at all → ErrNoConnectTargets, with or without a name.
	empty := LinkOpts{Cfg: &config.Config{}}
	if _, err := resolveLinkTarget(empty, ""); !errors.Is(err, ErrNoConnectTargets) {
		t.Fatalf("no targets: got %v, want ErrNoConnectTargets", err)
	}
	if _, err := resolveLinkTarget(empty, "prod"); !errors.Is(err, ErrNoConnectTargets) {
		t.Fatalf("no targets + name: got %v, want ErrNoConnectTargets", err)
	}

	// A single target is auto-used without a picker.
	one := LinkOpts{Cfg: &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "stage", SSHHost: "stage.example.com", RemotePath: "/srv/odoo/stage"},
	}}}
	got, err := resolveLinkTarget(one, "")
	if err != nil || got.Name != "stage" {
		t.Fatalf("single target auto-pick = %+v, %v", got, err)
	}
}

func TestLinkTargetName(t *testing.T) {
	cfg := &config.Config{
		ConnectSSHHost:    "erp.example.com",
		ConnectRemotePath: "/srv/odoo/erp",
		ConnectTargets: []config.ConnectTarget{
			{Name: "prod", SSHHost: "erp.example.com", RemotePath: "/srv/odoo/erp"},
		},
	}
	if got := linkTargetName(cfg); got != "prod" {
		t.Fatalf("linkTargetName = %q, want prod", got)
	}
	cfg.ConnectRemotePath = "/elsewhere"
	if got := linkTargetName(cfg); got != "" {
		t.Fatalf("hand-written binding must yield \"\", got %q", got)
	}
}
