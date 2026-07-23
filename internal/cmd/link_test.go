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
		{[]string{"--next"}, linkArgs{next: true}, false},
		{[]string{"--list"}, linkArgs{list: true}, false},
		{[]string{"--list", "--json"}, linkArgs{list: true, jsonOut: true}, false},
		// every mode pair is mutually exclusive
		{[]string{"--next", "--list"}, linkArgs{}, true},
		{[]string{"--next", "--show"}, linkArgs{}, true},
		{[]string{"--next", "--rm"}, linkArgs{}, true},
		{[]string{"--next", "prod"}, linkArgs{}, true},
		{[]string{"--list", "--show"}, linkArgs{}, true},
		{[]string{"--list", "--rm"}, linkArgs{}, true},
		{[]string{"--list", "prod"}, linkArgs{}, true},
		// --json only decorates --list
		{[]string{"--json"}, linkArgs{}, true},
		{[]string{"--next", "--json"}, linkArgs{}, true},
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

func TestNextTargetIndex(t *testing.T) {
	three := []config.ConnectTarget{{Name: "dev"}, {Name: "prod"}, {Name: "qa"}}
	two := []config.ConnectTarget{{Name: "dev"}, {Name: "prod"}}

	t.Run("cycles forward", func(t *testing.T) {
		got, err := nextTargetIndex(three, "dev")
		if err != nil || got != 1 {
			t.Fatalf("got %d err=%v, want 1", got, err)
		}
	})
	t.Run("wraps at the end", func(t *testing.T) {
		got, err := nextTargetIndex(three, "qa")
		if err != nil || got != 0 {
			t.Fatalf("got %d err=%v, want 0", got, err)
		}
	})
	t.Run("two targets toggle", func(t *testing.T) {
		a, _ := nextTargetIndex(two, "dev")
		b, _ := nextTargetIndex(two, "prod")
		if a != 1 || b != 0 {
			t.Errorf("toggle broken: dev->%d prod->%d", a, b)
		}
	})
	t.Run("unlinked starts at the first target", func(t *testing.T) {
		got, err := nextTargetIndex(three, "")
		if err != nil || got != 0 {
			t.Fatalf("got %d err=%v, want 0", got, err)
		}
	})
	t.Run("hand-written binding matching no target starts at 0", func(t *testing.T) {
		got, err := nextTargetIndex(three, "not-registered")
		if err != nil || got != 0 {
			t.Fatalf("got %d err=%v, want 0", got, err)
		}
	})
	t.Run("fewer than two targets is a usage error", func(t *testing.T) {
		for _, targets := range [][]config.ConnectTarget{nil, {{Name: "only"}}} {
			if _, err := nextTargetIndex(targets, ""); !errors.Is(err, ErrUsage) {
				t.Errorf("targets=%v: err = %v, want ErrUsage", targets, err)
			}
		}
	})
}

// --list must stay offline (no SSH, no write) and mark exactly one current.
func TestRunLinkListMarksCurrent(t *testing.T) {
	cfg := &config.Config{
		ConnectSSHHost:    "host-b",
		ConnectRemotePath: "/srv/b",
		ConnectTargets: []config.ConnectTarget{
			{Name: "alpha", SSHHost: "host-a", RemotePath: "/srv/a", DBName: "adb"},
			{Name: "beta", SSHHost: "host-b", RemotePath: "/srv/b", DBName: "bdb"},
		},
	}
	var lines []string
	opts := LinkOpts{Cfg: cfg, Log: func(level, sub, msg, db string, f ...[2]string) {
		lines = append(lines, msg)
	}}
	if err := runLinkList(opts, linkArgs{list: true}); err != nil {
		t.Fatalf("runLinkList: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("want one line per target, got %v", lines)
	}
	marked := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "●") {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("exactly one row must be marked current, got %d in %v", marked, lines)
	}
	if !strings.Contains(lines[1], "beta") || !strings.HasPrefix(lines[1], "●") {
		t.Errorf("beta is the current binding, got %q", lines[1])
	}
}

func TestRunLinkListEmptyErrors(t *testing.T) {
	opts := LinkOpts{Cfg: &config.Config{}, Log: func(string, string, string, string, ...[2]string) {}}
	if err := runLinkList(opts, linkArgs{list: true}); !errors.Is(err, ErrNoConnectTargets) {
		t.Errorf("err = %v, want ErrNoConnectTargets", err)
	}
}
