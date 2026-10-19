package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseDBPushArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
		check   func(dbPushArgs) bool
	}{
		{"to", []string{"a.dump", "--to", "dev"}, "", func(p dbPushArgs) bool { return p.to == "dev" && p.dump == "a.dump" }},
		{"remote", []string{"a.dump", "--remote"}, "", func(p dbPushArgs) bool { return p.remote }},
		{"both targets", []string{"a.dump", "--to", "dev", "--remote"}, "not both", nil},
		{"no target", []string{"a.dump"}, "--to <target> or --remote", nil},
		{"no dump", []string{"--to", "dev"}, "pass the dump", nil},
		{"two dumps", []string{"a.dump", "b.dump", "--to", "dev"}, "takes one dump", nil},
		{"unknown flag", []string{"a.dump", "--to", "dev", "--nope"}, "unknown flag --nope", nil},
		{"bare filestore", []string{"b/a.dump", "--to", "dev", "--filestore"}, "", func(p dbPushArgs) bool { return p.tar == "b/a.filestore.tar" }},
		{"filestore value", []string{"a.dump", "--filestore", "x.tar", "--to", "dev"}, "", func(p dbPushArgs) bool { return p.tar == "x.tar" && p.to == "dev" }},
		{"filestore equals", []string{"a.dump", "--filestore=y.tar", "--to=dev"}, "", func(p dbPushArgs) bool { return p.tar == "y.tar" && p.to == "dev" }},
		{"invalid as", []string{"a.dump", "--to", "dev", "--as", "My-DB"}, "not a valid database name", nil},
		{"to without value", []string{"a.dump", "--to", "--json"}, "--to needs a value", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseDBPushArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want usage error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !tc.check(p) {
				t.Fatalf("parse(%v) = %+v, %v", tc.args, p, err)
			}
		})
	}
}

func TestCheckDBPushFiles(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.dump")
	os.WriteFile(bad, []byte("plain sql"), 0o644)
	if _, _, err := checkDBPushFiles(dbPushArgs{dump: bad}); err == nil || !strings.Contains(err.Error(), "not a pg_dump custom-format") {
		t.Errorf("non-PGDMP file: %v", err)
	}
	good := filepath.Join(dir, "good.dump")
	os.WriteFile(good, []byte("PGDMP-data"), 0o644)
	_, _, err := checkDBPushFiles(dbPushArgs{dump: good, filestore: true, tar: filepath.Join(dir, "good.filestore.tar")})
	if err == nil || !strings.Contains(err.Error(), "good.filestore.tar") {
		t.Errorf("missing tar should be named: %v", err)
	}
}

// dbPushFake scripts every SSH call db-push makes and records them in order.
type dbPushFake struct {
	calls     []string
	sizes     map[string]int
	failOn    string
	lockReply string
}

func (f *dbPushFake) install(t *testing.T) {
	origSSH, origStream, origUpload, origTTY := ckptRunSSH, ckptRunSSHStream, dbPushUpload, stdinIsTTY
	t.Cleanup(func() {
		ckptRunSSH, ckptRunSSHStream, dbPushUpload, stdinIsTTY = origSSH, origStream, origUpload, origTTY
	})
	stdinIsTTY = func() bool { return false }
	if f.lockReply == "" {
		f.lockReply = "acquired"
	}
	ckptRunSSH = func(ctx context.Context, host, cmd string, stdin []byte) ([]byte, error) {
		f.calls = append(f.calls, cmd)
		if f.failOn != "" && strings.Contains(cmd, f.failOn) {
			return nil, errors.New("scripted failure")
		}
		switch {
		case strings.Contains(cmd, "SELECT 1"):
			return []byte("1\n"), nil
		case strings.Contains(cmd, "SELECT datname"):
			return []byte("mydb\nother\n"), nil
		case strings.Contains(cmd, "pg_database_size"):
			return []byte("1000\n"), nil
		case strings.Contains(cmd, "server_version_num"):
			return []byte("150004\n"), nil
		case strings.Contains(cmd, "df -Pk"):
			return []byte("Filesystem 1K-blocks Used Available Use% Mounted\n/dev/sda 100 0 1000000 0% /\n"), nil
		case strings.Contains(cmd, "mkdir "+opLockDir):
			return []byte(f.lockReply + "\n"), nil
		case strings.Contains(cmd, "if [ -d "+opLockDir):
			return []byte("free\n"), nil
		case strings.Contains(cmd, "wc -c"):
			for name, n := range f.sizes {
				if strings.Contains(cmd, name) {
					return []byte(strconv.Itoa(n) + "\n"), nil
				}
			}
		case strings.Contains(cmd, "ir_module_module"):
			return []byte("base|installed|19.0.1.3|Odoo S.A.\nsale_custom|installed|19.0.1.0|Acme\nmail|uninstalled||Odoo S.A.\n"), nil
		}
		return []byte(""), nil
	}
	ckptRunSSHStream = func(ctx context.Context, host, cmd string, stdin []byte, onLine func(string)) error {
		f.calls = append(f.calls, cmd)
		if f.failOn != "" && strings.Contains(cmd, f.failOn) {
			return errors.New("scripted failure")
		}
		return nil
	}
	dbPushUpload = func(ctx context.Context, host, cmd string, stdin io.Reader, onLine func(string)) error {
		io.Copy(io.Discard, stdin)
		f.calls = append(f.calls, "UPLOAD "+cmd)
		return nil
	}
}

