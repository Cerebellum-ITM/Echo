package config

import (
	"os"
	"path/filepath"
	"testing"
)

// globalWithEveryTable is a global.toml carrying every section the Config
// does not own, with distinctive values, plus one connect target.
const globalWithEveryTable = `theme = "tokyo"
icons = "nerd"

[reverb]
url = "http://reverb.local:8080"
token = "s3cr3t"

[checkpoint]
mode = "always"
method = "sql"
keep = 7

[push]
path = "/srv/overlay"
mkdir = true

[promote]
branch = "echo/promote"
base = "origin/main"

[deploy]
push = true

[[deploy.actions]]
name = "restart"
phase = "post"
where = "remote"
run = "docker compose restart odoo"

[connect_targets.iza-staging]
ssh_host = "Ionos"
remote_path = "/srv/iza/staging"
`

func writeGlobal(t *testing.T, body string) string {
	t.Helper()
	root, err := configRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "global.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveConnectTargetKeepsEverySection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeGlobal(t, globalWithEveryTable)

	if err := SaveConnectTarget(ConnectTarget{
		Name: "morwi-dev", SSHHost: "Ionos", RemotePath: "/srv/morwi/dev",
	}); err != nil {
		t.Fatal(err)
	}

	g := loadGlobalFile(path)
	if g.Reverb == nil || g.Reverb.Token != "s3cr3t" || g.Reverb.URL != "http://reverb.local:8080" {
		t.Errorf("[reverb] lost or altered: %+v", g.Reverb)
	}
	if g.Checkpoint == nil || g.Checkpoint.Mode != "always" || g.Checkpoint.Keep != 7 {
		t.Errorf("[checkpoint] lost or altered: %+v", g.Checkpoint)
	}
	if g.Push == nil || g.Push.Path != "/srv/overlay" {
		t.Errorf("[push] lost or altered: %+v", g.Push)
	}
	if g.Promote == nil || g.Promote.Branch != "echo/promote" || g.Promote.Base != "origin/main" {
		t.Errorf("[promote] lost or altered: %+v", g.Promote)
	}
	if g.Deploy == nil || len(g.Deploy.Actions) != 1 || g.Deploy.Actions[0].Name != "restart" {
		t.Errorf("[deploy] lost or altered: %+v", g.Deploy)
	}
	if g.Icons != "nerd" {
		t.Errorf("icons = %q, want nerd", g.Icons)
	}
	if len(g.ConnectTargets) != 2 || g.ConnectTargets["morwi-dev"] == nil {
		t.Errorf("connect targets = %v, want the original plus morwi-dev", g.ConnectTargets)
	}
}

func TestSaveGlobalKeepsReverbToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeGlobal(t, globalWithEveryTable)

	cfg, _ := Load("/test/project")
	cfg.Theme = "gruvbox"
	if err := SaveGlobal(cfg); err != nil {
		t.Fatal(err)
	}

	g := loadGlobalFile(path)
	if g.Theme != "gruvbox" {
		t.Errorf("Theme = %q, want gruvbox", g.Theme)
	}
	if g.Reverb == nil || g.Reverb.Token != "s3cr3t" {
		t.Errorf("[reverb] token lost by a theme save: %+v", g.Reverb)
	}
}

func TestSaveGlobalClearsDefaultPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeGlobal(t, "theme = \"tokyo\"\n\n[prompt]\nname_max = 12\n")

	cfg, _ := Load("/test/project")
	cfg.PromptNameMax = 0
	cfg.PromptSegments = nil
	cfg.HealthTTL = 0
	if err := SaveGlobal(cfg); err != nil {
		t.Fatal(err)
	}

	if g := loadGlobalFile(path); g.Prompt != nil {
		t.Errorf("[prompt] kept after returning to defaults: %+v", g.Prompt)
	}
}

func TestSaveProjectKeepsPromoteAndGitDeploy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, _ := configRoot()
	projDir := filepath.Join(root, "projects")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load("/test/project")
	path := filepath.Join(projDir, cfg.ProjectKey+".toml")
	if err := os.WriteFile(path, []byte(`db_name = "iza"

[promote]
branch = "echo/deploy"

[connect]
ssh_host = "Ionos"
remote_path = "/srv/iza"
git_deploy = true
git_branch = "echo/deploy"
git_path = "src"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded, _ := Load("/test/project")
	if !reloaded.ConnectGitDeploy {
		t.Fatal("fixture did not load: ConnectGitDeploy false")
	}
	reloaded.OdooContainer = "odoo"
	if err := SaveProject(reloaded); err != nil {
		t.Fatal(err)
	}

	after, _ := Load("/test/project")
	if after.OdooContainer != "odoo" {
		t.Errorf("OdooContainer = %q, want odoo", after.OdooContainer)
	}
	if after.PromoteBranch != "echo/deploy" || after.PromoteBranchSource != "project" {
		t.Errorf("project [promote] lost: %q from %q", after.PromoteBranch, after.PromoteBranchSource)
	}
	if !after.ConnectGitDeploy || after.ConnectGitBranch != "echo/deploy" || after.ConnectGitPath != "src" {
		t.Errorf("git-deploy topology lost: %+v", after)
	}
}
