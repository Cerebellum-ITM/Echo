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
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

// lockTarget makes the fake server look like another db-push, started age ago,
// holds its operation lock.
func lockTarget(t *testing.T, remote *fakeRemote, age time.Duration) {
	t.Helper()
	b, _ := json.Marshal(OpLock{Command: "db-push", ClientHost: "laptop", Started: time.Now().UTC().Add(-age).Truncate(time.Second)})
	mustWrite(t, filepath.Join(remote.dir, opLockFile), string(b)+"\n")
	mustWrite(t, filepath.Join(remote.dir, lockDirName, ".gitignore"), "*\n")
}

func opLockExists(remote *fakeRemote) bool {
	_, err := os.Stat(filepath.Join(remote.dir, opLockDir))
	return err == nil
}

func (l *logRecorder) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !l.has(substr) {
		if time.Now().After(deadline) {
			t.Fatalf("no log line with %q", substr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEntryPointsRefuseBusyTarget(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, remote *fakeRemote, root string)
		run   func(ctx context.Context, remote *fakeRemote, root string) error
	}{
		{
			name: "deploy",
			run: func(ctx context.Context, remote *fakeRemote, root string) error {
				args := []string{"--modules", "sale", "--from", "stg", "--push", "--force", "--no-lint"}
				_, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: root, Args: args})
				return err
			},
		},
		{
			name: "deploy --rollback",
			setup: func(t *testing.T, remote *fakeRemote, root string) {
				entry := config.CheckpointEntry{Name: "code_x", Method: codeCheckpointMethod, CodeSnapshot: "code_x"}
				if err := config.AddCheckpoint(config.ProjectKey(root), config.DeployTargetKey("fakehost", remote.dir), entry); err != nil {
					t.Fatal(err)
				}
			},
			run: func(ctx context.Context, remote *fakeRemote, root string) error {
				_, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: root, Args: []string{"--rollback", "--from", "stg", "--force"}})
				return err
			},
		},
		{
			name: "sideload",
			setup: func(t *testing.T, remote *fakeRemote, root string) {
				writeModule(t, root, "extra", map[string]string{"__manifest__.py": "{'name': 'Extra', 'version': '18.0.1.0.0'}"})
			},
			run: func(ctx context.Context, remote *fakeRemote, root string) error {
				addon := filepath.Join(root, "extra")
				_, err := RunSideload(ctx, SideloadOpts{Cfg: remote.cfg(), Root: root, Args: []string{addon, "--from", "stg"}})
				return err
			},
		},
		{
			name: "checkpoint create",
			run: func(ctx context.Context, remote *fakeRemote, root string) error {
				_, err := RunCheckpoint(ctx, CheckpointOpts{Cfg: remote.cfg(), Root: root, Args: []string{"create", "--method", "dump", "--from", "stg"}})
				return err
			},
		},
		{
			name: "checkpoint rm",
			setup: func(t *testing.T, remote *fakeRemote, root string) {
				entry := config.CheckpointEntry{Name: "stg__ckpt_x", Method: "db", DB: "stg"}
				if err := config.AddCheckpoint(config.ProjectKey(root), config.DeployTargetKey("fakehost", remote.dir), entry); err != nil {
					t.Fatal(err)
				}
			},
			run: func(ctx context.Context, remote *fakeRemote, root string) error {
				_, err := RunCheckpoint(ctx, CheckpointOpts{Cfg: remote.cfg(), Root: root, Args: []string{"rm", "stg__ckpt_x", "--force", "--from", "stg"}})
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			remote := newFakeRemote(t)
			r, _ := incidentRepo(t)
			if tc.setup != nil {
				tc.setup(t, remote, r.root)
			}
			mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
			lockTarget(t, remote, time.Minute)
			before := treeFiles(t, remote.dir)
			ckpts := config.LoadCheckpoints(config.ProjectKey(r.root), config.DeployTargetKey("fakehost", remote.dir))

			err := tc.run(ctx, remote, r.root)
			var held errOpLockHeld
			if !errors.As(err, &held) || !strings.Contains(err.Error(), "target busy: db-push from laptop") {
				t.Fatalf("want target busy, got %v", err)
			}
			if after := treeFiles(t, remote.dir); !reflect.DeepEqual(after, before) {
				t.Errorf("a busy target was written:\n%v\nwant:\n%v", after, before)
			}
			for _, write := range []string{"'stop'", "'up' '-d'", "stop-after-init", "cat > "} {
				if strings.Contains(remote.composeLog(), write) {
					t.Errorf("compose write %q ran on a busy target", write)
				}
			}
			if got := config.LoadCheckpoints(config.ProjectKey(r.root), config.DeployTargetKey("fakehost", remote.dir)); !reflect.DeepEqual(got, ckpts) {
				t.Errorf("checkpoint store changed: %v", got)
			}
		})
	}
}

