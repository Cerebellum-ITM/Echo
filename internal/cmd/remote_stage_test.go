package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

const undeclaredStageWarning = "target stage is not declared on the server"

func TestRemoteConnectTargetStage(t *testing.T) {
	cases := []struct {
		raw, stage string
		declared   bool
	}{
		{"dev", "dev", true},
		{" STAGING ", "staging", true},
		{"Prod", "prod", true},
		{"", "prod", false},
		{"production", "prod", false},
	}
	for _, c := range cases {
		got := remoteConnectTarget(config.RemoteProfile{Stage: c.raw})
		if got.stage != c.stage || got.stageDeclared != c.declared || got.rawStage != c.raw {
			t.Errorf("stage %q -> stage=%q declared=%v raw=%q, want %q %v %q",
				c.raw, got.stage, got.stageDeclared, got.rawStage, c.stage, c.declared, c.raw)
		}
	}
}

func (f *fakeRemote) writeProfile(t *testing.T, body string) {
	t.Helper()
	mustWrite(t, filepath.Join(os.Getenv("HOME"), ".config/echo/projects", config.ProjectKey(f.dir)+".toml"), body)
}

func (r *logRecorder) count(substr string) int {
	n := 0
	for _, l := range r.lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

func TestDeployFailsOnUnparseableServerProfile(t *testing.T) {
	remote := newFakeRemote(t)
	r, _ := incidentRepo(t)
	remote.writeProfile(t, "stage = \"dev\"\ndb_name = = \"stg\"\n")
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	before := treeFiles(t, remote.dir)

	args := []string{"--modules", "sale", "--from", "stg", "--push", "--force", "--no-lint", "--dry-run"}
	_, err := RunDeploy(context.Background(), DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
	if err == nil || !strings.Contains(err.Error(), "does not parse: line 2") || !strings.Contains(err.Error(), "on fakehost") {
		t.Fatalf("err = %v, want the server profile parse error", err)
	}
	if !reflect.DeepEqual(treeFiles(t, remote.dir), before) || remote.composeLog() != "" {
		t.Error("the server changed before the profile was rejected")
	}
}

func TestDeployUndeclaredStageGatesAsProd(t *testing.T) {
	remote := newFakeRemote(t)
	r, _ := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	args := []string{"--modules", "sale", "--from", "stg", "--push", "--no-lint", "--dry-run"}

	dryRun := func(profile string) *logRecorder {
		t.Helper()
		remote.writeProfile(t, profile)
		rec := &logRecorder{}
		if _, err := RunDeploy(context.Background(), DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args, Log: rec.log}); err != nil {
			t.Fatalf("dry-run: %v", err)
		}
		return rec
	}

	undeclared := dryRun("db_name = \"stg\"\nodoo_version = \"18\"\n")
	if n := undeclared.count(undeclaredStageWarning); n != 1 {
		t.Errorf("undeclared-stage warnings = %d, want 1:\n%s", n, strings.Join(undeclared.lines, "\n"))
	}
	if undeclared.count("checkpoint enabled") != 1 {
		t.Error("the checkpoint is not on by default for an undeclared stage")
	}

	dev := dryRun("stage = \"dev\"\ndb_name = \"stg\"\nodoo_version = \"18\"\n")
	if dev.count(undeclaredStageWarning) != 0 || dev.count("checkpoint enabled") != 0 {
		t.Errorf("a dev target changed behaviour:\n%s", strings.Join(dev.lines, "\n"))
	}
}

func TestRemoteRestartUndeclaredStageAsksProd(t *testing.T) {
	remote := newFakeRemote(t)
	prevTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = prevTTY })

	restart := func(args ...string) (*logRecorder, error) {
		rec := &logRecorder{}
		err := RunRestart(context.Background(), DockerOpts{
			Cfg: remote.cfg(), Root: t.TempDir(), Args: append([]string{"--from", "stg"}, args...),
			Log: rec.log, StreamOut: func(string) {},
		})
		return rec, err
	}

	remote.writeProfile(t, "db_name = \"stg\"\nodoo_container = \"odoo\"\n")
	rec, err := restart()
	if !errors.Is(err, ErrNonInteractive) {
		t.Fatalf("err = %v, want the prod confirm to fail closed without a TTY", err)
	}
	if strings.Contains(remote.composeLog(), "restart") {
		t.Error("restart ran without confirmation")
	}
	if n := rec.count(undeclaredStageWarning); n != 1 {
		t.Errorf("undeclared-stage warnings = %d, want 1", n)
	}
	if _, err := restart("--force"); err != nil || !strings.Contains(remote.composeLog(), "restart") {
		t.Errorf("--force: err = %v, compose log = %q", err, remote.composeLog())
	}

	remote.writeProfile(t, "stage = \"dev\"\ndb_name = \"stg\"\nodoo_container = \"odoo\"\n")
	if rec, err := restart(); err != nil || rec.count(undeclaredStageWarning) != 0 {
		t.Errorf("dev target: err = %v, lines = %v", err, rec.lines)
	}
}

func TestRemoteEchoProjectsSkipsUnparseableProfile(t *testing.T) {
	remote := newFakeRemote(t)
	remote.writeProfile(t, "project_path = \""+remote.dir+"\"\ndb_name = \"stg\"\n")
	broken := filepath.Join(os.Getenv("HOME"), ".config/echo/projects/broken.toml")
	mustWrite(t, broken, "db_name = = \"x\"\n")

	rec := &logRecorder{}
	projects, err := remoteEchoProjects(context.Background(), "fakehost", rec.log)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].DBName != "stg" {
		t.Errorf("projects = %+v, want only the parseable one", projects)
	}
	if rec.count("skipped a server profile that does not parse file="+broken) != 1 {
		t.Errorf("log = %v, want one WARNING naming %s", rec.lines, broken)
	}
}