func (f *dbPushFake) index(sub string) int {
	for i, c := range f.calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func dbPushFixture(t *testing.T) (dump string, sizes map[string]int) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	dump = filepath.Join(dir, "stg.dump")
	tar := filepath.Join(dir, "stg.filestore.tar")
	os.WriteFile(dump, []byte("PGDMP-0123456789"), 0o644)
	os.WriteFile(tar, []byte("tar-bytes"), 0o644)
	return dump, map[string]int{"stg.dump": 16, "stg.filestore.tar": 9}
}

func runDBPushTest(t *testing.T, rsc remoteShellContext, args ...string) (DBPushResult, error) {
	t.Helper()
	p, err := parseDBPushArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	dumpBytes, tarBytes, err := checkDBPushFiles(p)
	if err != nil {
		t.Fatal(err)
	}
	res := DBPushResult{Dump: filepath.Base(p.dump), DumpBytes: dumpBytes, Steps: []DBPushStep{}, Modules: []DBPushModule{}}
	if p.filestore {
		res.Filestore, res.FilestoreBytes = filepath.Base(p.tar), tarBytes
	}
	opts := DBPushOpts{Cfg: &config.Config{}, Root: t.TempDir()}
	return runDBPushFlow(context.Background(), opts, p, rsc, res)
}

func TestDBPushFlowOrder(t *testing.T) {
	dump, sizes := dbPushFixture(t)
	f := &dbPushFake{sizes: sizes}
	f.install(t)
	res, err := runDBPushTest(t, testRSC(), dump, "--to", "staging", "--filestore", "--neutralize")
	if err != nil {
		t.Fatalf("db-push: %v\ncalls: %v", err, f.calls)
	}
	order := []string{
		"SELECT 1", "mkdir " + opLockDir, "UPLOAD", "'stop' 'odoo'", `TEMPLATE "mydb"`,
		`DROP DATABASE IF EXISTS "mydb"`, "pg_restore", "pre-push", "'neutralize'", "--stop-after-init",
		"'up' '-d' 'odoo'", "ir_module_module", "rm -rf 'backups/db-push-", "rm -rf " + opLockDir,
	}
	last := -1
	for _, step := range order {
		i := f.index(step)
		if i < 0 || i < last {
			t.Fatalf("%q out of order (at %d, previous %d)\ncalls: %v", step, i, last, f.calls)
		}
		last = i
	}
	if !res.OK || !res.Neutralized || !strings.Contains(res.Checkpoint, "mydb__ckpt_") {
		t.Errorf("result = %+v", res)
	}
	if len(res.Modules) != 3 {
		t.Errorf("modules = %+v", res.Modules)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "null") {
		t.Errorf("JSON has a null: %s", b)
	}
}

func TestDBPushFailureAfterRestoreLeavesOdooStopped(t *testing.T) {
	dump, sizes := dbPushFixture(t)
	f := &dbPushFake{sizes: sizes, failOn: "pg_restore"}
	f.install(t)
	res, err := runDBPushTest(t, testRSC(), dump, "--to", "staging")
	if err == nil || res.OK {
		t.Fatalf("expected failure, got %+v", res)
	}
	if f.index("'up' '-d'") >= 0 {
		t.Errorf("Odoo must stay stopped after a failed restore: %v", f.calls)
	}
	if f.index("rm -rf 'backups/db-push-") < 0 || f.index("rm -rf "+opLockDir) < 0 {
		t.Errorf("cleanup must run on failure: %v", f.calls)
	}
}

