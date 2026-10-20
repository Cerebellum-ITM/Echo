package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
		{[]string{"--add"}, linkArgs{add: true}, false},
		{[]string{"--add", "--list"}, linkArgs{}, true},
		{[]string{"--add", "--show"}, linkArgs{}, true},
		{[]string{"--add", "prod"}, linkArgs{}, true},
		// --json only decorates --list and --show
		{[]string{"--show", "--json"}, linkArgs{show: true, jsonOut: true}, false},
		{[]string{"--json"}, linkArgs{}, true},
		{[]string{"--next", "--json"}, linkArgs{}, true},
		{[]string{"prod", "--json"}, linkArgs{}, true},
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

	got, err := resolveLinkTarget(context.Background(), opts, "prod")
	if err != nil || got.SSHHost != "erp.example.com" {
		t.Fatalf("resolveLinkTarget(prod) = %+v, %v", got, err)
	}

	if _, err := resolveLinkTarget(context.Background(), opts, "broken"); err == nil {
		t.Fatal("target without ssh_host/remote_path must error")
	}

	_, err = resolveLinkTarget(context.Background(), opts, "nope")
	if err == nil || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("unknown target error must list available names, got %v", err)
	}
}

func TestResolveLinkTargetImplicit(t *testing.T) {
	// No targets at all → ErrNoConnectTargets, with or without a name.
	empty := LinkOpts{Cfg: &config.Config{}}
	if _, err := resolveLinkTarget(context.Background(), empty, ""); !errors.Is(err, ErrNoConnectTargets) {
		t.Fatalf("no targets: got %v, want ErrNoConnectTargets", err)
	}
	if _, err := resolveLinkTarget(context.Background(), empty, "prod"); !errors.Is(err, ErrNoConnectTargets) {
		t.Fatalf("no targets + name: got %v, want ErrNoConnectTargets", err)
	}

	// A single target is auto-used without a picker.
	one := LinkOpts{Cfg: &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "stage", SSHHost: "stage.example.com", RemotePath: "/srv/odoo/stage"},
	}}}
	got, err := resolveLinkTarget(context.Background(), one, "")
	if err != nil || got.Name != "stage" {
		t.Fatalf("single target auto-pick = %+v, %v", got, err)
	}
}

