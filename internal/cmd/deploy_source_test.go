package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestSplitModuleRefs(t *testing.T) {
	names, refs, err := splitModuleRefs([]string{"sale@99f2109", "stock"}, "")
	if err != nil || !reflect.DeepEqual(names, []string{"sale", "stock"}) ||
		!reflect.DeepEqual(refs, map[string]string{"sale": "99f2109"}) {
		t.Fatalf("got %v %v %v", names, refs, err)
	}
	_, refs, _ = splitModuleRefs([]string{"sale@99f2109", "stock"}, "main")
	if !reflect.DeepEqual(refs, map[string]string{"sale": "99f2109", "stock": "main"}) {
		t.Errorf("--at did not pin the unpinned entry: %v", refs)
	}
	if names, refs, _ := splitModuleRefs(nil, ""); names != nil || refs != nil {
		t.Errorf("empty input: %v %v", names, refs)
	}
	for _, bad := range [][]string{{"sale@"}, {"sale@a", "sale@b"}} {
		if _, _, err := splitModuleRefs(bad, ""); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: want ErrUsage, got %v", bad, err)
		}
	}
}

func TestParseDeployArgsModuleRefs(t *testing.T) {
	a, err := parseDeployArgs([]string{"--modules", "sale@abc,stock", "--at", "main", "--no-fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.modules, []string{"sale", "stock"}) || a.moduleRefs["sale"] != "abc" || a.moduleRefs["stock"] != "main" || !a.noFetch {
		t.Errorf("parsed %+v", a)
	}
	for _, args := range [][]string{
		{"--at", "main"},
		{"--at", "main", "--modules", "sale", "--commits", "abc"},
		{"--modules", "sale@abc", "--no-push"},
		{"--modules", "sale", "--fetch"},
		{"--modules", "sale@abc", "--keep-overlay"},
	} {
		if _, err := parseDeployArgs(args); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: want ErrUsage, got %v", args, err)
		}
	}
}

// sourceRepo is a scratch repository for the source tests, with helpers to
// write, delete and commit files.
type sourceRepo struct {
	t    *testing.T
	root string
}

func newSourceRepo(t *testing.T) *sourceRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	r := &sourceRepo{t: t, root: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *sourceRepo) git(args ...string) string {
	r.t.Helper()
	c := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *sourceRepo) write(rel, content string) {
	r.t.Helper()
	mustWrite(r.t, filepath.Join(r.root, rel), content)
}

func (r *sourceRepo) remove(rel string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.root, rel)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *sourceRepo) commit(msg string) string {
	r.git("add", "-A")
	r.git("commit", "-qm", msg)
	return r.git("rev-parse", "HEAD")
}

func TestLocateModuleAt(t *testing.T) {
	ctx := context.Background()
	r := newSourceRepo(t)
	cfg := &config.Config{AddonsPaths: []string{"addons"}}
	r.write("addons/sale/__manifest__.py", "{'name': 'sale'}")
	r.write("legacy/crm/__manifest__.py", "{'name': 'crm'}")
	sha := r.commit("init")

	if got, err := locateModuleAt(ctx, cfg, r.root, sha, "sale"); err != nil || got != "addons/sale" {
		t.Errorf("sale = %q, %v", got, err)
	}
	r.remove("legacy/crm/__manifest__.py")
	if got, err := locateModuleAt(ctx, cfg, r.root, sha, "crm"); err != nil || got != "legacy/crm" {
		t.Errorf("module gone from disk = %q, %v", got, err)
	}
	if _, err := locateModuleAt(ctx, cfg, r.root, sha, "ghost"); !errors.Is(err, ErrUsage) ||
		!strings.Contains(err.Error(), "does not exist at") {
		t.Errorf("missing module: %v", err)
	}

	r.write("other/sale/__manifest__.py", "{'name': 'sale'}")
	r.remove("addons/sale/__manifest__.py")
	r.write("again/sale/__manifest__.py", "{'name': 'sale'}")
	twice := r.commit("two copies")
	if _, err := locateModuleAt(ctx, cfg, r.root, twice, "sale"); !errors.Is(err, ErrUsage) ||
		!strings.Contains(err.Error(), "several places") {
		t.Errorf("ambiguous module: %v", err)
	}
}