func TestDBPushCheckpointFailureRestartsOdoo(t *testing.T) {
	dump, sizes := dbPushFixture(t)
	f := &dbPushFake{sizes: sizes, failOn: "CREATE DATABASE \"mydb__ckpt"}
	f.install(t)
	if _, err := runDBPushTest(t, testRSC(), dump, "--to", "staging"); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	if f.index("'up' '-d'") < 0 || f.index("DROP DATABASE IF EXISTS \"mydb\"") >= 0 {
		t.Errorf("a failed checkpoint must restart Odoo and touch nothing: %v", f.calls)
	}
}

func TestDBPushDryRunOnlyReads(t *testing.T) {
	dump, sizes := dbPushFixture(t)
	f := &dbPushFake{sizes: sizes}
	f.install(t)
	res, err := runDBPushTest(t, testRSC(), dump, "--to", "staging", "--filestore", "--dry-run")
	if err != nil || !res.DryRun || !res.OK {
		t.Fatalf("dry run: %+v, %v", res, err)
	}
	for _, c := range f.calls {
		for _, write := range []string{"mkdir", "cat >", " mv ", "rm -", "UPLOAD", "'run'", "'stop'", "'up'", "DROP", "CREATE"} {
			if strings.Contains(c, write) {
				t.Errorf("dry run issued a write (%s): %s", write, c)
			}
		}
	}
}

func TestDBPushProdGate(t *testing.T) {
	dump, sizes := dbPushFixture(t)
	f := &dbPushFake{sizes: sizes}
	f.install(t)
	prod := testRSC()
	prod.target.stage = "prod"
	if _, err := runDBPushTest(t, prod, dump, "--to", "staging"); !errors.Is(err, ErrUsage) || len(f.calls) != 0 {
		t.Fatalf("prod without --force: err=%v calls=%v", err, f.calls)
	}
	if _, err := runDBPushTest(t, prod, dump, "--to", "staging", "--force"); err != nil {
		t.Fatalf("prod with --force and no TTY should run: %v", err)
	}
}

func TestDBPushRefusesReverb(t *testing.T) {
	dump, sizes := dbPushFixture(t)
	f := &dbPushFake{sizes: sizes}
	f.install(t)
	rsc := testRSC()
	rsc.prof.Reverb = &config.ReverbMarker{}
	if _, err := runDBPushTest(t, rsc, dump, "--to", "staging"); err == nil || !strings.Contains(err.Error(), "Reverb") {
		t.Fatalf("err = %v", err)
	}
}

func TestAcquireOpLock(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	origNow := opLockNow
	t.Cleanup(func() { opLockNow = origNow })
	opLockNow = func() time.Time { return now }

	holder := func(age time.Duration) string {
		b, _ := json.Marshal(OpLock{Command: "db-push", ClientHost: "laptop", Started: now.Add(-age)})
		return string(b) + "\n\nheld\n"
	}
	cases := []struct {
		name     string
		age      time.Duration
		force    bool
		wantHeld bool
	}{
		{"young lock", time.Hour, true, true},
		{"stale lock without force", 7 * time.Hour, false, true},
		{"stale lock with force", 7 * time.Hour, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempts, released := 0, false
			orig := ckptRunSSH
			t.Cleanup(func() { ckptRunSSH = orig })
			ckptRunSSH = func(ctx context.Context, host, cmd string, stdin []byte) ([]byte, error) {
				switch {
				case strings.Contains(cmd, "rm -rf "+opLockDir):
					released = true
					return nil, nil
				case attempts == 0:
					attempts++
					return []byte(holder(tc.age)), nil
				}
				return []byte("acquired\n"), nil
			}
			_, err := acquireOpLock(context.Background(), testRSC(), "db-push", tc.force, nil)
			var held errOpLockHeld
			if gotHeld := errors.As(err, &held); gotHeld != tc.wantHeld {
				t.Fatalf("err = %v, want held=%v", err, tc.wantHeld)
			}
			if tc.wantHeld && !strings.Contains(err.Error(), "db-push from laptop") {
				t.Errorf("message should name the holder: %v", err)
			}
			if released == tc.wantHeld {
				t.Errorf("released = %v", released)
			}
		})
	}
}
