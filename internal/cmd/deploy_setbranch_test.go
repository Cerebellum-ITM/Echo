package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseSetGitBranchFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    deployArgs
		wantErr bool
	}{
		{
			name: "name as a separate token",
			args: []string{"--set-git-branch", "echo/deploy"},
			want: deployArgs{limit: 20, setGitBranch: "echo/deploy"},
		},
		{
			name: "with rename and target",
			args: []string{"--set-git-branch=echo/deploy", "--rename", "--from", "develop"},
			want: deployArgs{limit: 20, setGitBranch: "echo/deploy", rename: true, from: "develop"},
		},
		{name: "missing name", args: []string{"--set-git-branch"}, wantErr: true},
		{name: "empty name", args: []string{"--set-git-branch="}, wantErr: true},
		{name: "with a selection", args: []string{"--set-git-branch", "b", "--modules", "sale"}, wantErr: true},
		{name: "with --set-code", args: []string{"--set-git-branch", "b", "--set-code", "main"}, wantErr: true},
		{name: "rename alone", args: []string{"--rename"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDeployArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("expected ErrUsage, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestSaveGitBranchWritesTheNamedTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	target := config.ConnectTarget{
		Name: "develop", SSHHost: "host", RemotePath: "/srv/odoo",
		GitDeploy: true, GitBranch: "staging", GitPath: "addons",
	}
	if err := config.SaveConnectTarget(target); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	opts := DeployOpts{Cfg: &config.Config{ConnectTargets: []config.ConnectTarget{target}}}

	if err := saveGitBranch(opts, "develop", "host", "/srv/odoo", "echo/deploy"); err != nil {
		t.Fatalf("save: %v", err)
	}
	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(g.ConnectTargets) != 1 {
		t.Fatalf("expected one target, got %d", len(g.ConnectTargets))
	}
	got := g.ConnectTargets[0]
	if got.GitBranch != "echo/deploy" {
		t.Errorf("git_branch = %q", got.GitBranch)
	}
	// The rest of the target must survive the edit.
	if got.SSHHost != "host" || got.RemotePath != "/srv/odoo" || got.GitPath != "addons" || !got.GitDeploy {
		t.Errorf("target was clobbered: %+v", got)
	}
	if opts.Cfg.ConnectTargets[0].GitBranch != "echo/deploy" {
		t.Error("in-memory config not updated")
	}
}

func TestSaveGitBranchWithoutATargetIsUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	opts := DeployOpts{Cfg: &config.Config{}}
	if err := saveGitBranch(opts, "", "host", "/srv/odoo", "echo/deploy"); !errors.Is(err, ErrUsage) {
		t.Fatalf("expected ErrUsage, got %v", err)
	}
}

func TestGitRenameBranchMovesOnlyTheRef(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{"branch": {}})
	if err := gitRenameBranch(context.Background(), "host", "/srv/odoo", "staging", "echo/deploy"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !containsCall(*calls, "branch", "-m", "staging", "echo/deploy") {
		t.Errorf("rename not issued: %v", *calls)
	}
	// A rename must not touch the working tree: no checkout, no reset — that is
	// what lets the dirty overlay survive it.
	for _, c := range *calls {
		if strings.Contains(c, "checkout") || strings.Contains(c, "reset") {
			t.Errorf("rename must not touch the working tree, ran: %s", c)
		}
	}
}

func TestRecordDeployedRefWritesAndClears(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{"config": {}})
	recordDeployedRef(context.Background(), "host", "/srv/odoo", "origin/main", "tipsha", nil)
	if !containsCall(*calls, "config", deployedRefKey, "origin/main") {
		t.Error("ref not recorded")
	}
	if !containsCall(*calls, "config", deployedSHAKey, "tipsha") {
		t.Error("sha not recorded")
	}
	if !containsCall(*calls, "config", deployedAtKey) {
		t.Error("timestamp not recorded")
	}

	// A restore has no ref: the key is unset rather than left describing a line
	// the checkout is no longer on.
	cleared := scriptedGit(t, map[string]scriptResp{"config": {}})
	recordDeployedRef(context.Background(), "host", "/srv/odoo", "", "oldsha", nil)
	if !containsCall(*cleared, "config", "--unset", deployedRefKey) {
		t.Error("ref not cleared")
	}
	for _, c := range *cleared {
		if strings.Contains(c, deployedRefKey) && !strings.Contains(c, "--unset") {
			t.Errorf("a refless move must not write the ref key: %s", c)
		}
	}
}

func TestReadDeployedCode(t *testing.T) {
	scriptedGit(t, map[string]scriptResp{
		deployedRefKey: {out: "origin/deploy/dev\n"},
		deployedSHAKey: {out: "a1b2c3d\n"},
		deployedAtKey:  {out: "2026-08-13T21:40:00Z\n"},
	})
	ref, sha, at := readDeployedCode(context.Background(), "host", "/srv/odoo")
	if ref != "origin/deploy/dev" || sha != "a1b2c3d" || at != "2026-08-13T21:40:00Z" {
		t.Errorf("got ref=%q sha=%q at=%q", ref, sha, at)
	}
}