func TestArchiveModuleSources(t *testing.T) {
	ctx := context.Background()
	r := newSourceRepo(t)
	r.write("addons/sale/__manifest__.py", "{'version': '1.0'}")
	r.write("addons/sale/old.py", "old")
	r.write("addons/stock/__manifest__.py", "{'version': '1.0'}")
	first := r.commit("first")
	r.write("addons/sale/__manifest__.py", "{'version': '2.0'}")
	r.remove("addons/sale/old.py")
	r.write("addons/stock/__manifest__.py", "{'version': '2.0'}")
	second := r.commit("second")

	dir, cleanup, err := archiveModuleSources(ctx, r.root, map[string]moduleSource{
		"sale":  {kind: lockSourceRef, sha: second, path: "addons/sale"},
		"stock": {kind: lockSourceRef, sha: first, path: "addons/stock"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(dir, rel))
		return string(b)
	}
	if read("addons/sale/__manifest__.py") != "{'version': '2.0'}" || read("addons/stock/__manifest__.py") != "{'version': '1.0'}" {
		t.Errorf("each module must come from its own commit")
	}
	if _, err := os.Stat(filepath.Join(dir, "addons/sale/old.py")); !os.IsNotExist(err) {
		t.Errorf("a file deleted at the commit was extracted: %v", err)
	}
}

func TestRefTouchesI18n(t *testing.T) {
	ctx := context.Background()
	r := newSourceRepo(t)
	r.write("addons/sale/__manifest__.py", "{}")
	r.write("addons/sale/i18n/es.po", "a")
	before := r.commit("before")
	r.write("addons/sale/models.py", "x")
	same := r.commit("code only")
	r.write("addons/sale/i18n/es.po", "b")
	changed := r.commit("terms")

	prev := LockModule{Source: lockSourceRef, SHA: before}
	if touched, known := refTouchesI18n(ctx, r.root, prev, true, same, "addons/sale"); touched || !known {
		t.Errorf("unchanged i18n: touched=%v known=%v", touched, known)
	}
	if touched, known := refTouchesI18n(ctx, r.root, prev, true, changed, "addons/sale"); !touched || !known {
		t.Errorf("changed i18n: touched=%v known=%v", touched, known)
	}
	for _, p := range []LockModule{
		{Source: lockSourceWorktree, SHA: before},
		{Source: lockSourceCommit, SHA: "0123456789abcdef0123456789abcdef01234567"},
	} {
		if _, known := refTouchesI18n(ctx, r.root, p, true, changed, "addons/sale"); known {
			t.Errorf("%+v: nothing comparable, want known=false", p)
		}
	}
	if _, known := refTouchesI18n(ctx, r.root, LockModule{}, false, changed, "addons/sale"); known {
		t.Error("no lock entry, want known=false")
	}
}

// fakeRemote is a target whose "server" is a local directory: a fake `ssh` on
// PATH runs every remote command locally, records compose calls instead of
// running them, and answers the module-state query.
type fakeRemote struct {
	dir     string
	logPath string
}

func newFakeRemote(t *testing.T) *fakeRemote {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &fakeRemote{dir: filepath.Join(t.TempDir(), "srv"), logPath: filepath.Join(home, "compose.log")}
	mustWrite(t, filepath.Join(home, ".config/echo/projects", config.ProjectKey(f.dir)+".toml"),
		"stage = \"dev\"\ndb_name = \"stg\"\nodoo_version = \"18\"\n")
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "ssh"), `#!/bin/sh
while [ "$1" = "-o" ]; do shift 2; done
shift
cmd="$*"
case "$cmd" in
  *psql*) printf 'sale|installed|18.0.1.0\nstock|installed|18.0.1.0\n' ;;
  *compose*) printf '%s\n' "$cmd" >> "`+f.logPath+`" ;;
  *) exec sh -c "$cmd" ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f *fakeRemote) composeLog() string {
	b, _ := os.ReadFile(f.logPath)
	return string(b)
}

func (f *fakeRemote) cfg() *config.Config {
	return &config.Config{
		AddonsPaths:    []string{"addons"},
		PushPath:       filepath.Join(f.dir, "addons"),
		ConnectTargets: []config.ConnectTarget{{Name: "stg", SSHHost: "fakehost", RemotePath: f.dir}},
	}
}

// treeFiles maps every regular file under dir to its content.
func treeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

// incidentRepo reproduces the 2026-09-30 shape: the fix lives at `fix`, the
// local checkout moved on past it, and the module is dirty on disk.
func incidentRepo(t *testing.T) (r *sourceRepo, fix string) {
	r = newSourceRepo(t)
	r.write("addons/sale/__manifest__.py", "{'name': 'sale', 'version': '18.0.1.0'}")
	r.write("addons/sale/models.py", "v1")
	r.write("addons/sale/legacy.py", "removed by the fix")
	r.write("addons/stock/__manifest__.py", "{'name': 'stock', 'version': '18.0.1.0'}")
	r.commit("[ADD] sale: base")
	r.write("addons/sale/__manifest__.py", "{'name': 'sale', 'version': '18.0.1.1'}")
	r.write("addons/sale/models.py", "v1 + fix")
	r.remove("addons/sale/legacy.py")
	fix = r.commit("[FIX] sale: the three lines")
	r.write("addons/sale/__manifest__.py", "{'name': 'sale', 'version': '18.0.1.3'}")
	r.write("addons/sale/models.py", "refactor + migration")
	r.commit("[IMP] sale: refactor")
	r.write("addons/sale/models.py", "uncommitted edit")
	return r, fix
}

func TestRunDeployModuleAtRef(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/legacy.py"), "removed by the fix")
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/stray.py"), "never committed")
	mustWrite(t, filepath.Join(remote.dir, "addons/stock/models.py"), "untouched")
	before := treeFiles(t, remote.dir)

	run := func(extra ...string) (DeployResult, error) {
		args := append([]string{"--modules", "sale@" + fix[:7], "--from", "stg", "--push", "--force", "--no-lint"}, extra...)
		return RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args})
	}

	res, err := run("--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !reflect.DeepEqual(treeFiles(t, remote.dir), before) || remote.composeLog() != "" {
		t.Fatal("the dry-run changed the server")
	}
	if len(res.Modules) != 1 || res.Modules[0].Source != lockSourceRef || res.Modules[0].Version != "18.0.1.1" {
		t.Errorf("dry-run plan = %+v", res.Modules)
	}

	if _, err := run(); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	want := map[string]string{
		"__manifest__.py": "{'name': 'sale', 'version': '18.0.1.1'}",
		"models.py":       "v1 + fix",
	}
	if got := treeFiles(t, filepath.Join(remote.dir, "addons/sale")); !reflect.DeepEqual(got, want) {
		t.Errorf("server module = %v, want the tree at the fix %v", got, want)
	}
	if got := treeFiles(t, filepath.Join(remote.dir, "addons/stock")); got["models.py"] != "untouched" {
		t.Errorf("another module changed: %v", got)
	}
	runLine := ""
	for _, l := range strings.Split(remote.composeLog(), "\n") {
		if strings.Contains(l, "stop-after-init") {
			runLine = l
		}
	}
	if !strings.Contains(runLine, "'-u' 'sale'") || strings.Contains(runLine, "stock") {
		t.Errorf("odoo run = %q, want -u sale only", runLine)
	}

	lock, found := readDeployLock(ctx, remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}, nil)
	e := lock.Modules["sale"]
	if !found || e.Source != lockSourceRef || e.SHA != fix || e.Ref != fix[:7] || !e.Verified || e.Version != "18.0.1.1" {
		t.Errorf("lock entry = %+v (found=%v)", e, found)
	}
}

func TestRunDeployCommitShipsTheCommit(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")

	args := []string{"--commits", fix, "--from", "stg", "--push", "--force", "--no-lint"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	got := treeFiles(t, filepath.Join(remote.dir, "addons/sale"))
	if got["models.py"] != "v1 + fix" {
		t.Errorf("shipped %q, want the selected commit's tree, not the disk or a later commit", got["models.py"])
	}
	lock, _ := readDeployLock(ctx, remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}, nil)
	if e := lock.Modules["sale"]; e.Source != lockSourceCommit || e.SHA != fix {
		t.Errorf("lock entry = %+v", e)
	}
}

func TestRunDeployWorktreeReleasesPin(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	if err := os.MkdirAll(filepath.Join(remote.dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	pin := []string{"--modules", "sale@" + fix, "--from", "stg", "--push", "--force", "--no-lint"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: pin}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/only_in_pin.py"), "pinned tree leftover")

	plain := []string{"--modules", "sale", "--from", "stg", "--push", "--force", "--no-lint"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: plain}); err != nil {
		t.Fatal(err)
	}
	got := treeFiles(t, filepath.Join(remote.dir, "addons/sale"))
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"__manifest__.py", "models.py"}) || got["models.py"] != "uncommitted edit" {
		t.Errorf("after the working-tree ship: %v", got)
	}
}

func TestRunDeployRefOnGitTarget(t *testing.T) {
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
	deploy := func(args ...string) {
		t.Helper()
		args = append(args, "--from", "stg", "--push", "--force", "--no-lint")
		if _, err := RunDeploy(ctx, DeployOpts{Cfg: cfg, Root: r.root, Args: args}); err != nil {
			t.Fatalf("deploy %v: %v", args, err)
		}
	}

	deploy("--modules", "sale@"+fix)
	if head := srv.git("rev-parse", "HEAD"); head != base {
		t.Errorf("a pinned module moved the branch to %s", head)
	}
	if got := treeFiles(t, filepath.Join(remote.dir, "addons/sale")); got["models.py"] != "v1 + fix" || got["legacy.py"] != "" {
		t.Errorf("overlay = %v, want the tree at the fix", got)
	}
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/only_in_pin.py"), "pin leftover")

	deploy("--commits", refactor)
	if head := srv.git("rev-parse", "HEAD"); head != refactor {
		t.Errorf("branch at %s, want %s", head, refactor)
	}
	if st := srv.git("status", "--porcelain"); st != "" {
		t.Errorf("the pin survived the branch deploy:\n%s", st)
	}
	lock, _ := readDeployLock(ctx, remoteShellContext{sshHost: "fakehost", remotePath: remote.dir}, nil)
	if e := lock.Modules["sale"]; e.Source != lockSourceBranch || e.SHA != refactor || lock.Base == nil {
		t.Errorf("lock after the branch deploy: %+v base=%+v", e, lock.Base)
	}
}

func TestRunDeployCommitsWithoutPush(t *testing.T) {
	ctx := context.Background()
	remote := newFakeRemote(t)
	r, fix := incidentRepo(t)
	mustWrite(t, filepath.Join(remote.dir, "addons/sale/models.py"), "running on the server")
	before := treeFiles(t, remote.dir)

	args := []string{"--commits", fix, "--from", "stg", "--force", "--no-lint"}
	if _, err := RunDeploy(ctx, DeployOpts{Cfg: remote.cfg(), Root: r.root, Args: args}); err != nil {
		t.Fatalf("an update-only deploy of a commit must not need a push: %v", err)
	}
	if !reflect.DeepEqual(treeFiles(t, remote.dir), before) {
		t.Error("a deploy without push changed the server's code")
	}
}
