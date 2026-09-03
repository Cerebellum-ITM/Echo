package cmd

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestParseSetCodeFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    deployArgs
		wantErr bool
	}{
		{
			name: "ref as a separate token",
			args: []string{"--set-code", "origin/main"},
			want: deployArgs{limit: 20, setCodeSet: true, setCode: "origin/main"},
		},
		{
			name: "ref with =",
			args: []string{"--set-code=v1.4.0", "--keep-overlay"},
			want: deployArgs{limit: 20, setCodeSet: true, setCode: "v1.4.0", keepOverlay: true},
		},
		{
			name: "with-local and no-fetch",
			args: []string{"--set-code", "main", "--with-local", "--no-fetch"},
			want: deployArgs{limit: 20, setCodeSet: true, setCode: "main", withLocal: true, noFetch: true},
		},
		{name: "bare --set-code has no picker", args: []string{"--set-code"}, wantErr: true},
		{name: "empty =ref", args: []string{"--set-code="}, wantErr: true},
		{name: "flag as ref", args: []string{"--set-code", "--force"}, wantErr: true},
		{name: "with a selection", args: []string{"--set-code", "main", "--commits", "abc"}, wantErr: true},
		{name: "with --rollback", args: []string{"--set-code", "main", "--rollback"}, wantErr: true},
		{name: "with --restore-code", args: []string{"--set-code", "main", "--restore-code", "abc"}, wantErr: true},
		{name: "with --no-git", args: []string{"--set-code", "main", "--no-git"}, wantErr: true},
		{name: "fetch and no-fetch", args: []string{"--set-code", "main", "--fetch", "--no-fetch"}, wantErr: true},
		{name: "keep-overlay without set-code", args: []string{"--keep-overlay"}, wantErr: true},
		{name: "with-local without set-code", args: []string{"--with-local"}, wantErr: true},
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

func TestPlanGitSetCodeIsReadOnly(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{
		"--version":           {},
		"is-inside-work-tree": {out: "true"},
		"cat-file":            {},
		"refs/heads/":         {out: "prevsha"},
		"status":              {out: " M addons/foo/a.py\n?? odoo.conf"},
	})
	// A real repo: the preflight reads this repo's root commit locally.
	opts := DeployOpts{Root: newTestRepo(t)}
	plan, err := planGitSetCode(context.Background(), opts, testRSC(), gitDeployConfig{enabled: true, branch: "echo/deploy"}, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.prev != "prevsha" {
		t.Errorf("prev = %q, want prevsha", plan.prev)
	}
	// odoo.conf maps to no module: a re-baseline must never touch the server's
	// own files, only module paths.
	if len(plan.cleaned) != 1 || plan.cleaned[0].path != "addons/foo/a.py" {
		t.Errorf("cleaned = %+v, want only addons/foo/a.py", plan.cleaned)
	}
	for _, c := range *calls {
		for _, mutating := range []string{"'reset'", "'clean'", "'branch'", "'checkout'", "update-ref"} {
			if strings.Contains(c, mutating) {
				t.Errorf("planning must not mutate, but ran: %s", c)
			}
		}
	}
}

func TestApplyGitSetCodeOrderAndNoFFGate(t *testing.T) {
	calls := scriptedGit(t, map[string]scriptResp{
		"'--verify'":         {out: "refs/heads/echo/deploy"},
		"'--abbrev-ref'":     {out: "echo/deploy"},
		"'rev-parse' 'HEAD'": {out: "prevsha"},
		"status":             {},
		"diff":               {},
		"ls-tree":            {},
		"checkout":           {},
		"clean":              {},
		"reset":              {},
		"update-ref":         {},
	})
	origPush := gitPushCommand
	pushed := ""
	gitPushCommand = func(ctx context.Context, root, sshHost, absDir, refspec string) *exec.Cmd {
		pushed = refspec
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { gitPushCommand = origPush })

	plan := setCodePlan{
		absDir:  "/srv/odoo",
		prev:    "prevsha",
		cleaned: []remoteDirtyEntry{{path: "addons/foo/a.py"}, {path: "addons/foo/new.py", untracked: true}},
	}
	opts := DeployOpts{Root: newTestRepo(t)}
	if err := applyGitSetCode(context.Background(), opts, testRSC(),
		gitDeployConfig{enabled: true, branch: "echo/deploy"}, plan, "tipsha", "origin/main"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if pushed != "tipsha:"+incomingRef {
		t.Errorf("pushed refspec = %q", pushed)
	}
	// The absent fast-forward gate IS the feature: a set-code moves the branch
	// backwards or sideways, which is what a deploy must never do.
	for _, c := range *calls {
		if strings.Contains(c, "merge-base") {
			t.Error("set-code must not run the fast-forward gate")
		}
	}
	// Order: the overlay is cleaned before the branch moves, so the advance has
	// nothing left to discard.
	cleanIdx, resetIdx := -1, -1
	for i, c := range *calls {
		if cleanIdx == -1 && strings.Contains(c, "'clean'") {
			cleanIdx = i
		}
		if resetIdx == -1 && strings.Contains(c, "'reset'") {
			resetIdx = i
		}
	}
	if cleanIdx == -1 || resetIdx == -1 || cleanIdx > resetIdx {
		t.Errorf("clean must precede the reset (clean=%d reset=%d)", cleanIdx, resetIdx)
	}
	if !containsCall(*calls, "reset", "tipsha") {
		t.Error("branch not reset to the tip")
	}
}

func TestModuleScopedEntries(t *testing.T) {
	in := []remoteDirtyEntry{
		{path: "addons/foo/a.py"},
		{path: "odoo.conf"},
		{path: "docker-compose.override.yml"},
		{path: "custom/bar/views/x.xml", untracked: true},
	}
	got := moduleScopedEntries(in)
	want := []string{"addons/foo/a.py", "custom/bar/views/x.xml"}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %v", got, want)
	}
	for i, e := range got {
		if e.path != want[i] {
			t.Errorf("[%d] = %q, want %q", i, e.path, want[i])
		}
	}
}
