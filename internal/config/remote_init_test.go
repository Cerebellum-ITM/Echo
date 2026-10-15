package config

import (
	"errors"
	"strings"
	"testing"
)

var initFields = RemoteInitFields{
	OdooVersion:   "18",
	Stage:         "staging",
	OdooContainer: "web",
	DBContainer:   "db",
	DBName:        "stg",
	ProjectPath:   "/srv/acme",
}

func TestRenderRemoteProjectFresh(t *testing.T) {
	out, err := RenderRemoteProject(nil, initFields)
	if err != nil {
		t.Fatal(err)
	}
	prof, err := ParseRemoteProfile(nil, out)
	if err != nil {
		t.Fatal(err)
	}
	if prof.OdooVersion != "18" || prof.Stage != "staging" || prof.OdooContainer != "web" ||
		prof.DBContainer != "db" || prof.DBName != "stg" {
		t.Fatalf("profile = %+v", prof)
	}
	info, err := ParseProjectInfo(out)
	if err != nil || info.ProjectPath != "/srv/acme" {
		t.Fatalf("project_path = %q, %v", info.ProjectPath, err)
	}
}

func TestRenderRemoteProjectKeepsOtherSections(t *testing.T) {
	existing := []byte(`stage = "prod"
db_name = "old"
addons_paths = ["addons"]
conf_path = "/etc/odoo/odoo.conf"

[push]
path = "/srv/acme/overlay"

[checkpoint]
mode = "always"
keep = 3

[[deploy.actions]]
name = "build"
phase = "post_push"
where = "remote"
run = "./build.sh"
`)
	out, err := RenderRemoteProject(existing, initFields)
	if err != nil {
		t.Fatal(err)
	}
	prof, err := ParseRemoteProfile(nil, out)
	if err != nil {
		t.Fatal(err)
	}
	if prof.Stage != "staging" || prof.DBName != "stg" {
		t.Errorf("init fields not applied: %+v", prof)
	}
	if prof.PushPath != "/srv/acme/overlay" || prof.CheckpointMode != "always" || prof.CheckpointKeep != 3 ||
		prof.ConfPath != "/etc/odoo/odoo.conf" || len(prof.AddonsPaths) != 1 {
		t.Errorf("sections lost: %+v", prof)
	}
	if len(prof.DeployActions) != 1 || prof.DeployActions[0].Name != "build" {
		t.Errorf("deploy actions lost: %+v", prof.DeployActions)
	}
}

func TestRenderRemoteProjectRefusals(t *testing.T) {
	_, err := RenderRemoteProject([]byte("stage = \"dev\"\n[reverb]\nenv_id = 4\n"), initFields)
	if !errors.Is(err, ErrReverbProfile) {
		t.Errorf("reverb marker: err = %v", err)
	}

	_, err = RenderRemoteProject([]byte("stage = \"dev\"\nfuture_key = 1\n[deploy]\nlater = true\n"), initFields)
	var uerr *UnknownKeysError
	if !errors.As(err, &uerr) {
		t.Fatalf("unknown keys: err = %v", err)
	}
	if got := strings.Join(uerr.Keys, ","); got != "deploy.later,future_key" {
		t.Errorf("keys = %s", got)
	}

	_, err = RenderRemoteProject([]byte("stage = \n"), initFields)
	var perr *ParseError
	if !errors.As(err, &perr) {
		t.Errorf("broken toml: err = %v", err)
	}
}

func TestDeclaredComposeCmd(t *testing.T) {
	if got, _ := DeclaredComposeCmd(nil); got != "" {
		t.Errorf("empty global = %q", got)
	}
	if got, _ := DeclaredComposeCmd([]byte("compose_cmd = \"docker-compose\"\n")); got != "docker-compose" {
		t.Errorf("declared = %q", got)
	}
	if got, _ := DeclaredComposeCmd(RenderRemoteGlobalCompose("docker-compose")); got != "docker-compose" {
		t.Errorf("rendered round trip = %q", got)
	}
}