// A target registered mid-session has to be visible to the picker,
// `--list` and `--next` right away, not after the next process start.
func TestRememberTarget(t *testing.T) {
	cfg := &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "prod", SSHHost: "old.example.com", RemotePath: "/srv/odoo/erp"},
	}}
	rememberTarget(cfg, config.ConnectTarget{Name: "stage", SSHHost: "stage.example.com", RemotePath: "/srv/odoo/stage"})
	if len(cfg.ConnectTargets) != 2 || cfg.ConnectTargets[1].Name != "stage" {
		t.Fatalf("new target not appended: %+v", cfg.ConnectTargets)
	}
	rememberTarget(cfg, config.ConnectTarget{Name: "prod", SSHHost: "new.example.com", RemotePath: "/srv/odoo/erp"})
	if len(cfg.ConnectTargets) != 2 || cfg.ConnectTargets[0].SSHHost != "new.example.com" {
		t.Fatalf("re-registered target not replaced: %+v", cfg.ConnectTargets)
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

// fakeLinkRemote puts a fake `ssh` first on PATH: `cat` of the server
// profile runs locally against a temp HOME, and `compose ps --format json`
// answers with one container, or fails when FAKE_PS_FAIL is set.
func fakeLinkRemote(t *testing.T, projectTOML string) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	remotePath := "/srv/shop"
	mustWrite(t, filepath.Join(home, ".config/echo/projects", config.ProjectKey(remotePath)+".toml"), projectTOML)
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "ssh"), `#!/bin/sh
while [ "$1" = "-o" ]; do shift 2; done
shift
cmd="$*"
case "$cmd" in
  *--format*) [ -z "$FAKE_PS_FAIL" ] || exit 1
    printf '%s\n' '{"Service":"odoo","State":"running","Health":"healthy","Status":"Up 3 hours"}' ;;
  cat*) exec sh -c "$cmd" ;;
  *) exit 1 ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &config.Config{
		ConnectSSHHost:    "shop-host",
		ConnectRemotePath: remotePath,
		ConnectTargets:    []config.ConnectTarget{{Name: "develop", SSHHost: "shop-host", RemotePath: remotePath}},
	}
}

func runLinkShowJSONFor(t *testing.T, cfg *config.Config) (*LinkShowResult, error) {
	t.Helper()
	var got *LinkShowResult
	err := RunLink(context.Background(), LinkOpts{
		Cfg:    cfg,
		Args:   []string{"--show", "--json"},
		Log:    func(string, string, string, string, ...[2]string) {},
		OnShow: func(res LinkShowResult) { got = &res },
	})
	return got, err
}

func TestLinkShowJSONAssemblesReport(t *testing.T) {
	cfg := fakeLinkRemote(t, "stage = \"Staging\"\ndb_name = \"shop\"\nodoo_version = \"18.0\"\n"+
		"[reverb]\nproject = \"p\"\nenv = \"e\"\n")

	got, err := runLinkShowJSONFor(t, cfg)
	if err != nil || got == nil {
		t.Fatalf("RunLink --show --json = %v, %v", got, err)
	}
	b, _ := json.Marshal(got)
	want := `{"target":"develop","ssh_host":"shop-host","remote_path":"/srv/shop","stage":"staging",` +
		`"stage_declared":true,"db":"shop","odoo_version":"18.0","compose":"docker compose",` +
		`"reverb":{"project":"p","env":"e"},"deployed":null,` +
		`"containers":[{"service":"odoo","state":"running","health":"healthy","status":"Up 3 hours"}]}`
	if string(b) != want {
		t.Fatalf("report\n got: %s\nwant: %s", b, want)
	}
}

func TestLinkShowJSONUndeclaredStageAndFailedPS(t *testing.T) {
	cfg := fakeLinkRemote(t, "db_name = \"shop\"\n")
	t.Setenv("FAKE_PS_FAIL", "1")

	got, err := runLinkShowJSONFor(t, cfg)
	if err != nil || got == nil {
		t.Fatalf("RunLink --show --json = %v, %v", got, err)
	}
	if got.Stage != "prod" || got.StageDeclared {
		t.Errorf("stage = %q declared=%v, want prod/false", got.Stage, got.StageDeclared)
	}
	if got.Containers != nil || got.Reverb != nil {
		t.Errorf("containers = %v reverb = %v, want both nil", got.Containers, got.Reverb)
	}
}

func TestLinkShowJSONDeployedCode(t *testing.T) {
	cfg := fakeLinkRemote(t, "stage = \"dev\"\n")
	cfg.ConnectTargets[0].GitDeploy = true
	orig := gitRunSSH
	t.Cleanup(func() { gitRunSSH = orig })
	gitRunSSH = func(_ context.Context, _, remoteCmd string, _ []byte) ([]byte, error) {
		if strings.Contains(remoteCmd, deployedSHAKey) {
			return []byte("0123456789abcdef\n"), nil
		}
		return nil, errors.New("unset")
	}

	got, err := runLinkShowJSONFor(t, cfg)
	if err != nil || got == nil {
		t.Fatalf("RunLink --show --json = %v, %v", got, err)
	}
	want := &LinkShowDeployed{Branch: defaultDeployBranch, SHA: "0123456789abcdef"}
	if got.Deployed == nil || *got.Deployed != *want {
		t.Fatalf("deployed = %+v, want %+v", got.Deployed, want)
	}
}

func TestLinkShowJSONFailures(t *testing.T) {
	if _, err := runLinkShowJSONFor(t, &config.Config{}); !errors.Is(err, ErrUsage) {
		t.Errorf("unbound: err = %v, want ErrUsage", err)
	}
	cfg := fakeLinkRemote(t, "stage = \"dev\"\n")
	cfg.ConnectRemotePath = "/srv/elsewhere"
	got, err := runLinkShowJSONFor(t, cfg)
	if err == nil || errors.Is(err, ErrUsage) || got != nil {
		t.Errorf("missing server profile: got %v, err = %v; want an execution error and no report", got, err)
	}
}

func TestRunLinkListEmptyErrors(t *testing.T) {
	opts := LinkOpts{Cfg: &config.Config{}, Log: func(string, string, string, string, ...[2]string) {}}
	if err := runLinkList(opts, linkArgs{list: true}); !errors.Is(err, ErrNoConnectTargets) {
		t.Errorf("err = %v, want ErrNoConnectTargets", err)
	}
}