func TestRunDeployReleasesOpLock(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	args := []string{"--modules", "sale@" + fix, "--from", "stg", "--push", "--force", "--no-lint"}

	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if opLockExists(remote) {
		t.Error("the lock outlived a green deploy")
	}

	// A failed run rolls back inside the deploy: that nested restore must not
	// take the lock a second time, and the lock must still be released.
	t.Setenv("FAKE_FAIL_RUN", "1")
	before := strings.Count(remote.sshLog(), "if mkdir "+opLockDir)
	res, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
	if err == nil || !res.RolledBack {
		t.Fatalf("want a rolled-back failure, got %v (rolled back %v)", err, res.RolledBack)
	}
	if n := strings.Count(remote.sshLog(), "if mkdir "+opLockDir) - before; n != 1 {
		t.Errorf("a failed deploy took the lock %d times, want 1", n)
	}
	if opLockExists(remote) {
		t.Error("the lock outlived a failed deploy")
	}
}

func TestRunDeployStaleOpLock(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	lockTarget(t, remote, 7*time.Hour)
	args := []string{"--modules", "sale@" + fix, "--from", "stg", "--push", "--no-lint"}

	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args}); !errors.As(err, new(errOpLockHeld)) {
		t.Fatalf("without --force a stale lock still blocks, got %v", err)
	}
	var logs logRecorder
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: append(args, "--force"), Log: logs.log}); err != nil {
		t.Fatalf("--force over a stale lock: %v", err)
	}
	if !logs.has("WARNING lock taking over a stale operation lock lock=" + opLockDir + " command=db-push") {
		t.Errorf("no takeover warning naming the old holder: %v", logs.lines)
	}
	if opLockExists(remote) {
		t.Error("the taken-over lock was not released")
	}
}

func TestRunDeployDryRunWarnsBusyTarget(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	lockTarget(t, remote, time.Minute)
	var logs logRecorder
	args := []string{"--modules", "sale@" + fix, "--from", "stg", "--push", "--no-lint", "--dry-run"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args, Log: logs.log}); err != nil {
		t.Fatalf("a dry run must complete on a busy target: %v", err)
	}
	if !logs.has("WARNING lock target busy command=db-push host=laptop since=") {
		t.Errorf("no busy warning: %v", logs.lines)
	}
	if !opLockExists(remote) {
		t.Error("the dry run touched the lock")
	}
}

func TestRunWatchSkipsCycleOnBusyTarget(t *testing.T) {
	remote := newFakeRemote(t)
	r, _ := incidentRepo(t)
	r.commit("[IMP] sale: uncommitted edit")
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	lockTarget(t, remote, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs logRecorder
	done := make(chan error, 1)
	go func() {
		done <- RunWatch(ctx, WatchOpts{
			Cfg: remote.cfg(), Root: r.root, Log: logs.log,
			Args: []string{"main", "--from", "stg", "--no-logs", "--interval", "2"},
		})
	}()
	logs.waitFor(t, "watching branch")

	r.write("addons/sale/models.py", "watched")
	r.commit("[IMP] sale: watched")
	logs.waitFor(t, "target busy, cycle skipped command=db-push host=laptop")
	if got := treeFiles(t, remote.dir)["addons/sale/models.py"]; got == "watched" {
		t.Fatal("the cycle deployed through a held lock")
	}

	if err := os.RemoveAll(filepath.Join(remote.dir, opLockDir)); err != nil {
		t.Fatal(err)
	}
	logs.waitFor(t, "cycle ok")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("watch: %v", err)
	}
	if got := treeFiles(t, remote.dir)["addons/sale/models.py"]; got != "watched" {
		t.Errorf("the retried cycle shipped %q", got)
	}
	if opLockExists(remote) {
		t.Error("the cycle did not release the lock")
	}
}
