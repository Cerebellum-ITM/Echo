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
	project := []byte("odoo_container = \"web\"\ndb_container = \"postgres\"\ndb_name = \"erp_prod\"\nstage = \"prod\"\n")
	prof := ParseRemoteProfile(global, project)
	if prof.ComposeCmd != "docker-compose" {
		t.Errorf("ComposeCmd = %q", prof.ComposeCmd)
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
