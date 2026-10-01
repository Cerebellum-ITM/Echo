package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

const doctorTestPassword = "s3cret-pw"

// doctorRemote is newFakeRemote plus a doctor-aware ssh in front of it: every
// call is logged, the DB batch gets canned psql/df answers (sizes from
// FAKE_DB_SIZE and FAKE_DATA_FREE_KIB), FAKE_SSH_DOWN makes the host
// unreachable, and everything else runs locally against the temp directory.
type doctorRemote struct {
	*fakeRemote
	home    string
	callLog string
}

func newDoctorRemote(t *testing.T) *doctorRemote {
	t.Helper()
	f := newFakeRemote(t)
	home := os.Getenv("HOME")
	d := &doctorRemote{fakeRemote: f, home: home, callLog: filepath.Join(home, "ssh-calls.log")}
	d.writeProfile(t, f.dir, "stage = \"dev\"\n")
	mustWrite(t, filepath.Join(f.dir, ".env"), "POSTGRES_USER=odoo\nPOSTGRES_PASSWORD="+doctorTestPassword+"\n")
	if err := os.MkdirAll(filepath.Join(f.dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "ssh"), `#!/bin/sh
while [ "$1" = "-o" ]; do shift 2; done
shift
cmd="$*"
printf '%s\n----\n' "$cmd" >> "`+d.callLog+`"
if [ -n "$FAKE_SSH_DOWN" ]; then
  echo "ssh: connect to host fakehost port 22: Connection refused" >&2
  exit 255
fi
case "$cmd" in
  *pg_database_size*)
    printf '%s\n@@echo-doctor size 0\n/var/lib/postgresql/data\n@@echo-doctor datadir 0\n' "${FAKE_DB_SIZE:-1048576}"
    printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/data 100 10 %s 10%% /data\n@@echo-doctor datadf 0\n' "${FAKE_DATA_FREE_KIB:-10485760}" ;;
  *) exec sh -c "$cmd" ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return d
}

// writeProfile writes the server profile of the project at remotePath: a
// complete one plus extra.
func (d *doctorRemote) writeProfile(t *testing.T, remotePath, extra string) {
	t.Helper()
	mustWrite(t, filepath.Join(d.home, ".config/echo/projects", config.ProjectKey(remotePath)+".toml"),
		"db_name = \"stg\"\nodoo_version = \"18\"\nodoo_container = \"odoo\"\ndb_container = \"db\"\n"+extra)
}

func (d *doctorRemote) calls() []string {
	b, _ := os.ReadFile(d.callLog)
	var out []string
	for _, c := range strings.Split(string(b), "\n----\n") {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out
}

func (d *doctorRemote) run(t *testing.T, cfg *config.Config, args ...string) (DoctorResult, *logRecorder, error) {
	t.Helper()
	rec := &logRecorder{}
	res, err := RunDoctor(context.Background(), DoctorOpts{Cfg: cfg, Root: t.TempDir(), Args: args, Log: rec.log})
	return res, rec, err
}

func checkByID(res DoctorResult, id string) (DoctorCheck, bool) {
	for _, c := range res.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return DoctorCheck{}, false
}

func checkField(c DoctorCheck, key string) string {
	for _, f := range c.Fields {
		if f[0] == key {
			return f[1]
		}
	}
	return ""
}

func wantStatus(t *testing.T, res DoctorResult, id, status string) DoctorCheck {
	t.Helper()
	c, ok := checkByID(res, id)
	if !ok {
		t.Fatalf("no %s check in %+v", id, res.Checks)
	}
	if c.Status != status {
		t.Errorf("%s = %s (%s %v), want %s", id, c.Status, c.Message, c.Fields, status)
	}
	return c
}

func TestParseDoctorArgs(t *testing.T) {
	good := map[string][]string{
		"bare":     nil,
		"from":     {"--from", "stg"},
		"from=":    {"--from=stg", "--json"},
		"remote":   {"--remote"},
		"json":     {"--json"},
		"together": {"--json", "--from", "stg"},
	}
	for name, args := range good {
		if _, err := parseDoctorArgs(args); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	p, _ := parseDoctorArgs([]string{"--from", "stg", "--json"})
	if p.from != "stg" || !p.jsonOut {
		t.Errorf("parsed %+v", p)
	}
	bad := map[string][]string{
		"-E":          {"-E", "acme/main"},
		"--env=":      {"--env=acme/main"},
		"positional":  {"stg"},
		"unknown":     {"--force"},
		"from bare":   {"--from"},
		"from a flag": {"--from", "--json"},
	}
	for name, args := range bad {
		if _, err := parseDoctorArgs(args); !errors.Is(err, ErrUsage) {
			t.Errorf("%s: err = %v, want ErrUsage", name, err)
		}
	}
}

func TestSplitDoctorSections(t *testing.T) {
	out := "a = 1\n@@echo-doctor global 0\nno trailing newline\n@@echo-doctor project 0\n\n@@echo-doctor rsync 1\n"
	s := splitDoctorSections(out)
	if s["global"].out != "a = 1" || s["project"].out != "no trailing newline" {
		t.Errorf("bodies = %q / %q", s["global"].out, s["project"].out)
	}
	if !s.ok("global") || s.ok("rsync") || s.ok("missing") {
		t.Errorf("rc parsing wrong: %+v", s)
	}
}

func TestDeployLockStates(t *testing.T) {
	ctx := context.Background()
	valid := `{"schema":1,"modules":{"sale":{"source":"worktree","via":"push","at":"x","verified":false}}}`
	cases := []struct {
		name    string
		out     string
		readErr error
		fetched lockState
		parsed  lockState
	}{
		{"absent", lockAbsentMarker + "\n", nil, lockAbsent, lockAbsent},
		{"found", valid, nil, lockFound, lockFound},
		{"corrupt", "{not json", nil, lockFound, lockCorrupt},
		{"unreadable", "", errors.New("cat: lock.json: Permission denied"), lockUnreadable, lockAbsent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeLockRemote{file: tc.out, readErr: tc.readErr}
			fake.install(t)
			raw, state, err := fetchDeployLock(ctx, lockTestRSC())
			if state != tc.fetched || (err != nil) != (tc.readErr != nil) {
				t.Fatalf("fetch = %s, %v; want %s", state, err, tc.fetched)
			}
			lock, state, err := parseDeployLock(raw)
			if state != tc.parsed || (state == lockCorrupt) != (err != nil) {
				t.Fatalf("parse = %s, %v; want %s", state, err, tc.parsed)
			}
			if state == lockFound && len(lock.Modules) != 1 {
				t.Errorf("lock = %+v", lock)
			}
		})
	}
	if _, state, _ := fetchDeployLock(ctx, remoteShellContext{}); state != lockAbsent {
		t.Errorf("no remote path: state = %s", state)
	}
}

func TestDeployLockReadScript(t *testing.T) {
	dir := t.TempDir()
	run := func() (string, error) {
		out, err := exec.Command("sh", "-c", lockReadScript(dir)).Output()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := run(); err != nil || out != lockAbsentMarker {
		t.Errorf("absent: %q %v", out, err)
	}
	mustWrite(t, lockFilePath(dir), `{"schema":1}`)
	if out, err := run(); err != nil || out != `{"schema":1}` {
		t.Errorf("found: %q %v", out, err)
	}
}

func TestCheckpointNeed(t *testing.T) {
	if got := checkpointNeed(1000, "db"); got != 1200 {
		t.Errorf("db need = %d", got)
	}
	if got := checkpointNeed(1000, "dump"); got != 500 {
		t.Errorf("dump need = %d", got)
	}
}

func TestMeasureCheckpointDiskDumpUsesHostDF(t *testing.T) {
	df := func(kib string) doctorOutput {
		return doctorOutput{out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 1 1 " + kib + " 1% /\n"}
	}
	host := doctorSections{"hostdf": df("1")}
	db := doctorSections{"size": {out: "4096"}, "datadf": df("100")}
	if m := measureCheckpointDisk("dump", host, db, nil); m.err != nil || m.free != 1024 || m.size != 4096 {
		t.Errorf("dump measured %+v, want the host df", m)
	}
	if m := measureCheckpointDisk("db", host, db, nil); m.err != nil || m.free != 100*1024 {
		t.Errorf("db measured %+v, want the data directory df", m)
	}
	if c := evalDisk(true, "dump", measureCheckpointDisk("dump", host, db, nil)); c.Status != doctorFailed {
		t.Errorf("dump on a full host disk = %s", c.Status)
	}
}

func TestDoctorHealthyRsyncTarget(t *testing.T) {
	d := newDoctorRemote(t)
	res, rec, err := d.run(t, d.cfg(), "--from", "stg")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ssh", "profile", "rsync", "disk", "lock", "dest"} {
		wantStatus(t, res, id, doctorOK)
	}
	if git := wantStatus(t, res, "git", doctorSkipped); git.Reason != "git_deploy_off" {
		t.Errorf("git reason = %q", git.Reason)
	}
	if _, shared := checkByID(res, "dest.shared"); shared {
		t.Error("dest.shared reported with no other target")
	}
	if res.Counts.Failed != 0 || res.Counts.Skipped != 1 || res.Target.Stage != "dev" || res.Target.DB != "stg" {
		t.Errorf("result = %+v", res)
	}
	if lock, _ := checkByID(res, "lock"); checkField(lock, "state") != "absent" {
		t.Errorf("lock = %+v", lock)
	}

	calls := d.calls()
	if len(calls) != 3 {
		t.Fatalf("ssh calls = %d, want 3:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	mutating := regexp.MustCompile(`\b(mkdir|mv|rm)\b`)
	composeVerb := regexp.MustCompile(`compose (\S+)`)
	for _, c := range calls {
		if mutating.MatchString(c) {
			t.Errorf("mutating call: %s", c)
		}
		for _, m := range composeVerb.FindAllStringSubmatch(c, -1) {
			if m[1] != "exec" {
				t.Errorf("compose verb %q in %s", m[1], c)
			}
		}
	}

	for _, l := range rec.lines {
		if strings.Contains(l, doctorTestPassword) {
			t.Errorf("password logged: %s", l)
		}
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), doctorTestPassword) {
		t.Error("password in the JSON report")
	}
	var decoded struct {
		Target map[string]any   `json:"target"`
		Checks []map[string]any `json:"checks"`
		Counts map[string]int   `json:"counts"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil || len(decoded.Checks) != 7 || decoded.Target["stage_declared"] != true {
		t.Errorf("json = %s (%v)", b, err)
	}
	if _, ok := decoded.Checks[0]["fields"].(map[string]any); !ok {
		t.Errorf("fields is not an object: %s", b)
	}
}

func TestDoctorFaultsKeepChecking(t *testing.T) {
	t.Run("no rsync on the server", func(t *testing.T) {
		d := newDoctorRemote(t)
		orig := doctorRunSSH
		doctorRunSSH = func(ctx context.Context, host, cmd string, stdin []byte) ([]byte, error) {
			return orig(ctx, host, strings.ReplaceAll(cmd, "command -v rsync", "command -v echo-no-rsync"), stdin)
		}
		t.Cleanup(func() { doctorRunSSH = orig })

		res, _, _ := d.run(t, d.cfg(), "--from", "stg")
		c := wantStatus(t, res, "rsync", doctorWarn)
		if checkField(c, "side") != "remote" {
			t.Errorf("rsync = %+v", c)
		}
		wantStatus(t, res, "dest", doctorOK)

		d.writeProfile(t, d.dir, "stage = \"dev\"\n[deploy]\npush = true\n")
		res, _, _ = d.run(t, d.cfg(), "--from", "stg")
		wantStatus(t, res, "rsync", doctorFailed)
		if res.Counts.Failed != 1 {
			t.Errorf("counts = %+v", res.Counts)
		}
	})

	t.Run("no project profile", func(t *testing.T) {
		d := newDoctorRemote(t)
		if err := os.Remove(filepath.Join(d.home, ".config/echo/projects", config.ProjectKey(d.dir)+".toml")); err != nil {
			t.Fatal(err)
		}
		res, _, _ := d.run(t, d.cfg(), "--from", "stg")
		wantStatus(t, res, "profile", doctorFailed)
		wantStatus(t, res, "rsync", doctorOK)
		wantStatus(t, res, "lock", doctorOK)
		for _, id := range []string{"disk", "dest"} {
			if c := wantStatus(t, res, id, doctorSkipped); c.Reason != "profile" {
				t.Errorf("%s reason = %q", id, c.Reason)
			}
		}
		if len(d.calls()) != 1 {
			t.Errorf("calls = %d, want only the host batch", len(d.calls()))
		}
	})

	t.Run("profile syntax error", func(t *testing.T) {
		d := newDoctorRemote(t)
		d.writeProfile(t, d.dir, "stage = \n")
		res, _, _ := d.run(t, d.cfg(), "--from", "stg")
		c := wantStatus(t, res, "profile", doctorFailed)
		if !strings.Contains(checkField(c, "file"), config.ProjectKey(d.dir)) || !strings.Contains(checkField(c, "error"), "line 5") {
			t.Errorf("profile = %+v", c)
		}
		wantStatus(t, res, "rsync", doctorOK)
	})

	t.Run("no stage", func(t *testing.T) {
		d := newDoctorRemote(t)
		d.writeProfile(t, d.dir, "")
		res, _, _ := d.run(t, d.cfg(), "--from", "stg")
		c := wantStatus(t, res, "profile", doctorWarn)
		if checkField(c, "stage_declared") != "false" || res.Target.Stage != "prod" || res.Counts.Failed != 0 {
			t.Errorf("profile = %+v, target = %+v", c, res.Target)
		}
	})

	t.Run("git target that is not a clone", func(t *testing.T) {
		d := newDoctorRemote(t)
		cfg := d.cfg()
		cfg.ConnectTargets[0].GitDeploy = true
		res, _, _ := d.run(t, cfg, "--from", "stg")
		c := wantStatus(t, res, "git", doctorFailed)
		if !strings.Contains(c.Message, "not one") {
			t.Errorf("git = %+v", c)
		}
		wantStatus(t, res, "lock", doctorOK)
		wantStatus(t, res, "dest", doctorOK)
	})
}

func TestDoctorUnreachableHost(t *testing.T) {
	d := newDoctorRemote(t)
	t.Setenv("FAKE_SSH_DOWN", "1")
	res, _, err := d.run(t, d.cfg(), "--from", "stg")
	if err != nil {
		t.Fatal(err)
	}
	c := wantStatus(t, res, "ssh", doctorFailed)
	if !strings.Contains(checkField(c, "error"), "Connection refused") {
		t.Errorf("ssh = %+v", c)
	}
	for _, id := range []string{"profile", "rsync", "git", "disk", "lock", "dest"} {
		if s := wantStatus(t, res, id, doctorSkipped); s.Reason != "unreachable" {
			t.Errorf("%s reason = %q", id, s.Reason)
		}
	}
	if len(d.calls()) != 1 || res.Counts.Failed != 1 {
		t.Errorf("calls = %d, counts = %+v", len(d.calls()), res.Counts)
	}
}

func TestDoctorCheckpointDisk(t *testing.T) {
	d := newDoctorRemote(t)
	t.Setenv("FAKE_DB_SIZE", "1073741824")
	t.Setenv("FAKE_DATA_FREE_KIB", "1024")

	d.writeProfile(t, d.dir, "stage = \"dev\"\n[checkpoint]\nmode = \"on\"\n")
	res, _, _ := d.run(t, d.cfg(), "--from", "stg")
	if c := wantStatus(t, res, "disk", doctorFailed); checkField(c, "method") != "db" {
		t.Errorf("disk = %+v", c)
	}

	d.writeProfile(t, d.dir, "stage = \"dev\"\n")
	res, _, _ = d.run(t, d.cfg(), "--from", "stg")
	wantStatus(t, res, "disk", doctorWarn)

	// The dump lands on the host, whose df is the real temp filesystem.
	t.Setenv("FAKE_DB_SIZE", "1024")
	d.writeProfile(t, d.dir, "stage = \"dev\"\n[checkpoint]\nmode = \"on\"\nmethod = \"dump\"\n")
	res, _, _ = d.run(t, d.cfg(), "--from", "stg")
	wantStatus(t, res, "disk", doctorOK)
}

func TestDoctorSharedDestination(t *testing.T) {
	d := newDoctorRemote(t)
	base := filepath.Dir(d.dir)
	shared := filepath.Join(base, "cache", "all_odoo")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	prd := filepath.Join(base, "prd")
	if err := os.MkdirAll(prd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(prd, "cache")); err != nil {
		t.Fatal(err)
	}
	d.writeProfile(t, d.dir, "stage = \"dev\"\n[push]\npath = \""+shared+"\"\n")

	cfg := func(prdHost, prdStage string) *config.Config {
		d.writeProfile(t, prd, "stage = \""+prdStage+"\"\n[push]\npath = \"cache\"\n")
		c := d.cfg()
		c.ConnectTargets = append(c.ConnectTargets, config.ConnectTarget{Name: "prd", SSHHost: prdHost, RemotePath: prd})
		return c
	}

	res, _, _ := d.run(t, cfg("fakehost", "prod"), "--from", "stg")
	c := wantStatus(t, res, "dest.shared", doctorWarn)
	if checkField(c, "with") != "prd" || checkField(c, "stage") != "prod" {
		t.Errorf("dest.shared = %+v", c)
	}

	res, _, _ = d.run(t, cfg("fakehost", "dev"), "--from", "stg")
	wantStatus(t, res, "dest.shared", doctorOK)

	res, _, _ = d.run(t, cfg("otherhost", "prod"), "--from", "stg")
	if c, found := checkByID(res, "dest.shared"); found {
		t.Errorf("different hosts reported as sharing: %+v", c)
	}
}

func TestDoctorUsageBeforeSSH(t *testing.T) {
	d := newDoctorRemote(t)
	for _, args := range [][]string{{"-E", "acme/main"}, {"stg"}, {"--bogus"}} {
		if _, _, err := d.run(t, d.cfg(), args...); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: err = %v, want ErrUsage", args, err)
		}
	}

	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = origTTY })
	cfg := d.cfg()
	cfg.ConnectTargets = append(cfg.ConnectTargets, config.ConnectTarget{Name: "other", SSHHost: "fakehost", RemotePath: "/srv/other"})
	if _, _, err := d.run(t, cfg); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("several targets without a TTY: err = %v", err)
	}
	if n := len(d.calls()); n != 0 {
		t.Errorf("ssh calls before a usage error = %d", n)
	}
}
