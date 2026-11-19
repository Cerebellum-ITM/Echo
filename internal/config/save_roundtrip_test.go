package config

import (
	"os"
	"path/filepath"
	"strings"
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

	g := mustLoadGlobalFile(t, path)
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

	g := mustLoadGlobalFile(t, path)
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

	if g := mustLoadGlobalFile(t, path); g.Prompt != nil {
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

func TestParseRemoteProfileReverbMarker(t *testing.T) {
	const profile = `odoo_container = "odoo"
db_container = "reverb-iza-db"
db_name = "iza_staging"

[reverb]
env_id = 116
project = "iza"
env = "staging"
api_url = "http://reverb.local:8080"
`
	prof := mustParseRemote(t, nil, []byte(profile))
	if prof.Reverb == nil {
		t.Fatal("[reverb] marker not parsed")
	}
	if prof.Reverb.EnvID != 116 || prof.Reverb.Project != "iza" ||
		prof.Reverb.Env != "staging" || prof.Reverb.APIURL != "http://reverb.local:8080" {
		t.Errorf("marker = %+v", prof.Reverb)
	}
	if bare := mustParseRemote(t, nil, []byte("db_name = \"iza\"\n")); bare.Reverb != nil {
		t.Errorf("marker invented on a classic profile: %+v", bare.Reverb)
	}
}

func mustLoadGlobalFile(t *testing.T, path string) globalFile {
	t.Helper()
	g, err := loadGlobalFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRelayTargetRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeGlobal(t, globalWithEveryTable+`
[relay]
url = "https://relay.example.test"
token_file = "~/.config/relay/echo-token"
`)

	if err := SaveConnectTarget(ConnectTarget{
		Name: "fb-stage", SSHHost: "morwi-fb", RemotePath: RelayPath("fb/stage"), Relay: "fb/stage", DBName: "fuentebuena",
	}); err != nil {
		t.Fatal(err)
	}

	g := mustLoadGlobalFile(t, path)
	if g.Relay == nil || g.Relay.URL != "https://relay.example.test" || g.Relay.TokenFile != "~/.config/relay/echo-token" {
		t.Errorf("[relay] lost or altered: %+v", g.Relay)
	}
	if f := g.ConnectTargets["fb-stage"]; f == nil || f.Relay != "fb/stage" || f.RemotePath != "" {
		t.Errorf("fb-stage stored as %+v, want relay = fb/stage and no remote_path", f)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "relay = "); n != 1 {
		t.Errorf("the relay key appears %d times, want only on fb-stage:\n%s", n, raw)
	}

	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RelayURL != "https://relay.example.test" || cfg.RelayTokenFile != "~/.config/relay/echo-token" {
		t.Errorf("relay config = %q %q", cfg.RelayURL, cfg.RelayTokenFile)
	}
	for _, ct := range cfg.ConnectTargets {
		spec, isRelay := RelaySpec(ct.RemotePath)
		switch ct.Name {
		case "fb-stage":
			if !isRelay || spec != "fb/stage" || ct.Relay != "fb/stage" {
				t.Errorf("fb-stage loaded as %+v", ct)
			}
		default:
			if isRelay || ct.Relay != "" {
				t.Errorf("%s loaded as a relay target: %+v", ct.Name, ct)
			}
		}
	}
}

func TestRelaySSHHosts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeGlobal(t, globalWithEveryTable+`
[relay]
url = "https://relay.example.test"
token_file = "~/.config/relay/echo-token"

[relay.ssh_hosts]
morwi-fb = "morwi-fb-2222"
`)
	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RelaySSHHosts["morwi-fb"] != "morwi-fb-2222" {
		t.Fatalf("RelaySSHHosts = %v", cfg.RelaySSHHosts)
	}

	saved, err := SaveRelaySSHHost("morwi-habitta", "habitta-ssh")
	if err != nil || !saved {
		t.Fatalf("SaveRelaySSHHost = %v, %v; want a write", saved, err)
	}
	saved, err = SaveRelaySSHHost("morwi-fb", "other-alias")
	if err != nil || saved {
		t.Fatalf("SaveRelaySSHHost over an entry = %v, %v; want no write", saved, err)
	}
	g := mustLoadGlobalFile(t, path)
	want := map[string]string{"morwi-fb": "morwi-fb-2222", "morwi-habitta": "habitta-ssh"}
	if len(g.Relay.SSHHosts) != len(want) || g.Relay.SSHHosts["morwi-fb"] != want["morwi-fb"] ||
		g.Relay.SSHHosts["morwi-habitta"] != want["morwi-habitta"] {
		t.Errorf("ssh_hosts = %v, want %v", g.Relay.SSHHosts, want)
	}
	if g.Relay.URL != "https://relay.example.test" || g.Relay.TokenFile != "~/.config/relay/echo-token" {
		t.Errorf("[relay] altered: %+v", g.Relay)
	}

	before := sshHostsTable(t, path)
	if err := SaveConnectTarget(ConnectTarget{Name: "other", SSHHost: "Ionos", RemotePath: "/srv/odoo"}); err != nil {
		t.Fatal(err)
	}
	if after := sshHostsTable(t, path); after != before {
		t.Errorf("a connect-target save changed [relay.ssh_hosts]:\n%s\nwant\n%s", after, before)
	}
}

// sshHostsTable is the [relay.ssh_hosts] table of the file at path, as text.
func sshHostsTable(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, table, found := strings.Cut(string(raw), "[relay.ssh_hosts]")
	if !found {
		t.Fatalf("no [relay.ssh_hosts] in:\n%s", raw)
	}
	if i := strings.Index(table, "\n["); i >= 0 {
		table = table[:i]
	}
	return table
}
