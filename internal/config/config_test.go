package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load("/nonexistent/project/path/xyz123")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "charm" {
		t.Errorf("Theme = %q, want charm", cfg.Theme)
	}
	if cfg.Logo != "echo" {
		t.Errorf("Logo = %q, want echo", cfg.Logo)
	}
	if cfg.OdooVersion != "18" {
		t.Errorf("OdooVersion = %q, want 18", cfg.OdooVersion)
	}
	if cfg.Stage != "dev" {
		t.Errorf("Stage = %q, want dev", cfg.Stage)
	}
	if cfg.FilestorePath != "/var/lib/odoo/filestore" {
		t.Errorf("FilestorePath = %q, want /var/lib/odoo/filestore", cfg.FilestorePath)
	}
}

func TestProjectKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := "/some/project"
	cfg, _ := Load(path)
	sum := sha256.Sum256([]byte(path))
	expected := fmt.Sprintf("%x", sum)
	if cfg.ProjectKey != expected {
		t.Errorf("ProjectKey = %q, want %q", cfg.ProjectKey, expected)
	}
}

func TestDifferentPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg1, _ := Load("/path/a")
	cfg2, _ := Load("/path/b")
	if cfg1.ProjectKey == cfg2.ProjectKey {
		t.Error("different paths produced the same project key")
	}
}

func TestSavePromoteBranch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Seed global.toml with an unrelated key so we can prove the lossless
	// read-modify-write preserves it.
	cfg, _ := Load("/test/project")
	cfg.Theme = "tokyo"
	if err := SaveGlobal(cfg); err != nil {
		t.Fatal(err)
	}

	if err := SavePromoteBranch("pruebas"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PromoteBranch != "pruebas" {
		t.Errorf("PromoteBranch = %q, want pruebas", reloaded.PromoteBranch)
	}
	if reloaded.Theme != "tokyo" {
		t.Errorf("Theme lost by SavePromoteBranch: %q", reloaded.Theme)
	}

	// Clearing resets it.
	if err := SavePromoteBranch(""); err != nil {
		t.Fatal(err)
	}
	cleared, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.PromoteBranch != "" {
		t.Errorf("PromoteBranch = %q after clear, want empty", cleared.PromoteBranch)
	}
	if cleared.Theme != "tokyo" {
		t.Errorf("Theme lost by clearing promote branch: %q", cleared.Theme)
	}
}

func TestPromoteBranchProjectOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := SavePromoteBranch("global-br"); err != nil {
		t.Fatal(err)
	}
	// Hand-write a project profile with its own [promote] branch.
	root, _ := configRoot()
	projDir := filepath.Join(root, "projects")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load("/test/project")
	key := cfg.ProjectKey
	if err := os.WriteFile(filepath.Join(projDir, key+".toml"),
		[]byte("[promote]\nbranch = \"proj-br\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := Load("/test/project")
	if reloaded.PromoteBranch != "proj-br" {
		t.Errorf("PromoteBranch = %q, want proj-br (project overrides global)", reloaded.PromoteBranch)
	}
}

func TestSaveAndReload(t *testing.T) {
	// Redirect config root to a temp dir for isolation.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load("/test/project")
	cfg.Theme = "tokyo"
	cfg.Logo = "planet"
	if err := SaveGlobal(cfg); err != nil {
		t.Fatal(err)
	}

	cfg.Stage = "staging"
	cfg.OdooVersion = "19"
	if err := SaveProject(cfg); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Theme != "tokyo" {
		t.Errorf("Theme = %q, want tokyo", reloaded.Theme)
	}
	if reloaded.Logo != "planet" {
		t.Errorf("Logo = %q, want planet", reloaded.Logo)
	}
	if reloaded.Stage != "staging" {
		t.Errorf("Stage = %q, want staging", reloaded.Stage)
	}
	if reloaded.OdooVersion != "19" {
		t.Errorf("OdooVersion = %q, want 19", reloaded.OdooVersion)
	}
}

func TestConnectSectionRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load("/test/project")
	cfg.ConnectSSHHost = "deploy@erp.example.com"
	cfg.ConnectRemotePath = "/opt/odoo/erp"
	cfg.ConnectChromePath = "/usr/bin/chromium"
	if err := SaveProject(cfg); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ConnectSSHHost != "deploy@erp.example.com" {
		t.Errorf("ConnectSSHHost = %q", reloaded.ConnectSSHHost)
	}
	if reloaded.ConnectRemotePath != "/opt/odoo/erp" {
		t.Errorf("ConnectRemotePath = %q", reloaded.ConnectRemotePath)
	}
	if reloaded.ConnectChromePath != "/usr/bin/chromium" {
		t.Errorf("ConnectChromePath = %q", reloaded.ConnectChromePath)
	}
}

