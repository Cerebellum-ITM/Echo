package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestCheckSnapshotPaths(t *testing.T) {
	if err := checkSnapshotPaths([]string{"/srv/odoo/addons/sale", "/srv/odoo/.echo/lock.json"}); err != nil {
		t.Errorf("safe paths refused: %v", err)
	}
	for _, bad := range []string{"/", "/srv", "addons/sale", "/srv/odoo/../etc", "/srv/odoo/addons/"} {
		if err := checkSnapshotPaths([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// localCkptSSH runs every checkpoint command locally, standing in for a
// server whose filesystem is this machine's.
func localCkptSSH(t *testing.T) {
	t.Helper()
	orig := ckptRunSSH
	ckptRunSSH = func(ctx context.Context, _ string, remoteCmd string, stdin []byte) ([]byte, error) {
		c := exec.CommandContext(ctx, "sh", "-c", remoteCmd)
		if stdin != nil {
			c.Stdin = strings.NewReader(string(stdin))
		}
		return c.Output()
	}
	t.Cleanup(func() { ckptRunSSH = orig })
}

func TestCodeSnapshotRoundTrip(t *testing.T) {
	localCkptSSH(t)
	ctx := context.Background()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := filepath.Join(base, "srv")
	mustWrite(t, filepath.Join(srv, "addons/sale/models.py"), "before")
	mustWrite(t, filepath.Join(srv, "addons/sale/views/form.xml"), "<odoo/>")
	mustWrite(t, filepath.Join(srv, ".echo/lock.json"), `{"schema":1}`)
	mustWrite(t, filepath.Join(srv, "addons/stock/models.py"), "not in the snapshot")
	rsc := remoteShellContext{sshHost: "local", remotePath: srv}
	dests := map[string]string{
		"sale":  filepath.Join(srv, "addons/sale"),
		"fresh": filepath.Join(srv, "addons/fresh"),
	}

	snap, err := createCodeSnapshot(ctx, rsc, dests, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !reflect.DeepEqual(snap.Absent, []string{filepath.Join(srv, "addons/fresh")}) {
		t.Errorf("absent = %v", snap.Absent)
	}
	if got, _ := os.ReadFile(filepath.Join(srv, codeSnapshotDir, ".gitignore")); string(got) != "*\n" {
		t.Errorf("snapshot dir .gitignore = %q", got)
	}
	before := treeFiles(t, srv)

	mustWrite(t, filepath.Join(srv, "addons/sale/models.py"), "after")
	mustWrite(t, filepath.Join(srv, "addons/sale/added.py"), "new")
	mustWrite(t, filepath.Join(srv, "addons/fresh/__manifest__.py"), "{}")
	mustWrite(t, filepath.Join(srv, ".echo/lock.json"), `{"schema":1,"modules":{}}`)
	mustWrite(t, filepath.Join(srv, "addons/stock/models.py"), "changed outside the snapshot")

	read, err := readCodeSnapshot(ctx, rsc, snap.Name)
	if err != nil || !reflect.DeepEqual(read, snap) {
		t.Fatalf("sidecar = %+v, %v", read, err)
	}
	if err := restoreCodeSnapshot(ctx, rsc, read, nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after := treeFiles(t, srv)
	if after["addons/stock/models.py"] != "changed outside the snapshot" {
		t.Error("restore touched a module outside the snapshot")
	}
	delete(after, "addons/stock/models.py")
	delete(before, "addons/stock/models.py")
	if !reflect.DeepEqual(after, before) {
		t.Errorf("after restore:\n%v\nwant:\n%v", after, before)
	}

	if err := destroyCodeSnapshot(ctx, rsc, snap.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(srv, codeSnapshotTarball(snap.Name))); !os.IsNotExist(err) {
		t.Errorf("tarball survived destroy: %v", err)
	}
}

func TestDeployFailureRestorePoint(t *testing.T) {
	ckpt := &config.CheckpointEntry{Name: "db_ckpt", Method: "dump"}
	snap := &codeSnapshot{Name: "code_x"}

	cases := []struct {
		name             string
		f                deployFailure
		withDB, withCode bool
		want             config.CheckpointEntry
		ok               bool
	}{
		{"db and code", deployFailure{checkpoint: ckpt, snapshot: snap, codeSHA: "abc1234567"}, true, true,
			config.CheckpointEntry{Name: "db_ckpt", Method: "dump", CodeSHA: "abc1234567", CodeSnapshot: "code_x"}, true},
		{"db only, code restored", deployFailure{checkpoint: ckpt, snapshot: snap}, true, false,
			config.CheckpointEntry{Name: "db_ckpt", Method: "dump"}, true},
		{"code only", deployFailure{snapshot: snap}, false, true,
			config.CheckpointEntry{Name: "code_x", Method: codeCheckpointMethod, CodeSnapshot: "code_x"}, true},
		{"nothing left", deployFailure{checkpoint: ckpt, snapshot: snap}, false, false, config.CheckpointEntry{}, false},
	}
	for _, tc := range cases {
		got, ok := tc.f.restorePoint(tc.withDB, tc.withCode)
		got.CreatedAt = tc.want.CreatedAt
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v %v, want %+v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func codeSnapshotFiles(t *testing.T, srv string) []string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(srv, codeSnapshotDir, "*.tar.gz"))
	return matches
}

func TestRunDeployFailureRestoresCode(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	r.write("addons/fresh/__manifest__.py", "{'name': 'fresh'}")
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/legacy.py"), "still here")
	before := treeFiles(t, remote.dir)
	t.Setenv("FAKE_FAIL_RUN", "1")

	args := []string{"--modules", "sale@" + fix + ",fresh", "--from", "stg", "--push", "--force", "--no-lint"}
	res, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
	if err == nil || !strings.Contains(err.Error(), "odoo run failed") {
		t.Fatalf("want the run failure, got %v", err)
	}
	if !res.RolledBack {
		t.Error("result not marked rolled back")
	}
	after := treeFiles(t, remote.dir)
	// The self-ignore files of .echo/ and the snapshot dir stay by design.
	for k := range after {
		if filepath.Base(k) == ".gitignore" {
			delete(after, k)
		}
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("server after rollback:\n%v\nwant:\n%v", after, before)
	}
	if left := codeSnapshotFiles(t, remote.dir); len(left) != 0 {
		t.Errorf("snapshot not cleaned after a restore: %v", left)
	}
}

func TestRunDeployPostPushFailureRebuilds(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	before := treeFiles(t, remote.dir)

	var builds []string
	orig := actionRunRemote
	actionRunRemote = func(_ context.Context, _ remoteShellContext, a config.DeployAction, _ []string, _ func(string)) error {
		b, _ := os.ReadFile(filepath.Join(remote.dir, "addons/sale/models.py"))
		builds = append(builds, string(b))
		if len(builds) == 1 {
			return os.ErrPermission
		}
		return nil
	}
	t.Cleanup(func() { actionRunRemote = orig })

	cfg := remote.cfg()
	cfg.DeployActions = []config.DeployAction{{Name: "build", Phase: config.PhasePostPush, Where: config.WhereRemote, Run: "make image"}}
	args := []string{"--modules", "sale@" + fix, "--from", "stg", "--push", "--force", "--no-lint"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: cfg, Root: r.root, Args: args}); err == nil {
		t.Fatal("want the build failure")
	}
	if got := treeFiles(t, remote.dir)["addons/sale/models.py"]; got != before["addons/sale/models.py"] {
		t.Errorf("code after rollback = %q", got)
	}
	want := []string{"v1 + fix", "running on the server"}
	if !reflect.DeepEqual(builds, want) {
		t.Errorf("builds saw %v, want the failed build then a rebuild from the restored code", builds)
	}
	if strings.Contains(remote.composeLog(), "stop") {
		t.Errorf("the app was stopped although the run never touched it:\n%s", remote.composeLog())
	}
}

func TestRunDeployNoRollbackTakesNoSnapshot(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	t.Setenv("FAKE_FAIL_RUN", "1")

	args := []string{"--modules", "sale@" + fix, "--from", "stg", "--push", "--force", "--no-lint", "--no-rollback-on-fail"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args}); err == nil {
		t.Fatal("want the run failure")
	}
	if _, err := os.Stat(filepath.Join(remote.dir, codeSnapshotDir)); !os.IsNotExist(err) {
		t.Errorf("a snapshot was taken under --no-rollback-on-fail: %v", err)
	}
	if got := treeFiles(t, remote.dir)["addons/sale/models.py"]; got != "v1 + fix" {
		t.Errorf("the failed code should stay for inspection, got %q", got)
	}
}

func TestRunDeployRollbackRestoresCodeEntry(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, _ := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "good")
	rsc := remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}
	snap, err := createCodeSnapshot(ctx, rsc, map[string]string{"sale": filepath.Join(remote.dir, "addons/sale")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := config.CheckpointEntry{Name: snap.Name, Method: codeCheckpointMethod, CodeSnapshot: snap.Name}
	projectKey, targetKey := config.ProjectKey(r.root), config.DeployTargetKey("fakehost", remote.dir)
	if err := config.AddCheckpoint(projectKey, targetKey, entry); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "broken")

	args := []string{"--rollback", "--from", "stg", "--force"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := treeFiles(t, remote.dir)["addons/sale/models.py"]; got != "good" {
		t.Errorf("code after --rollback = %q", got)
	}
	if strings.Contains(remote.composeLog(), "psql") {
		t.Error("a code entry touched the database")
	}
	if len(config.LoadCheckpoints(projectKey, targetKey)) != 1 {
		t.Error("the code entry should stay restorable")
	}
}

func TestRunDeployFailureRestoresBranchAndOverlay(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	base := r.git("rev-list", "--max-parents=0", "HEAD")
	refactor := r.git("rev-parse", "HEAD")
	if out, err := exec.Command("git", "clone", "-q", r.root, remote.dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	srv := &sourceRepo{t: t, root: remote.dir}
	srv.git("reset", "-q", "--hard", base)
	cfg := remote.cfg()
	cfg.ConnectTargets[0].GitDeploy = true
	deploy := func(args ...string) error {
		args = append(args, "--from", "stg", "--push", "--force", "--no-lint")
		_, err := RunDeploy(ctx, DeployOpts{Cfg: cfg, Root: r.root, Args: args})
		return err
	}

	if err := deploy("--modules", "sale@"+fix); err != nil {
		t.Fatal(err)
	}
	pinned := treeFiles(t, filepath.Join(remote.dir, "addons/sale"))
	status := srv.git("status", "--porcelain")

	t.Setenv("FAKE_FAIL_RUN", "1")
	if err := deploy("--commits", refactor); err == nil {
		t.Fatal("want the run failure")
	}
	if head := srv.git("rev-parse", "HEAD"); head != base {
		t.Errorf("branch at %s after rollback, want %s", head, base)
	}
	if got := treeFiles(t, filepath.Join(remote.dir, "addons/sale")); !reflect.DeepEqual(got, pinned) {
		t.Errorf("overlay after rollback = %v, want the pinned tree %v", got, pinned)
	}
	if got := srv.git("status", "--porcelain"); got != status {
		t.Errorf("status after rollback:\n%s\nwant:\n%s", got, status)
	}
	lock, _ := readDeployLock(ctx, remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}, nil)
	if e := lock.Modules["sale"]; e.Source != lockSourceRef || e.SHA != fix {
		t.Errorf("lock after rollback = %+v, want the pin back", e)
	}
}
