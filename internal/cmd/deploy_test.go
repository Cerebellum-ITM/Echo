package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseDeployArgs(t *testing.T) {
	cases := []struct {
		in      []string
		want    deployArgs
		wantErr bool
	}{
		{nil, deployArgs{limit: 20}, false},
		{[]string{"--from", "prod"}, deployArgs{from: "prod", limit: 20}, false},
		{[]string{"--from=prod", "--dry-run"}, deployArgs{from: "prod", limit: 20, dryRun: true}, false},
		{[]string{"--limit", "50", "--force"}, deployArgs{limit: 50, force: true}, false},
		{[]string{"--limit=5"}, deployArgs{limit: 5}, false},
		{[]string{"--limit", "0"}, deployArgs{}, true},
		{[]string{"--limit", "x"}, deployArgs{}, true},
		{[]string{"--from"}, deployArgs{}, true},
		{[]string{"--bogus"}, deployArgs{}, true},
		{[]string{"some_module"}, deployArgs{}, true},
		{[]string{"--i18n"}, deployArgs{limit: 20, i18n: true}, false},
		{[]string{"--no-i18n"}, deployArgs{limit: 20, noI18n: true}, false},
		{[]string{"--i18n", "--no-i18n"}, deployArgs{}, true}, // mutually exclusive
		{[]string{"--commits=a1b2,c3d4"}, deployArgs{limit: 20, commits: []string{"a1b2", "c3d4"}}, false},
		{[]string{"--commits", "a1b2, c3d4"}, deployArgs{limit: 20, commits: []string{"a1b2", "c3d4"}}, false},
		{[]string{"--modules=sale,account"}, deployArgs{limit: 20, modules: []string{"sale", "account"}}, false},
		{[]string{"--commits"}, deployArgs{}, true},
		{[]string{"--modules"}, deployArgs{}, true},
		{[]string{"--auto"}, deployArgs{limit: 20, auto: true}, false},
		{[]string{"--json"}, deployArgs{limit: 20, jsonOut: true}, false},
		{[]string{"--auto", "--json", "--dry-run"}, deployArgs{limit: 20, auto: true, jsonOut: true, dryRun: true}, false},
		{[]string{"--auto", "--modules=sale"}, deployArgs{}, true}, // mutually exclusive
		{[]string{"--auto", "--commits=a1b2"}, deployArgs{}, true}, // mutually exclusive
	}
	for _, tc := range cases {
		got, err := parseDeployArgs(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDeployArgs(%v): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDeployArgs(%v): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseDeployArgs(%v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// addonsRepo builds a temp repo layout with the given addon modules (each
// gets a __manifest__.py) plus a non-addon `docs/` folder.
func addonsRepo(t *testing.T, modules ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range modules {
		dir := filepath.Join(root, m)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "__manifest__.py"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestModuleFromSubject(t *testing.T) {
	root := addonsRepo(t, "sale_extra")

	cases := []struct {
		subject string
		want    string
	}{
		{"[FIX] sale_extra: correct tax rounding", "sale_extra"},
		{"[IMP]sale_extra:no spaces", "sale_extra"},
		{"[ADD] docs: not an addon", ""},       // names a non-addon dir
		{"[FIX] missing_mod: not on disk", ""}, // module doesn't exist
		{"plain subject without scheme", ""},   // no scheme at all
		{"[REL] sale_extra bump", ""},          // missing the colon
	}
	for _, tc := range cases {
		if got := moduleFromSubject(root, tc.subject); got != tc.want {
			t.Errorf("moduleFromSubject(%q) = %q, want %q", tc.subject, got, tc.want)
		}
	}
}

func TestModulesFromPaths(t *testing.T) {
	root := addonsRepo(t, "sale_extra", "stock_extra")

	cases := []struct {
		paths []string
		want  []string
	}{
		{[]string{"sale_extra/models/sale.py", "sale_extra/views/sale.xml"}, []string{"sale_extra"}},
		{[]string{"sale_extra/models/sale.py", "stock_extra/models/stock.py"}, []string{"sale_extra", "stock_extra"}},
		{[]string{"docs/readme.md", "README.md"}, nil},
		{nil, nil},
	}
	for _, tc := range cases {
		if got := modulesFromPaths(root, tc.paths); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("modulesFromPaths(%v) = %v, want %v", tc.paths, got, tc.want)
		}
	}
}

func TestParsePorcelainPaths(t *testing.T) {
	out := " M sale_extra/models/sale.py\n" +
		"?? stock_extra/views/new.xml\n" +
		"A  sale_extra/i18n/es.po\n" +
		`R  old_mod/x.py -> new_mod/x.py` + "\n" +
		` M "weird name/with space.py"` + "\n" +
		"\n" // trailing blank line
	got := parsePorcelainPaths(out)
	want := []string{
		"sale_extra/models/sale.py",
		"stock_extra/views/new.xml",
		"sale_extra/i18n/es.po",
		"new_mod/x.py",
		"weird name/with space.py",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePorcelainPaths = %v, want %v", got, want)
	}
}

func TestDirtyModulesFromPaths(t *testing.T) {
	root := addonsRepo(t, "sale_extra", "stock_extra")
	paths := []string{
		"sale_extra/models/sale.py",
		"docs/readme.md", // non-addon → dropped
		"stock_extra/views/s.xml",
		"sale_extra/i18n/es.po", // groups under sale_extra
		"README.md",             // top-level non-addon → dropped
	}
	got := dirtyModulesFromPaths(root, paths)
	want := []dirtyModule{
		{name: "sale_extra", paths: []string{"sale_extra/models/sale.py", "sale_extra/i18n/es.po"}},
		{name: "stock_extra", paths: []string{"stock_extra/views/s.xml"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dirtyModulesFromPaths = %+v, want %+v", got, want)
	}
}

func TestSplitInstallUpdate(t *testing.T) {
	states := map[string]string{
		"sale_extra":  "installed",
		"stock_extra": "to upgrade",
		"old_mod":     "uninstalled",
	}
	install, update := splitInstallUpdate(
		[]string{"brand_new", "old_mod", "sale_extra", "stock_extra"}, states)
	if !reflect.DeepEqual(update, []string{"sale_extra", "stock_extra"}) {
		t.Errorf("update = %v", update)
	}
	if !reflect.DeepEqual(install, []string{"brand_new", "old_mod"}) {
		t.Errorf("install = %v", install)
	}
}

func TestPathsTouchI18n(t *testing.T) {
	cases := []struct {
		module string
		paths  []string
		want   bool
	}{
		{"sale_extra", []string{"sale_extra/i18n/es.po"}, true},
		{"sale_extra", []string{"sale_extra/i18n/sale_extra.pot"}, true},
		{"sale_extra", []string{"sale_extra/models/sale.py"}, false},
		{"sale_extra", []string{"sale_extra/i18n_helpers/x.py"}, false}, // not the i18n/ dir
		{"sale_extra", []string{"stock_extra/i18n/es.po"}, false},       // another module's i18n
		{"sale_extra", nil, false},
	}
	for _, tc := range cases {
		if got := pathsTouchI18n(tc.module, tc.paths); got != tc.want {
			t.Errorf("pathsTouchI18n(%q, %v) = %v, want %v", tc.module, tc.paths, got, tc.want)
		}
	}
}

func TestI18nOverwriteDecision(t *testing.T) {
	cases := []struct {
		name                      string
		force, no, detectedUpdate bool
		wantState                 string
		wantOverwrite             bool
	}{
		{"auto on", false, false, true, "on", true},
		{"auto off", false, false, false, "off", false},
		{"forced no detection", true, false, false, "forced", true},
		{"forced with detection", true, false, true, "forced", true},
		{"suppressed detection", false, true, true, "suppressed", false},
		{"no-i18n without detection", false, true, false, "off", false},
	}
	for _, tc := range cases {
		state, ov := i18nOverwriteDecision(tc.force, tc.no, tc.detectedUpdate)
		if state != tc.wantState || ov != tc.wantOverwrite {
			t.Errorf("%s: i18nOverwriteDecision(%v,%v,%v) = (%q,%v), want (%q,%v)",
				tc.name, tc.force, tc.no, tc.detectedUpdate, state, ov, tc.wantState, tc.wantOverwrite)
		}
	}
}

func TestIsAddonDirRejectsPathTricks(t *testing.T) {
	root := addonsRepo(t, "sale_extra")
	if isAddonDir(root, "sale_extra/../sale_extra") {
		t.Fatal("path separators in a module name must be rejected")
	}
	if isAddonDir(root, "") {
		t.Fatal("empty name must be rejected")
	}
}

// TestParseDeployArgsUsageErrors asserts the caller-mistake paths wrap
// ErrUsage so the REPL/one-shot layer maps them to exit code 2.
func TestParseDeployArgsUsageErrors(t *testing.T) {
	for _, in := range [][]string{
		{"--bogus"},
		{"positional"},
		{"--i18n", "--no-i18n"},
		{"--auto", "--modules=sale"},
		{"--auto", "--commits=a1b2"},
		{"--push", "--no-push"},
		{"--set-push=maybe"},
		{"--set-checkpoint=sometimes"},
		{"--set-checkpoint-method=zip"},
		{"--set-checkpoint-keep=0"},
		{"--set-checkpoint-keep=-1"},
		{"--set-checkpoint-keep=x"},
		{"--set-checkpoint", "--checkpoint"},
		{"--set-checkpoint", "--no-checkpoint"},
	} {
		_, err := parseDeployArgs(in)
		if !errors.Is(err, ErrUsage) {
			t.Errorf("parseDeployArgs(%v) err = %v, want wraps ErrUsage", in, err)
		}
	}
}

func TestParseDeployArgsCheckpointSetFlags(t *testing.T) {
	t.Run("bare = on, method/keep untouched", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--set-checkpoint"})
		if err != nil || !p.isCheckpointManage() {
			t.Fatalf("got setCheckpoint=%v err=%v", p.setCheckpoint, err)
		}
		if p.setCheckpoint.mode == nil || *p.setCheckpoint.mode != "on" {
			t.Fatalf("mode = %v, want on", p.setCheckpoint.mode)
		}
		if p.setCheckpoint.method != nil || p.setCheckpoint.keep != nil {
			t.Fatalf("method/keep should stay nil, got %v/%v", p.setCheckpoint.method, p.setCheckpoint.keep)
		}
	})
	t.Run("mode values", func(t *testing.T) {
		for _, m := range []string{"on", "off", "auto"} {
			p, err := parseDeployArgs([]string{"--set-checkpoint=" + m})
			if err != nil || p.setCheckpoint.mode == nil || *p.setCheckpoint.mode != m {
				t.Fatalf("mode %q: got %v err=%v", m, p.setCheckpoint, err)
			}
		}
	})
	t.Run("method and keep combine in one call", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--set-checkpoint=on", "--set-checkpoint-method=dump", "--set-checkpoint-keep=5"})
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if p.setCheckpoint.mode == nil || *p.setCheckpoint.mode != "on" ||
			p.setCheckpoint.method == nil || *p.setCheckpoint.method != "dump" ||
			p.setCheckpoint.keep == nil || *p.setCheckpoint.keep != 5 {
			t.Fatalf("got %+v", p.setCheckpoint)
		}
	})
	t.Run("method-only without mode is a manage op", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--set-checkpoint-method=db"})
		if err != nil || !p.isCheckpointManage() || p.setCheckpoint.mode != nil {
			t.Fatalf("got %v err=%v", p.setCheckpoint, err)
		}
	})
}

func TestRunDeployCheckpointManage(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := config.Load("/test/project")
	cfg.Stage = "dev"
	opts := DeployOpts{Cfg: cfg}

	// Set only the mode: method/keep keep their resolved defaults.
	p, err := parseDeployArgs([]string{"--set-checkpoint=on"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runDeployCheckpointManage(opts, p); err != nil {
		t.Fatal(err)
	}
	r, _ := config.Load("/test/project")
	if r.CheckpointMode != "on" || r.CheckpointSource != "project" {
		t.Fatalf("after set: mode=%q source=%q", r.CheckpointMode, r.CheckpointSource)
	}
	if r.CheckpointMethod != config.Defaults.CheckpointMethod || r.CheckpointKeep != config.Defaults.CheckpointKeep {
		t.Errorf("unnamed fields changed: method=%q keep=%d", r.CheckpointMethod, r.CheckpointKeep)
	}

	// A second call sets keep only; mode persists from the first write.
	opts2 := DeployOpts{Cfg: r}
	p2, _ := parseDeployArgs([]string{"--set-checkpoint-keep=7"})
	if err := runDeployCheckpointManage(opts2, p2); err != nil {
		t.Fatal(err)
	}
	r2, _ := config.Load("/test/project")
	if r2.CheckpointMode != "on" || r2.CheckpointKeep != 7 {
		t.Errorf("after keep set: mode=%q keep=%d, want on/7", r2.CheckpointMode, r2.CheckpointKeep)
	}
}

func TestParseDeployArgsPushFlags(t *testing.T) {
	t.Run("no-push", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--no-push"})
		if err != nil || !p.noPush {
			t.Fatalf("got noPush=%v err=%v", p.noPush, err)
		}
	})
	t.Run("set-push bare = true", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--set-push"})
		if err != nil || p.setPush == nil || !*p.setPush {
			t.Fatalf("got setPush=%v err=%v", p.setPush, err)
		}
	})
	t.Run("set-push=false", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--set-push=false"})
		if err != nil || p.setPush == nil || *p.setPush {
			t.Fatalf("got setPush=%v err=%v", p.setPush, err)
		}
	})
}

func TestResolveDeployPush(t *testing.T) {
	tru, fls := true, false
	tests := []struct {
		name string
		p    deployArgs
		prof config.RemoteProfile
		cfg  *config.Config
		want bool
	}{
		{"no flag no config → off", deployArgs{}, config.RemoteProfile{}, &config.Config{}, false},
		{"--push wins", deployArgs{push: true}, config.RemoteProfile{DeployPush: &fls}, &config.Config{DeployPush: &fls}, true},
		{"--no-push wins", deployArgs{noPush: true}, config.RemoteProfile{DeployPush: &tru}, &config.Config{DeployPush: &tru}, false},
		{"server default on", deployArgs{}, config.RemoteProfile{DeployPush: &tru}, &config.Config{DeployPush: &fls}, true},
		{"local default when no server", deployArgs{}, config.RemoteProfile{}, &config.Config{DeployPush: &tru}, true},
		{"explicit local false", deployArgs{}, config.RemoteProfile{}, &config.Config{DeployPush: &fls}, false},
		{"nil cfg safe", deployArgs{}, config.RemoteProfile{}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveDeployPush(tc.p, tc.prof, tc.cfg); got != tc.want {
				t.Errorf("resolveDeployPush = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseDeployArgsTestFlags(t *testing.T) {
	t.Run("--test", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--test"})
		if err != nil || !p.test {
			t.Fatalf("got test=%v err=%v", p.test, err)
		}
	})
	t.Run("--test-modules=csv sets list", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--test-modules=sale,stock"})
		if err != nil || p.testModulesSet == nil ||
			!reflect.DeepEqual(*p.testModulesSet, []string{"sale", "stock"}) {
			t.Fatalf("got set=%v err=%v", p.testModulesSet, err)
		}
	})
	t.Run("--test-modules bare = picker", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--test-modules"})
		if err != nil || !p.testPick {
			t.Fatalf("got pick=%v err=%v", p.testPick, err)
		}
	})
	t.Run("--test-add", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--test-add", "sale,stock"})
		if err != nil || !reflect.DeepEqual(p.testAdd, []string{"sale", "stock"}) {
			t.Fatalf("got add=%v err=%v", p.testAdd, err)
		}
	})
	t.Run("--test-toggle standalone", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--test-toggle"})
		if err != nil || !p.testToggle {
			t.Fatalf("got toggle=%v err=%v", p.testToggle, err)
		}
	})
}

func TestParseDeployArgsTestUsageErrors(t *testing.T) {
	for _, in := range [][]string{
		{"--test", "--no-test"},                // mutually exclusive
		{"--test-modules", "--test-modules=a"}, // picker + explicit set
		{"--test-toggle", "--commits=a1b2"},    // management + selection
		{"--test-clear", "--test-add=sale"},    // two management ops
		{"--test-toggle", "--test"},            // management + per-run test
	} {
		if _, err := parseDeployArgs(in); !errors.Is(err, ErrUsage) {
			t.Errorf("parseDeployArgs(%v) err = %v, want wraps ErrUsage", in, err)
		}
	}
}

func TestParseDeployArgsRollbackOnFail(t *testing.T) {
	t.Run("--rollback-on-fail = true", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--rollback-on-fail"})
		if err != nil || p.rollbackOnFail == nil || !*p.rollbackOnFail {
			t.Fatalf("got %v err=%v", p.rollbackOnFail, err)
		}
	})
	t.Run("--no-rollback-on-fail = false", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--no-rollback-on-fail"})
		if err != nil || p.rollbackOnFail == nil || *p.rollbackOnFail {
			t.Fatalf("got %v err=%v", p.rollbackOnFail, err)
		}
	})
	t.Run("both together → ErrUsage", func(t *testing.T) {
		if _, err := parseDeployArgs([]string{"--rollback-on-fail", "--no-rollback-on-fail"}); !errors.Is(err, ErrUsage) {
			t.Fatalf("want ErrUsage, got %v", err)
		}
	})
	t.Run("unspecified = nil", func(t *testing.T) {
		p, err := parseDeployArgs(nil)
		if err != nil || p.rollbackOnFail != nil {
			t.Fatalf("got %v err=%v", p.rollbackOnFail, err)
		}
	})
}

