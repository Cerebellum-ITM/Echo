package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
