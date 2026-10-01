package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseDeployArgsPlanFlags(t *testing.T) {
	ok := [][]string{
		{"--modules", "sale", "--dry-run", "--save-plan", "p.json"},
		{"--auto", "--dry-run", "--save-plan=p.json"},
		{"--apply", "p.json"},
		{"--apply=p.json", "--force", "--json", "--dry-run", "--from", "stg", "--no-rollback-on-fail"},
	}
	for _, args := range ok {
		if _, err := parseDeployArgs(args); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	bad := [][]string{
		{"--modules", "sale", "--dry-run", "--save-plan"},
		{"--modules", "sale", "--save-plan", "--dry-run"},
		{"--modules", "sale", "--save-plan", "p.json"},
		{"--apply"},
		{"--apply="},
		{"--apply", "p.json", "--modules", "sale"},
		{"--apply", "p.json", "--no-checkpoint"},
		{"--apply", "p.json", "--dry-run", "--save-plan", "q.json"},
		{"--apply", "p.json", "--at=main"},
	}
	for _, args := range bad {
		if _, err := parseDeployArgs(args); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: want ErrUsage, got %v", args, err)
		}
	}
}

func TestWorktreeDigest(t *testing.T) {
	build := func(t *testing.T, files map[string]string) string {
		dir := t.TempDir()
		for rel, content := range files {
			mustWrite(t, filepath.Join(dir, rel), content)
		}
		return dir
	}
	digest := func(t *testing.T, dir string) string {
		t.Helper()
		d, err := worktreeDigest(dir)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	base := map[string]string{"__manifest__.py": "{}", "models/sale.py": "x", "data/views.xml": "<odoo/>"}
	want := digest(t, build(t, base))

	cases := []struct {
		name   string
		change func(t *testing.T, dir string)
		same   bool
	}{
		{"untouched copy", func(*testing.T, string) {}, true},
		{"compiled files and VCS are not shipped", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "models/__pycache__/sale.cpython-311.pyc"), "bytecode")
			mustWrite(t, filepath.Join(dir, "models/old.pyc"), "bytecode")
			mustWrite(t, filepath.Join(dir, ".git/HEAD"), "ref: main")
		}, true},
		{"content", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "models/sale.py"), "y")
		}, false},
		{"executable bit", func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "models/sale.py"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"renamed file", func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, "models/sale.py"), filepath.Join(dir, "models/order.py")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"empty directory", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "static"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"symlink", func(t *testing.T, dir string) {
			if err := os.Symlink("models/sale.py", filepath.Join(dir, "link.py")); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := build(t, base)
			c.change(t, dir)
			if got := digest(t, dir); (got == want) != c.same {
				t.Errorf("digest equal=%v, want %v", got == want, c.same)
			}
		})
	}

	t.Run("symlink target", func(t *testing.T) {
		a, b := build(t, base), build(t, base)
		if err := os.Symlink("models/sale.py", filepath.Join(a, "link.py")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("data/views.xml", filepath.Join(b, "link.py")); err != nil {
			t.Fatal(err)
		}
		if digest(t, a) == digest(t, b) {
			t.Error("two symlink targets gave one digest")
		}
	})
	t.Run("creation order", func(t *testing.T) {
		dir := t.TempDir()
		for _, rel := range []string{"models/sale.py", "data/views.xml", "__manifest__.py"} {
			mustWrite(t, filepath.Join(dir, rel), base[rel])
		}
		if digest(t, dir) != want {
			t.Error("the digest depends on the order files were created")
		}
	})
}