func TestRollbackDecision(t *testing.T) {
	tru, fls := true, false
	tests := []struct {
		name        string
		p           deployArgs
		tty         bool
		wantDecided bool
		wantDo      bool
	}{
		{"explicit true wins over tty", deployArgs{rollbackOnFail: &tru}, true, true, true},
		{"explicit false wins over force", deployArgs{rollbackOnFail: &fls, force: true}, false, true, false},
		{"force → rollback, no prompt", deployArgs{force: true}, true, true, true},
		{"tty, no flags → defer to confirm", deployArgs{}, true, false, false},
		{"headless, no flags → rollback", deployArgs{}, false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decided, do := rollbackDecision(tc.p, tc.tty)
			if decided != tc.wantDecided || do != tc.wantDo {
				t.Errorf("rollbackDecision = (%v,%v), want (%v,%v)", decided, do, tc.wantDecided, tc.wantDo)
			}
		})
	}
}

func TestResolveDeployTest(t *testing.T) {
	tru, fls := true, false
	tests := []struct {
		name string
		p    deployArgs
		prof config.RemoteProfile
		cfg  *config.Config
		want bool
	}{
		{"no flag no config → off", deployArgs{}, config.RemoteProfile{}, &config.Config{}, false},
		{"--test wins", deployArgs{test: true}, config.RemoteProfile{DeployTest: &fls}, &config.Config{DeployTest: &fls}, true},
		{"--no-test wins", deployArgs{noTest: true}, config.RemoteProfile{DeployTest: &tru}, &config.Config{DeployTest: &tru}, false},
		{"server default on", deployArgs{}, config.RemoteProfile{DeployTest: &tru}, &config.Config{DeployTest: &fls}, true},
		{"local default when no server", deployArgs{}, config.RemoteProfile{}, &config.Config{DeployTest: &tru}, true},
		{"nil cfg safe", deployArgs{}, config.RemoteProfile{}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveDeployTest(tc.p, tc.prof, tc.cfg); got != tc.want {
				t.Errorf("resolveDeployTest = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveTestModules(t *testing.T) {
	deployed := []string{"a", "b"}
	t.Run("empty → deployed set", func(t *testing.T) {
		got := resolveTestModules(config.RemoteProfile{}, &config.Config{}, deployed)
		if !reflect.DeepEqual(got, deployed) {
			t.Errorf("got %v, want %v", got, deployed)
		}
	})
	t.Run("pinned local wins", func(t *testing.T) {
		got := resolveTestModules(config.RemoteProfile{}, &config.Config{DeployTestModules: []string{"x"}}, deployed)
		if !reflect.DeepEqual(got, []string{"x"}) {
			t.Errorf("got %v, want [x]", got)
		}
	})
	t.Run("server pinned wins over local", func(t *testing.T) {
		got := resolveTestModules(
			config.RemoteProfile{DeployTestModules: []string{"srv"}},
			&config.Config{DeployTestModules: []string{"loc"}}, deployed)
		if !reflect.DeepEqual(got, []string{"srv"}) {
			t.Errorf("got %v, want [srv]", got)
		}
	})
}

func TestTestModulesEditing(t *testing.T) {
	t.Run("merge dedupes and drops blanks", func(t *testing.T) {
		got := mergeTestModules([]string{"a", "b"}, []string{"b", "", "c"})
		if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Errorf("got %v, want [a b c]", got)
		}
	})
	t.Run("drop removes named", func(t *testing.T) {
		got := dropTestModules([]string{"a", "b", "c"}, []string{"b"})
		if !reflect.DeepEqual(got, []string{"a", "c"}) {
			t.Errorf("got %v, want [a c]", got)
		}
	})
	t.Run("labels", func(t *testing.T) {
		if testModulesLabel(nil) != "(auto)" {
			t.Errorf("empty label = %q, want (auto)", testModulesLabel(nil))
		}
		if testModulesLabel([]string{"a", "b"}) != "a,b" {
			t.Errorf("label = %q, want a,b", testModulesLabel([]string{"a", "b"}))
		}
	})
}

// TestGitAheadCommitsNoUpstream verifies the --auto helper degrades to an
// empty set (no error) when the branch has no upstream, so --auto still
// falls back to dirty modules instead of failing.
func TestGitAheadCommitsNoUpstream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")

	commits, err := gitAheadCommits(context.Background(), root)
	if err != nil {
		t.Fatalf("gitAheadCommits with no upstream: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("no-upstream ahead = %v, want empty", commits)
	}
}
