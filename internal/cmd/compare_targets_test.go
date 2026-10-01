package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func foundSide(name string, base *LockBase, modules map[string]LockModule) TargetSide {
	return TargetSide{Name: name, LockState: "found", Base: base, state: lockFound,
		lock: DeployLock{Schema: lockSchema, Base: base, Modules: modules}}
}

// stubIdentity answers from a fixed table keyed by "module@sha"; a key
// mapped to "-" is a sha whose commit does not contain the module.
func stubIdentity(trees map[string]string) identityFn {
	return func(module string, e LockModule) (string, bool) {
		if e.Dirty {
			return "", true
		}
		if e.Tree != "" {
			return e.Tree, true
		}
		tree := trees[module+"@"+e.SHA]
		if tree == "-" {
			return "", false
		}
		return tree, true
	}
}

func TestDiffLocks(t *testing.T) {
	dev := foundSide("dev", nil, map[string]LockModule{
		"flow":    {Source: "ref", SHA: "a1", Tree: "t-new", Version: "1.4.0"},
		"crm":     {Source: "worktree", SHA: "a1", Dirty: true, Version: "1.2.0"},
		"mail":    {Source: "worktree", SHA: "a1", Dirty: true},
		"same":    {Source: "worktree", SHA: "a1"},
		"bysha":   {Source: "commit", SHA: "c9"},
		"ghost":   {Source: "commit", SHA: "x1"},
		"equalsv": {Source: "ref", SHA: "a1", Tree: "t-eq", Version: "1.0"},
	})
	staging := foundSide("staging", nil, map[string]LockModule{
		"flow":    {Source: "commit", SHA: "b2", Tree: "t-old", Version: "1.3.0"},
		"mail":    {Source: "commit", SHA: "b2", Tree: "t-mail"},
		"same":    {Source: "ref", SHA: "b2", Tree: "t-same"},
		"bysha":   {Source: "worktree", SHA: "c9"},
		"ghost":   {Source: "commit", SHA: "y2"},
		"equalsv": {Source: "commit", SHA: "b2", Tree: "t-eq", Version: "1.0"},
	})
	identity := stubIdentity(map[string]string{"same@a1": "t-same"})

	got := map[string]TargetRow{}
	for _, r := range diffLocks(dev, staging, nil, identity) {
		got[r.Name] = r
	}
	want := map[string][4]string{ // status, only, reason, newer
		"flow":    {"differs", "", "", "dev"},
		"crm":     {"only", "dev", "", ""},
		"mail":    {"unknown", "", "dirty", ""},
		"same":    {"same", "", "", ""},
		"bysha":   {"same", "", "", ""},
		"ghost":   {"unknown", "", "no-identity", ""},
		"equalsv": {"same", "", "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %d", got, len(want))
	}
	for name, w := range want {
		r := got[name]
		if have := [4]string{r.Status, r.Only, r.Reason, r.Newer}; have != w {
			t.Errorf("%s = %v, want %v", name, have, w)
		}
	}
	if c := countTargetRows(diffLocks(dev, staging, nil, identity)); c != (TargetCounts{Same: 3, Differs: 1, Unknown: 2, OnlyA: 1}) {
		t.Errorf("counts = %+v", c)
	}
}