func TestParseRemoteProfile(t *testing.T) {
	global := []byte("compose_cmd = \"docker-compose\"\n")
	project := []byte("odoo_container = \"web\"\ndb_container = \"postgres\"\ndb_name = \"erp_prod\"\nstage = \"prod\"\nodoo_version = \"19\"\n")
	prof := ParseRemoteProfile(global, project)
	if prof.ComposeCmd != "docker-compose" {
		t.Errorf("ComposeCmd = %q", prof.ComposeCmd)
	}
	if prof.OdooVersion != "19" {
		t.Errorf("OdooVersion = %q, want 19", prof.OdooVersion)
	}
	if prof.OdooContainer != "web" {
		t.Errorf("OdooContainer = %q", prof.OdooContainer)
	}
	if prof.DBContainer != "postgres" {
		t.Errorf("DBContainer = %q", prof.DBContainer)
	}
	if prof.DBName != "erp_prod" {
		t.Errorf("DBName = %q", prof.DBName)
	}
	if prof.Stage != "prod" {
		t.Errorf("Stage = %q", prof.Stage)
	}
	// Missing global → compose falls back to a sane default.
	if got := ParseRemoteProfile(nil, project).ComposeCmd; got != "docker compose" {
		t.Errorf("fallback ComposeCmd = %q", got)
	}
	// No [checkpoint] on the server → the policy fields stay empty/zero so the
	// client can fall back to its own local config (Unit 90).
	if prof.CheckpointMode != "" || prof.CheckpointMethod != "" || prof.CheckpointKeep != 0 {
		t.Errorf("absent server [checkpoint] should be empty, got %q/%q/%d",
			prof.CheckpointMode, prof.CheckpointMethod, prof.CheckpointKeep)
	}
}

func TestParseRemoteProfileCheckpoint(t *testing.T) {
	// Server [checkpoint]: global sets mode+keep, project overrides method.
	global := []byte("[checkpoint]\nmode = \"on\"\nkeep = 5\n")
	project := []byte("db_name = \"erp\"\n[checkpoint]\nmethod = \"dump\"\n")
	prof := ParseRemoteProfile(global, project)
	if prof.CheckpointMode != "on" {
		t.Errorf("CheckpointMode = %q, want on", prof.CheckpointMode)
	}
	if prof.CheckpointMethod != "dump" {
		t.Errorf("CheckpointMethod = %q, want dump (project override)", prof.CheckpointMethod)
	}
	if prof.CheckpointKeep != 5 {
		t.Errorf("CheckpointKeep = %d, want 5 (from global)", prof.CheckpointKeep)
	}
}

func TestParseRemoteProfilePush(t *testing.T) {
	// Server [push]: global sets a path, project overrides it and adds mkdir.
	global := []byte("[push]\npath = \"build/addons\"\n")
	project := []byte("db_name = \"erp\"\n[push]\npath = \"docker/build\"\nmkdir = true\n")
	prof := ParseRemoteProfile(global, project)
	if prof.PushPath != "docker/build" {
		t.Errorf("PushPath = %q, want docker/build (project override)", prof.PushPath)
	}
	if prof.PushMkdir == nil || !*prof.PushMkdir {
		t.Errorf("PushMkdir = %v, want true", prof.PushMkdir)
	}
	// Absent server [push] → empty so the client falls back to local config.
	if bare := ParseRemoteProfile(nil, []byte("db_name = \"erp\"\n")); bare.PushPath != "" || bare.PushMkdir != nil {
		t.Errorf("absent server [push] should be empty, got %q/%v", bare.PushPath, bare.PushMkdir)
	}
}

func TestPushSectionRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load("/test/project")
	cfg.Stage = "dev"
	cfg.PushPath = "build/addons"
	if err := SaveProject(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PushPath != "build/addons" {
		t.Errorf("PushPath after round-trip = %q, want build/addons", reloaded.PushPath)
	}

	// A pure auto-detect config leaves [push] out of the profile.
	cfg2, _ := Load("/test/other")
	cfg2.Stage = "dev"
	if err := SaveProject(cfg2); err != nil {
		t.Fatal(err)
	}
	if r2, _ := Load("/test/other"); r2.PushPath != "" {
		t.Errorf("absent [push] should stay empty, got %q", r2.PushPath)
	}
}

func TestParseRemoteProfileDeployActions(t *testing.T) {
	// A project [[deploy.actions]] list replaces the global one wholesale.
	global := []byte("[[deploy.actions]]\nname = \"g\"\nphase = \"pre_push\"\nwhere = \"local\"\nrun = \"echo g\"\n")
	project := []byte("db_name = \"erp\"\n[[deploy.actions]]\nname = \"build\"\nphase = \"post_push\"\nwhere = \"remote\"\nrun = \"./build.sh\"\n")
	prof := ParseRemoteProfile(global, project)
	if len(prof.DeployActions) != 1 || prof.DeployActions[0].Name != "build" {
		t.Fatalf("DeployActions = %+v, want the project list (wholesale)", prof.DeployActions)
	}
	a := prof.DeployActions[0]
	if a.Phase != "post_push" || a.Where != "remote" || a.Run != "./build.sh" {
		t.Errorf("action = %+v", a)
	}
	if err := ValidateDeployActions(prof.DeployActions); err != nil {
		t.Errorf("decoded actions failed validation: %v", err)
	}
	// Only a global list → it stands.
	if p2 := ParseRemoteProfile(global, []byte("db_name = \"erp\"\n")); len(p2.DeployActions) != 1 || p2.DeployActions[0].Name != "g" {
		t.Errorf("global-only DeployActions = %+v, want [g]", p2.DeployActions)
	}
	// Neither → empty so the client falls back to its own local list.
	if bare := ParseRemoteProfile(nil, []byte("db_name = \"erp\"\n")); len(bare.DeployActions) != 0 {
		t.Errorf("absent [[deploy.actions]] should be empty, got %+v", bare.DeployActions)
	}
}

func TestDeployActionsExecPathRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load("/test/project")
	cfg.Stage = "dev"
	cfg.DeployActions = []DeployAction{
		{Name: "build", Phase: "post_push", Where: "remote", ExecPath: "docker", Run: "./b.sh"},
		{Name: "notify", Phase: "post_deploy", Where: "local", Run: "echo hi"}, // no exec_path
	}
	if err := SaveProject(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.DeployActions) != 2 {
		t.Fatalf("got %d actions, want 2", len(reloaded.DeployActions))
	}
	if reloaded.DeployActions[0].ExecPath != "docker" {
		t.Errorf("exec_path lost: %q", reloaded.DeployActions[0].ExecPath)
	}
	if reloaded.DeployActions[1].ExecPath != "" {
		t.Errorf("absent exec_path should stay empty, got %q", reloaded.DeployActions[1].ExecPath)
	}
}

func TestDeployPushConfig(t *testing.T) {
	// Server [deploy] push: project overrides global.
	prof := ParseRemoteProfile([]byte("[deploy]\npush = false\n"), []byte("db_name = \"erp\"\n[deploy]\npush = true\n"))
	if prof.DeployPush == nil || !*prof.DeployPush {
		t.Errorf("server DeployPush = %v, want true (project override)", prof.DeployPush)
	}
	// Absent → nil so the client falls back.
	if bare := ParseRemoteProfile(nil, []byte("db_name = \"erp\"\n")); bare.DeployPush != nil {
		t.Errorf("absent [deploy] push should be nil, got %v", bare.DeployPush)
	}
}

func TestDeployPushRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load("/test/project")
	cfg.Stage = "dev"
	tru := true
	cfg.DeployPush = &tru
	if err := SaveProject(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DeployPush == nil || !*reloaded.DeployPush {
		t.Errorf("DeployPush after round-trip = %v, want true", reloaded.DeployPush)
	}
	// Absent → stays nil.
	cfg2, _ := Load("/test/other")
	cfg2.Stage = "dev"
	if err := SaveProject(cfg2); err != nil {
		t.Fatal(err)
	}
	if r2, _ := Load("/test/other"); r2.DeployPush != nil {
		t.Errorf("absent [deploy] push should stay nil, got %v", r2.DeployPush)
	}
}