func TestDiffPlans(t *testing.T) {
	saved := DeployPlan{
		Target: planTarget{Name: "stg", SSHHost: "h", RemotePath: "/srv", DB: "db", Stage: "staging"},
		Run:    planRun{Push: true, Checkpoint: "db", Actions: true, Lint: true, DepCheck: true, Fetch: "auto"},
		Modules: []planModule{
			{Name: "sale", Action: "update", Source: lockSourceRef, Ref: "release/1.4", SHA: "99f2109aaaa", Tree: "t1"},
			{Name: "crm", Action: "install", Source: lockSourceWorktree, Digest: "sha256:aaaaaaaaaa"},
		},
		DeployActions: planActions{Names: []string{"build/post_push/remote"}, Digest: "sha256:1111111111"},
		Lock:          "sha256:3f2a9c1aaaa",
	}
	cases := []struct {
		name   string
		change func(p *DeployPlan)
		want   []PlanChange
	}{
		{"identical", func(*DeployPlan) {}, nil},
		{"ref moved", func(p *DeployPlan) { p.Modules[0].SHA = "a1c4e02bbbb" }, []PlanChange{
			{What: "ref", Module: "sale", Planned: "99f2109", Now: "a1c4e02", ref: "release/1.4"}}},
		{"disk changed", func(p *DeployPlan) { p.Modules[1].Digest = "sha256:bbbbbbbbbb" }, []PlanChange{
			{What: "disk", Module: "crm", Planned: "aaaaaaa", Now: "bbbbbbb"}}},
		{"installed meanwhile", func(p *DeployPlan) { p.Modules[1].Action = "update" }, []PlanChange{
			{What: "action", Module: "crm", Planned: "install", Now: "update"}}},
		{"source changed", func(p *DeployPlan) {
			p.Modules[0].Source, p.Modules[0].Ref, p.Modules[0].Digest = lockSourceWorktree, "", "sha256:cc"
		}, []PlanChange{{What: "source", Module: "sale", Planned: "ref", Now: "worktree"}}},
		{"module gone", func(p *DeployPlan) { p.Modules = p.Modules[:1] }, []PlanChange{
			{What: "module", Module: "crm", Planned: "install", Now: "none"}}},
		{"lock written", func(p *DeployPlan) { p.Lock = "sha256:b77d0e4cccc" }, []PlanChange{
			{What: "lock", Planned: "3f2a9c1", Now: "b77d0e4"}}},
		{"action edited", func(p *DeployPlan) { p.DeployActions.Digest = "sha256:2222222222" }, []PlanChange{
			{What: "actions", Planned: "1111111", Now: "2222222"}}},
		{"action added", func(p *DeployPlan) {
			p.DeployActions = planActions{Names: []string{"build/post_push/remote", "notify/post_deploy/local"}, Digest: "sha256:33"}
		}, []PlanChange{{What: "actions", Planned: "build/post_push/remote", Now: "build/post_push/remote,notify/post_deploy/local"}}},
		{"stage and git", func(p *DeployPlan) { p.Target.Stage = "prod"; p.Run.Git = true }, []PlanChange{
			{What: "stage", Planned: "staging", Now: "prod"}, {What: "run.git", Planned: "false", Now: "true"}}},
		{"new finding", func(p *DeployPlan) {
			p.Dependencies = []planDependency{{Module: "sale", Symbol: "_promo", Kind: "method", UsedBy: "crm"}}
		}, []PlanChange{{What: "dependencies", Planned: "none", Now: "sale._promo>crm"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fresh := saved
			fresh.Modules = append([]planModule(nil), saved.Modules...)
			c.change(&fresh)
			if got := diffPlans(saved, fresh); !reflect.DeepEqual(got, c.want) {
				t.Errorf("diff = %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestPlanDeployArgs(t *testing.T) {
	plan := DeployPlan{
		Target:  planTarget{Name: "stg"},
		Run:     planRun{Push: true, Checkpoint: "off", Test: false, I18nOverwrite: true, Git: false, Actions: true, Lint: false, DepCheck: true, Fetch: "off"},
		Commits: []string{"abc"},
		Modules: []planModule{
			{Name: "sale", Source: lockSourceRef, Ref: "release/1.4"},
			{Name: "stock", Source: lockSourceWorktree},
			{Name: "crm", Source: lockSourceCommit},
		},
	}
	fls := false
	got, err := parseDeployArgs(planDeployArgs(plan, deployArgs{force: true, dryRun: true, rollbackOnFail: &fls}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.commits, []string{"abc"}) || !reflect.DeepEqual(got.modules, []string{"sale", "stock"}) ||
		got.moduleRefs["sale"] != "release/1.4" || got.from != "stg" {
		t.Errorf("selection = %+v", got)
	}
	if !got.push || !got.noCheckpoint || !got.noTest || !got.i18n || !got.noGit || got.noActions || !got.noLint ||
		got.noDepCheck || !got.noFetch || !got.force || !got.dryRun || got.rollbackOnFail == nil || *got.rollbackOnFail {
		t.Errorf("run pins = %+v", got)
	}
}

// planRepo is the incident repo plus a branch `rel` at the fix and a module
// that is not installed on the server.
func planRepo(t *testing.T) (*sourceRepo, string) {
	r, fix := incidentRepo(t)
	r.git("branch", "rel", fix)
	r.write("addons/crm/__manifest__.py", "{'name': 'crm', 'version': '18.0.1.0'}")
	return r, fix
}

func TestRunDeploySavedPlan(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := planRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	mustWrite(t, filepath.Join(remote.dir, "addons/stock/models.py"), "running on the server")
	profile := filepath.Join(os.Getenv("HOME"), ".config/echo/projects", config.ProjectKey(remote.dir)+".toml")
	appendProfile := func(text string) {
		b, _ := os.ReadFile(profile)
		mustWrite(t, profile, string(b)+text)
	}
	planPath := filepath.Join(t.TempDir(), "p.json")
	var logs []string
	deploy := func(args ...string) (DeployResult, error) {
		logs = nil
		return RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args,
			Log: func(level, sub, msg, db string, fields ...[2]string) {
				line := level + " " + sub + " " + msg
				for _, f := range fields {
					line += " " + f[0] + "=" + f[1]
				}
				logs = append(logs, line)
			}})
	}
	save := func() DeployPlan {
		t.Helper()
		if _, err := deploy("--modules", "sale@rel,stock,crm", "--from", "stg", "--push", "--no-lint",
			"--dry-run", "--save-plan", planPath); err != nil {
			t.Fatalf("save: %v", err)
		}
		var plan DeployPlan
		b, _ := os.ReadFile(planPath)
		if err := json.Unmarshal(b, &plan); err != nil {
			t.Fatal(err)
		}
		return plan
	}

	before, sshBefore := treeFiles(t, remote.dir), len(remote.sshLog())
	plan := save()
	if info, err := os.Stat(planPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plan file: %v %v", info, err)
	}
	if !reflect.DeepEqual(treeFiles(t, remote.dir), before) || remote.composeLog() != "" {
		t.Fatal("--save-plan changed the server")
	}
	for _, write := range []string{"lock.json.tmp", "backups/code"} {
		if strings.Contains(remote.sshLog()[sshBefore:], write) {
			t.Errorf("--save-plan sent a write to the server: %q", write)
		}
	}
	mods := map[string]planModule{}
	for _, m := range plan.Modules {
		mods[m.Name] = m
	}
	if m := mods["sale"]; m.Source != lockSourceRef || m.Ref != "rel" || m.SHA != fix || m.Tree == "" || m.Action != "update" {
		t.Errorf("sale = %+v", m)
	}
	if m := mods["stock"]; m.Source != lockSourceWorktree || !strings.HasPrefix(m.Digest, "sha256:") || m.SHA != "" {
		t.Errorf("stock = %+v", m)
	}
	if m := mods["crm"]; m.Action != "install" {
		t.Errorf("crm = %+v", m)
	}
	if plan.Lock != "absent" || plan.Run.Checkpoint != "off" || !plan.Run.Push || plan.Root != r.root || plan.Target.Name != "stg" {
		t.Errorf("plan = %+v", plan)
	}

	appendProfile("[checkpoint]\nmode = \"on\"\n")
	res, err := deploy("--apply", planPath, "--force")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Checkpoint != nil {
		t.Error("the server's checkpoint mode overrode the planned decision")
	}
	lock, _ := readDeployLock(ctx, remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}, nil)
	if e := lock.Modules["sale"]; e.SHA != fix || e.Tree != mods["sale"].Tree || !e.Verified {
		t.Errorf("lock sale = %+v", e)
	}
	if e := lock.Modules["stock"]; e.Source != lockSourceWorktree {
		t.Errorf("lock stock = %+v", e)
	}

	stale := []struct {
		what   string
		change func()
	}{
		{"ref", func() {
			moved := r.git("commit-tree", fix+"^{tree}", "-p", fix, "-m", "moved")
			r.git("update-ref", "refs/heads/rel", moved)
		}},
		{"disk", func() { r.write("addons/stock/models.py", "edited after the review") }},
		{"lock", func() { mustWrite(t, filepath.Join(remote.dir, ".echo/lock.json"), `{"schema":1,"modules":{}}`) }},
		{"action", func() { t.Setenv("FAKE_MODULE_STATES", "crm|installed|18.0.1.0") }},
		{"actions", func() {
			appendProfile("[[deploy.actions]]\nname = \"build\"\nphase = \"post_push\"\nwhere = \"remote\"\nrun = \"true\"\n")
		}},
	}
	for _, c := range stale {
		save()
		c.change()
		before, compose, sshBefore := treeFiles(t, remote.dir), remote.composeLog(), len(remote.sshLog())
		res, err := deploy("--apply", planPath, "--force")
		if !errors.Is(err, ErrPlanStale) {
			t.Fatalf("%s: want ErrPlanStale, got %v", c.what, err)
		}
		found := false
		for _, ch := range res.PlanStale {
			found = found || ch.What == c.what
		}
		if !found {
			t.Errorf("%s: plan_stale = %+v", c.what, res.PlanStale)
		}
		if !hasLog(logs, "ERROR plan changed what="+c.what) {
			t.Errorf("%s: no changed line in %v", c.what, logs)
		}
		if !reflect.DeepEqual(treeFiles(t, remote.dir), before) || remote.composeLog() != compose {
			t.Errorf("%s: a refused apply changed the server", c.what)
		}
		if strings.Contains(remote.sshLog()[sshBefore:], "rsync") {
			t.Errorf("%s: a refused apply ran rsync", c.what)
		}
	}

	save()
	if _, err := deploy("--apply", planPath, "--dry-run"); err != nil {
		t.Fatalf("apply --dry-run: %v", err)
	}
	if !hasLog(logs, "INFO plan plan matches age=") || !hasLog(logs, "dry-run — nothing executed") {
		t.Errorf("apply --dry-run logs: %v", logs)
	}
}

func TestRunDeployApplyUsageBeforeSSH(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, _ := planRepo(t)
	dir := t.TempDir()
	write := func(name string, plan any) string {
		path := filepath.Join(dir, name)
		b, _ := json.Marshal(plan)
		mustWrite(t, path, string(b))
		return path
	}
	good := DeployPlan{Schema: planSchema, Root: r.root, Target: planTarget{Name: "stg"}, Run: planRun{Checkpoint: "off"}}
	other := good
	other.Root = t.TempDir()
	schema2 := good
	schema2.Schema = 2
	notJSON := filepath.Join(dir, "plan.txt")
	mustWrite(t, notJSON, "modules: sale")

	for _, args := range [][]string{
		{"--apply", notJSON},
		{"--apply", write("schema2.json", schema2)},
		{"--apply", write("other.json", other)},
		{"--apply", write("good.json", good), "--from", "prod"},
		{"--apply", write("good.json", good), "--modules", "sale"},
		{"--apply", filepath.Join(dir, "missing.json")},
	} {
		_, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
		if !errors.Is(err, ErrUsage) {
			t.Errorf("%v: want ErrUsage, got %v", args, err)
		}
	}
	if log := remote.sshLog(); log != "" {
		t.Errorf("a refused plan reached the server:\n%s", log)
	}
}

func TestRunDeployApplyOnProdNeedsForce(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, _ := planRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/stock/models.py"), "running on the server")
	mustWrite(t, filepath.Join(os.Getenv("HOME"), ".config/echo/projects", config.ProjectKey(remote.dir)+".toml"),
		"stage = \"prod\"\ndb_name = \"prd\"\nodoo_version = \"18\"\n")
	orig := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = orig })

	planPath := filepath.Join(t.TempDir(), "p.json")
	run := func(args ...string) error {
		_, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
		return err
	}
	if err := run("--modules", "stock", "--from", "stg", "--push", "--no-lint", "--dry-run", "--save-plan", planPath); err != nil {
		t.Fatalf("save on prod: %v", err)
	}
	if err := run("--apply", planPath); !errors.Is(err, ErrNonInteractive) {
		t.Fatalf("apply on prod without a TTY: want ErrNonInteractive, got %v", err)
	}
	if remote.composeLog() != "" {
		t.Error("the prod gate let the apply through")
	}
}

func hasLog(logs []string, part string) bool {
	for _, l := range logs {
		if strings.Contains(l, part) {
			return true
		}
	}
	return false
}