func TestDiffLocksBase(t *testing.T) {
	base := &LockBase{Branch: "echo/deploy", SHA: "base1", At: "2026-09-30T00:00:00Z"}
	git := foundSide("dev", base, map[string]LockModule{"pinned": {Source: "ref", SHA: "r1", Tree: "t-pin"}})
	rsync := foundSide("staging", nil, map[string]LockModule{
		"onbase":  {Source: "commit", SHA: "c1", Tree: "t-base"},
		"gone":    {Source: "commit", SHA: "c1", Tree: "t-gone"},
		"unknown": {Source: "commit", SHA: "c1", Tree: "t-unk"},
		"pinned":  {Source: "commit", SHA: "c1", Tree: "t-pin"},
	})
	identity := stubIdentity(map[string]string{"onbase@base1": "t-base", "gone@base1": "-"})

	rows := diffLocks(git, rsync, nil, identity)
	got := map[string]string{}
	for _, r := range rows {
		got[r.Name] = r.StatusLabel() + "/" + r.Reason
		if r.Name == "onbase" && (r.A == nil || r.A.label() != "base@base1") {
			t.Errorf("onbase cell = %+v, want the base", r.A)
		}
	}
	want := map[string]string{
		"onbase":  "same/",
		"gone":    "only staging/",
		"unknown": "unknown/no-identity",
		"pinned":  "same/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestDiffLocksFilterAndFailures(t *testing.T) {
	a := foundSide("dev", nil, map[string]LockModule{"flow": {Source: "ref", SHA: "a1", Tree: "t1"}})
	b := foundSide("staging", nil, map[string]LockModule{"flow": {Source: "ref", SHA: "a1", Tree: "t1"}})
	identity := stubIdentity(nil)

	rows := diffLocks(a, b, []string{"flow", "absent"}, identity)
	if len(rows) != 2 || rows[0].Status != "same" || !rows[0].Named || rows[1].Status != "unknown" {
		t.Fatalf("filtered rows = %+v", rows)
	}

	broken := TargetSide{Name: "staging", LockState: "unreadable", state: lockUnreadable}
	rows = diffLocks(a, broken, nil, identity)
	if len(rows) != 1 || rows[0].Status != "unknown" || rows[0].Reason != "unreadable" || rows[0].A == nil || rows[0].B != nil {
		t.Fatalf("one side unreadable = %+v", rows)
	}
	if cell := TargetCell(broken, nil); cell != "unreadable" {
		t.Errorf("failed side cell = %q", cell)
	}
	if rows := diffLocks(TargetSide{state: lockCorrupt}, broken, nil, identity); rows == nil || len(rows) != 0 {
		t.Fatalf("both failing = %#v, want an empty slice", rows)
	}
}

func TestTargetCell(t *testing.T) {
	side := foundSide("dev", nil, nil)
	cases := []struct {
		e    *LockModule
		want string
	}{
		{nil, "none"},
		{&LockModule{Source: "worktree", SHA: "0b6fc41aa", Dirty: true, Version: "1.2.0"}, "worktree@0b6fc41+dirty 1.2.0 unv."},
		{&LockModule{Source: "ref", SHA: "99f2109ff", Version: "1.4.0", Verified: true}, "ref@99f2109 1.4.0"},
		{&LockModule{Source: lockSourceBase, SHA: "3f2a9c1aa"}, "base@3f2a9c1"},
	}
	for _, tc := range cases {
		if got := TargetCell(side, tc.e); got != tc.want {
			t.Errorf("TargetCell(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestLockIdentity(t *testing.T) {
	r := newSourceRepo(t)
	r.write("addons/flow/__manifest__.py", "{'version': '1.0'}")
	first := r.commit("[ADD] flow")
	r.write("addons/late/__manifest__.py", "{'version': '1.0'}")
	second := r.commit("[ADD] late")
	flowTree := r.git("rev-parse", first+":addons/flow")
	cfg := &config.Config{AddonsPaths: []string{"addons"}}
	ctx := context.Background()

	cases := []struct {
		name        string
		module      string
		e           LockModule
		wantTree    string
		wantPresent bool
	}{
		{"recorded tree wins", "flow", LockModule{SHA: first, Tree: "recorded"}, "recorded", true},
		{"clean worktree derives its tree", "flow", LockModule{Source: "worktree", SHA: second}, flowTree, true},
		{"dirty has no identity", "flow", LockModule{Source: "worktree", SHA: second, Dirty: true}, "", true},
		{"sha not in the repository", "flow", LockModule{SHA: strings.Repeat("e", 40)}, "", true},
		{"module absent at the sha", "late", LockModule{SHA: first}, "", false},
		{"module not in the checkout", "nowhere", LockModule{SHA: first}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree, present := lockIdentity(ctx, cfg, r.root, tc.module, tc.e)
			if tree != tc.wantTree || present != tc.wantPresent {
				t.Errorf("lockIdentity = (%q, %v), want (%q, %v)", tree, present, tc.wantTree, tc.wantPresent)
			}
		})
	}
}

// targetsFixture is a local repository and two fake-ssh targets on one host.
type targetsFixture struct {
	remote     *fakeRemote
	repo       *sourceRepo
	cfg        *config.Config
	devDir     string
	stagingDir string
	oldSHA     string
	newSHA     string
}

func newTargetsFixture(t *testing.T) *targetsFixture {
	t.Helper()
	f := &targetsFixture{remote: newFakeRemote(t), repo: newSourceRepo(t)}
	f.repo.write("addons/flow/__manifest__.py", "{'version': '1.3.0'}")
	f.repo.write("addons/same/__manifest__.py", "{'version': '1.0'}")
	f.oldSHA = f.repo.commit("[ADD] flow, same")
	f.repo.write("addons/flow/__manifest__.py", "{'version': '1.4.0'}")
	f.newSHA = f.repo.commit("[IMP] flow")
	f.devDir = filepath.Join(f.remote.dir, "dev")
	f.stagingDir = filepath.Join(f.remote.dir, "staging")
	f.cfg = &config.Config{
		AddonsPaths: []string{"addons"},
		ConnectTargets: []config.ConnectTarget{
			{Name: "dev", SSHHost: "fakehost", RemotePath: f.devDir},
			{Name: "staging", SSHHost: "fakehost", RemotePath: f.stagingDir},
			{Name: "twin", SSHHost: "fakehost", RemotePath: f.devDir},
		},
	}
	return f
}

func (f *targetsFixture) writeLock(t *testing.T, dir string, lock DeployLock) {
	t.Helper()
	b, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, lockFilePath(dir), string(b))
}

func (f *targetsFixture) run(args ...string) (CompareTargetsResult, *logRecorder, error) {
	rec := &logRecorder{}
	res, err := RunCompareTargets(context.Background(), CompareTargetsOpts{
		Cfg: f.cfg, Root: f.repo.root, Args: args, Log: rec.log,
	})
	return res, rec, err
}

func TestCompareTargetsEndToEnd(t *testing.T) {
	f := newTargetsFixture(t)
	tree := func(sha, mod string) string { return f.repo.git("rev-parse", sha+":addons/"+mod) }
	f.writeLock(t, f.stagingDir, DeployLock{Schema: 1, Target: "staging", Modules: map[string]LockModule{
		"flow":  {Source: "commit", SHA: f.oldSHA, Tree: tree(f.oldSHA, "flow"), Version: "1.3.0"},
		"mail":  {Source: "commit", SHA: f.oldSHA, Tree: "t-mail"},
		"same":  {Source: "ref", SHA: f.oldSHA, Tree: tree(f.oldSHA, "same"), Version: "1.0"},
		"ghost": {Source: "commit", SHA: strings.Repeat("e", 40)},
	}})
	devLock := DeployLock{Schema: 1, Target: "dev", Modules: map[string]LockModule{
		"flow":  {Source: "ref", SHA: f.newSHA, Tree: tree(f.newSHA, "flow"), Version: "1.4.0", Verified: true},
		"crm":   {Source: "worktree", SHA: f.newSHA, Dirty: true, Version: "1.2.0"},
		"mail":  {Source: "worktree", SHA: f.newSHA, Dirty: true},
		"same":  {Source: "worktree", SHA: f.newSHA, Version: "1.0"},
		"ghost": {Source: "commit", SHA: strings.Repeat("d", 40)},
	}}
	f.writeLock(t, f.devDir, devLock)

	res, rec, err := f.run("--targets", "dev,staging")
	if err != nil {
		t.Fatalf("RunCompareTargets: %v", err)
	}
	if res.Failed() {
		t.Fatalf("a side failed: %+v %+v", res.A, res.B)
	}
	got := map[string]string{}
	for _, r := range res.Modules {
		got[r.Name] = r.StatusLabel() + "/" + r.Reason + "/" + r.Newer
	}
	want := map[string]string{
		"flow":  "differs//dev",
		"crm":   "only dev//",
		"mail":  "unknown/dirty/",
		"same":  "same//",
		"ghost": "unknown/no-identity/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !rec.has("INFO targets lock target=dev modules=5 unverified=4") || !rec.has("INFO targets lock target=staging modules=4") {
		t.Errorf("side lines missing:\n%s", strings.Join(rec.lines, "\n"))
	}

	calls := strings.Split(strings.TrimSpace(f.remote.sshLog()), "\n")
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "cat ") || !strings.Contains(calls[0], "/dev/.echo/lock.json") ||
		!strings.Contains(calls[1], "/staging/.echo/lock.json") {
		t.Fatalf("ssh calls = %q, want one lock cat per target, dev first", calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "mkdir") || strings.Contains(c, " mv ") || strings.Contains(c, "compose") {
			t.Fatalf("a read-only compare ran %q", c)
		}
	}

	var obj map[string]any
	b, _ := json.Marshal(res)
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	a := obj["a"].(map[string]any)
	if a["name"] != "dev" || a["host"] != "fakehost" || a["lock"] != "found" || a["base"] != nil {
		t.Errorf("side a = %v", a)
	}
	counts := obj["counts"].(map[string]any)
	if counts["only_a"] != float64(1) || counts["differs"] != float64(1) || counts["unknown"] != float64(2) {
		t.Errorf("counts = %v", counts)
	}
}

func TestCompareTargetsAbsentLock(t *testing.T) {
	f := newTargetsFixture(t)
	f.writeLock(t, f.devDir, DeployLock{Schema: 1, Modules: map[string]LockModule{
		"flow": {Source: "ref", SHA: f.newSHA, Tree: "t1"},
		"same": {Source: "ref", SHA: f.newSHA, Tree: "t2"},
	}})
	res, rec, err := f.run("--targets=dev,staging")
	if err != nil || res.Failed() {
		t.Fatalf("err = %v failed = %v", err, res.Failed())
	}
	if res.B.LockState != "absent" || !rec.has("INFO targets no deploy lock target=staging") {
		t.Fatalf("absent side = %+v\n%s", res.B, strings.Join(rec.lines, "\n"))
	}
	for _, r := range res.Modules {
		if r.StatusLabel() != "only dev" {
			t.Errorf("%s = %s, want only dev", r.Name, r.StatusLabel())
		}
	}
}

func TestCompareTargetsBothEmptyJSON(t *testing.T) {
	f := newTargetsFixture(t)
	res, _, err := f.run("--targets", "dev,staging", "--json")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"modules":[]`) || !strings.Contains(string(b), `"base":null`) {
		t.Fatalf("json = %s", b)
	}
}

func TestCompareTargetsFailedSides(t *testing.T) {
	f := newTargetsFixture(t)
	f.writeLock(t, f.devDir, DeployLock{Schema: 1, Modules: map[string]LockModule{"flow": {Source: "ref", SHA: f.newSHA, Tree: "t1"}}})
	mustWrite(t, lockFilePath(f.stagingDir), "{not json")

	res, rec, err := f.run("--targets", "dev,staging")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed() || res.B.LockState != "corrupt" || !rec.has("ERROR targets deploy lock is corrupt target=staging") {
		t.Fatalf("corrupt side = %+v\n%s", res.B, strings.Join(rec.lines, "\n"))
	}
	if len(res.Modules) != 1 || res.Modules[0].Status != "unknown" || res.Modules[0].Reason != "unreadable" || res.Modules[0].A == nil {
		t.Fatalf("rows = %+v", res.Modules)
	}

	orig := lockRunSSH
	lockRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		if strings.Contains(remoteCmd, "/dev/") {
			return nil, errors.New("ssh: connect to host fakehost: Connection refused")
		}
		return orig(ctx, host, remoteCmd, stdin)
	}
	t.Cleanup(func() { lockRunSSH = orig })
	res, rec, err = f.run("--targets", "dev,staging")
	if err != nil {
		t.Fatal(err)
	}
	if !res.A.Failed() || !res.B.Failed() || len(res.Modules) != 0 ||
		!rec.has("ERROR targets could not read the deploy lock target=dev reason=ssh: connect") {
		t.Fatalf("both failing = %+v\n%s", res, strings.Join(rec.lines, "\n"))
	}
}

func TestCompareTargetsUsage(t *testing.T) {
	f := newTargetsFixture(t)
	cases := [][]string{
		{"--targets", "dev"},
		{"--targets", "dev,staging,prod"},
		{"--targets", "dev,dev"},
		{"--targets", "dev,nope"},
		{"--targets", "dev,twin"},
		{"--targets", "dev,staging", "--from", "dev"},
		{"--targets", "dev,staging", "--all"},
		{"--targets", "dev,staging", "--remote"},
		{"--targets", "dev,staging", "-E", "acme/main"},
		{"--targets"},
	}
	for _, args := range cases {
		if _, _, err := f.run(args...); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: err = %v, want ErrUsage", args, err)
		}
	}
	if log := f.remote.sshLog(); log != "" {
		t.Fatalf("usage errors reached ssh:\n%s", log)
	}
	if _, err := parseCompareArgs([]string{"sale", "--json"}); !errors.Is(err, ErrUsage) {
		t.Errorf("--json without --targets: err = %v, want ErrUsage", err)
	}
	if p, err := parseCompareArgs([]string{"sale", "stock", "--all"}); err != nil || p.module() != "sale" {
		t.Errorf("compare without --targets changed: %+v, %v", p, err)
	}
}
