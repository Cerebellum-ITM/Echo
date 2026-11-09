package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requirementsRepo(t *testing.T) (r *sourceRepo, reqOnly string) {
	r = newSourceRepo(t)
	r.write("addons/sale/__manifest__.py", "{'name': 'sale', 'version': '18.0.1.0'}")
	r.write("requirements.txt", "fastapi\n")
	r.commit("[ADD] sale: base")
	r.write("requirements.txt", "fastapi\nhttpx\n")
	reqOnly = r.commit("[FIX] requirements: install httpx")
	return r, reqOnly
}

func TestPathsTouchRequirements(t *testing.T) {
	if !pathsTouchRequirements([]string{"sale/models.py", "requirements.txt"}) {
		t.Error("root requirements.txt not detected")
	}
	if pathsTouchRequirements([]string{"sale/requirements.txt"}) {
		t.Error("a module's own requirements.txt is not the project's")
	}
}

func TestRequirementsDelta(t *testing.T) {
	added, removed := requirementsDelta([]byte("# pinned\nfastapi\nrequests==2.31\n"), []byte("fastapi\n\nhttpx\nrequests==2.32\n"))
	if strings.Join(added, ",") != "httpx,requests==2.32" || strings.Join(removed, ",") != "requests==2.31" {
		t.Errorf("added=%v removed=%v", added, removed)
	}
}

func TestRunDeployRequirementsOnlyCommit(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, reqOnly := requirementsRepo(t)
	serverReq := filepath.Join(remote.dir, "addons/requirements.txt")
	mustWrite(t, serverReq, "fastapi\n")
	r.write("requirements.txt", "uncommitted\n")

	run := func(extra ...string) (DeployResult, error) {
		args := append([]string{"--commits", reqOnly[:7], "--from", "stg", "--force", "--no-lint"}, extra...)
		return RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
	}

	if _, err := run("--no-push"); !errors.Is(err, ErrUsage) {
		t.Fatalf("requirements-only without push: err = %v, want ErrUsage", err)
	}

	res, err := run("--push", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if req := res.Requirements; req == nil || !req.Changed || req.Synced || req.Source != lockSourceCommit || req.SHA != reqOnly {
		t.Fatalf("dry-run requirements = %+v", res.Requirements)
	}
	if b, _ := os.ReadFile(serverReq); string(b) != "fastapi\n" || remote.composeLog() != "" {
		t.Fatal("the dry-run changed the server")
	}

	res, err = run("--push")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if b, _ := os.ReadFile(serverReq); string(b) != "fastapi\nhttpx\n" {
		t.Errorf("server requirements = %q, want the commit's, not the disk's", b)
	}
	if res.Requirements == nil || !res.Requirements.Synced || res.Skipped != 0 || len(res.Modules) != 0 {
		t.Errorf("result = %+v requirements=%+v", res, res.Requirements)
	}
	log := remote.composeLog()
	if strings.Contains(log, "stop-after-init") || !strings.Contains(log, "up") {
		t.Errorf("compose log = %q, want a restart without an Odoo run", log)
	}

	res, err = run("--push", "--dry-run")
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	if res.Requirements == nil || res.Requirements.Changed {
		t.Errorf("after the sync requirements = %+v, want unchanged", res.Requirements)
	}
}

func TestRunDeployModulesCarryRequirements(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, _ := requirementsRepo(t)
	serverReq := filepath.Join(remote.dir, "addons/requirements.txt")
	mustWrite(t, serverReq, "fastapi\n")

	args := []string{"--modules", "sale", "--from", "stg", "--push", "--force", "--no-lint"}
	res, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if b, _ := os.ReadFile(serverReq); string(b) != "fastapi\nhttpx\n" {
		t.Errorf("server requirements = %q, want the working tree's", b)
	}
	if res.Requirements == nil || res.Requirements.Source != lockSourceWorktree || !res.Requirements.Synced {
		t.Errorf("requirements = %+v", res.Requirements)
	}
}

func TestRunDeployRequirementsNeedExplicitDest(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, _ := requirementsRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/__manifest__.py"), "{}")
	cfg := remote.cfg()
	cfg.PushPath = ""

	args := []string{"--modules", "sale", "--from", "stg", "--push", "--force", "--no-lint"}
	res, err := RunDeploy(ctx, DeployOpts{Cfg: cfg, Root: r.root, Args: args})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if res.Requirements != nil {
		t.Errorf("requirements = %+v, want none without an explicit destination", res.Requirements)
	}
	if _, err := os.Stat(filepath.Join(remote.dir, "addons/requirements.txt")); err == nil {
		t.Error("requirements.txt landed in an auto-detected addons directory")
	}
}

func TestRunDeployPlanStaleOnRequirements(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, _ := requirementsRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/__manifest__.py"), "{}")
	planPath := filepath.Join(t.TempDir(), "p.json")
	deploy := func(args ...string) (DeployResult, error) {
		return RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
	}

	if _, err := deploy("--modules", "sale", "--from", "stg", "--push", "--no-lint",
		"--dry-run", "--save-plan", planPath); err != nil {
		t.Fatalf("save: %v", err)
	}
	r.write("requirements.txt", "fastapi\nhttpx\npytest\n")
	res, err := deploy("--apply", planPath, "--force")
	if !errors.Is(err, ErrPlanStale) {
		t.Fatalf("apply: err = %v, want ErrPlanStale", err)
	}
	found := false
	for _, c := range res.PlanStale {
		found = found || c.What == "requirements"
	}
	if !found {
		t.Errorf("plan_stale = %+v, want a requirements change", res.PlanStale)
	}
	if _, err := os.Stat(filepath.Join(remote.dir, "addons/requirements.txt")); err == nil {
		t.Error("a stale plan wrote requirements.txt")
	}
}