func TestDeployTestConfig(t *testing.T) {
	// Server [deploy] test + test_modules: project overrides global.
	prof := ParseRemoteProfile(
		[]byte("[deploy]\ntest = false\ntest_modules = [\"a\"]\n"),
		[]byte("db_name = \"erp\"\n[deploy]\ntest = true\ntest_modules = [\"sale\",\"stock\"]\n"))
	if prof.DeployTest == nil || !*prof.DeployTest {
		t.Errorf("server DeployTest = %v, want true (project override)", prof.DeployTest)
	}
	if !reflect.DeepEqual(prof.DeployTestModules, []string{"sale", "stock"}) {
		t.Errorf("server DeployTestModules = %v, want [sale stock] (project wholesale)", prof.DeployTestModules)
	}
	// Absent → nil/empty so the client falls back.
	bare := ParseRemoteProfile(nil, []byte("db_name = \"erp\"\n"))
	if bare.DeployTest != nil || len(bare.DeployTestModules) != 0 {
		t.Errorf("absent [deploy] test should be nil/empty, got %v / %v", bare.DeployTest, bare.DeployTestModules)
	}
}

func TestDeployTestRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load("/test/project")
	cfg.Stage = "dev"
	tru := true
	cfg.DeployTest = &tru
	cfg.DeployTestModules = []string{"sale", "stock"}
	if err := SaveProject(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DeployTest == nil || !*reloaded.DeployTest {
		t.Errorf("DeployTest after round-trip = %v, want true", reloaded.DeployTest)
	}
	if !reflect.DeepEqual(reloaded.DeployTestModules, []string{"sale", "stock"}) {
		t.Errorf("DeployTestModules after round-trip = %v, want [sale stock]", reloaded.DeployTestModules)
	}
}

func TestWithDeployActions(t *testing.T) {
	// Splicing actions into an existing profile preserves other keys.
	existing := []byte("db_name = \"erp\"\nstage = \"prod\"\n[connect]\nssh_host = \"h\"\n")
	actions := []DeployAction{{Name: "build", Phase: "post_push", Where: "remote", ExecPath: "docker", Run: "./b.sh"}}
	out, err := WithDeployActions(existing, actions)
	if err != nil {
		t.Fatal(err)
	}
	var p projectFile
	if err := toml.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	if p.DBName != "erp" || p.Stage != "prod" || p.Connect == nil || p.Connect.SSHHost != "h" {
		t.Errorf("unrelated keys not preserved: %+v", p)
	}
	if p.Deploy == nil || len(p.Deploy.Actions) != 1 || p.Deploy.Actions[0].ExecPath != "docker" {
		t.Errorf("actions not spliced: %+v", p.Deploy)
	}
	// Empty actions removes the section.
	out2, _ := WithDeployActions(out, nil)
	var p2 projectFile
	_ = toml.Unmarshal(out2, &p2)
	if p2.Deploy != nil {
		t.Errorf("empty actions should drop [deploy], got %+v", p2.Deploy)
	}
	if p2.DBName != "erp" {
		t.Error("other keys should survive the removal")
	}
}

func TestConnectSectionAbsentByDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load("/test/project")
	cfg.Stage = "dev"
	if err := SaveProject(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load("/test/project")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ConnectSSHHost != "" || reloaded.ConnectRemotePath != "" {
		t.Errorf("expected empty connect config, got ssh=%q path=%q",
			reloaded.ConnectSSHHost, reloaded.ConnectRemotePath)
	}
}

func TestNoFilesInProject(t *testing.T) {
	projectPath := "/test/my/odoo/project"
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, _ := Load(projectPath)
	cfg.Stage = "prod"
	_ = SaveProject(cfg)

	entries, _ := filepath.Glob(filepath.Join(projectPath, "*"))
	if len(entries) > 0 {
		t.Errorf("found files in project dir: %v", entries)
	}

	configFiles, _ := filepath.Glob(filepath.Join(tmp, ".config", "echo", "projects", "*"))
	if len(configFiles) == 0 {
		t.Error("expected project toml in ~/.config/echo/projects/, found none")
	}
	_ = os.RemoveAll(tmp)
}
