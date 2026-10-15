package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

// fakeInitHost scripts initRunSSH: shell commands run locally with the
// remote `~/` mapped to remoteHome, docker probes get canned answers.
type fakeInitHost struct {
	dir        string
	remoteHome string
	compose    string
	calls      []string
}

func newFakeInitHost(t *testing.T) *fakeInitHost {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "docker-compose.yml"), "services: {}\n")
	mustWrite(t, filepath.Join(dir, ".env"), "POSTGRES_USER=odoo\n")
	f := &fakeInitHost{dir: dir, remoteHome: t.TempDir(), compose: "v2"}
	prevSSH, prevTTY := initRunSSH, stdinIsTTY
	t.Cleanup(func() { initRunSSH, stdinIsTTY = prevSSH, prevTTY })
	stdinIsTTY = func() bool { return false }
	initRunSSH = f.run
	return f
}

func (f *fakeInitHost) run(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, remoteCmd)
	switch {
	case strings.Contains(remoteCmd, "docker compose version"):
		return []byte(f.compose + "\n"), nil
	case strings.Contains(remoteCmd, "config --services"):
		return []byte("web\ndb\n"), nil
	case strings.Contains(remoteCmd, "ps --status=running"):
		return []byte("web\tacme-web-1\n"), nil
	case strings.Contains(remoteCmd, "'psql'"):
		return []byte(" stg       | odoo | UTF8\n postgres  | odoo | UTF8\n template0 | odoo | UTF8\n"), nil
	case strings.Contains(remoteCmd, "'--version'"):
		return []byte("Odoo Server 17.0\n"), nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", strings.ReplaceAll(remoteCmd, "~/", f.remoteHome+"/"))
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Output()
}

func (f *fakeInitHost) profilePath() string {
	return filepath.Join(f.remoteHome, ".config/echo/projects", config.ProjectKey(f.dir)+".toml")
}

func (f *fakeInitHost) globalPath() string {
	return filepath.Join(f.remoteHome, ".config/echo/global.toml")
}

func (f *fakeInitHost) runInit(t *testing.T, args ...string) (InitRemoteResult, error) {
	t.Helper()
	return RunInitRemote(context.Background(), InitRemoteOpts{Cfg: &config.Config{}, Root: t.TempDir(), Args: args})
}

func TestInitRemoteHeadlessWritesProfile(t *testing.T) {
	f := newFakeInitHost(t)
	res, err := f.runInit(t, "--remote", "--host", "acme", "--path", f.dir+"/", "--stage", "staging", "--name", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != f.dir || res.Key != config.ProjectKey(f.dir) || res.Updated {
		t.Errorf("result = %+v", res)
	}
	data, err := os.ReadFile(f.profilePath())
	if err != nil {
		t.Fatalf("profile not at the key path: %v", err)
	}
	prof, err := config.ParseRemoteProfile(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	if prof.Stage != "staging" || prof.OdooVersion != "17" || prof.OdooContainer != "web" ||
		prof.DBContainer != "db" || prof.DBName != "stg" {
		t.Errorf("profile = %+v", prof)
	}
	info, _ := config.ParseProjectInfo(data)
	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if len(g.ConnectTargets) != 1 || g.ConnectTargets[0].RemotePath != info.ProjectPath || info.ProjectPath != f.dir {
		t.Errorf("target %+v vs project_path %q", g.ConnectTargets, info.ProjectPath)
	}
	if _, err := os.Stat(f.globalPath()); !os.IsNotExist(err) {
		t.Errorf("remote global.toml written with compose v2: %v", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, " up") {
			t.Errorf("init started the stack: %s", c)
		}
	}
}

func TestInitRemoteComposeV1(t *testing.T) {
	f := newFakeInitHost(t)
	f.compose = "v1"
	if _, err := f.runInit(t, "--remote", "--host", "acme", "--path", f.dir, "--stage", "dev"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f.globalPath()); string(got) != "compose_cmd = \"docker-compose\"\n" {
		t.Errorf("global = %q", got)
	}

	g := newFakeInitHost(t)
	g.compose = "v1"
	mustWrite(t, g.globalPath(), "theme = \"dark\"\n")
	_, err := g.runInit(t, "--remote", "--host", "acme", "--path", g.dir, "--stage", "dev")
	if err == nil || !strings.Contains(err.Error(), "compose_cmd") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(g.globalPath()); string(got) != "theme = \"dark\"\n" {
		t.Errorf("existing global rewritten: %q", got)
	}
	if _, err := os.Stat(g.profilePath()); !os.IsNotExist(err) {
		t.Errorf("profile written after a compose failure")
	}
}

func TestInitRemoteEditsInPlace(t *testing.T) {
	f := newFakeInitHost(t)
	mustWrite(t, f.profilePath(), "stage = \"dev\"\ndb_name = \"old\"\n[push]\npath = \"/srv/overlay\"\n")
	res, err := f.runInit(t, "--remote", "--host", "acme", "--path", f.dir, "--stage", "staging", "--db", "stg")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated {
		t.Error("existing profile not reported as updated")
	}
	data, _ := os.ReadFile(f.profilePath())
	prof, _ := config.ParseRemoteProfile(nil, data)
	if prof.Stage != "staging" || prof.DBName != "stg" || prof.PushPath != "/srv/overlay" {
		t.Errorf("profile = %+v", prof)
	}
}

func TestInitRemoteStageDowngradeGate(t *testing.T) {
	for _, existing := range []string{"stage = \"prod\"\n", "db_name = \"stg\"\n"} {
		f := newFakeInitHost(t)
		mustWrite(t, f.profilePath(), existing)
		_, err := f.runInit(t, "--remote", "--host", "acme", "--path", f.dir, "--stage", "dev")
		if !errors.Is(err, ErrNonInteractive) {
			t.Fatalf("%q: err = %v", existing, err)
		}
		if got, _ := os.ReadFile(f.profilePath()); string(got) != existing {
			t.Errorf("%q: profile rewritten without a confirm: %q", existing, got)
		}
		if _, err := f.runInit(t, "--remote", "--host", "acme", "--path", f.dir, "--stage", "dev", "--force"); err != nil {
			t.Fatalf("%q with --force: %v", existing, err)
		}
		data, _ := os.ReadFile(f.profilePath())
		if prof, _ := config.ParseRemoteProfile(nil, data); prof.Stage != "dev" {
			t.Errorf("%q with --force: stage = %q", existing, prof.Stage)
		}
	}
}

func TestInitRemoteRefusesReverbProfile(t *testing.T) {
	f := newFakeInitHost(t)
	mustWrite(t, f.profilePath(), "stage = \"dev\"\n[reverb]\nenv_id = 3\n")
	_, err := f.runInit(t, "--remote", "--host", "acme", "--path", f.dir, "--stage", "dev")
	if !errors.Is(err, config.ErrReverbProfile) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseInitRemoteArgs(t *testing.T) {
	prev := stdinIsTTY
	t.Cleanup(func() { stdinIsTTY = prev })
	stdinIsTTY = func() bool { return false }

	usage := [][]string{
		{"--host", "a", "--stage", "dev"},
		{"--remote", "--from", "x", "--stage", "dev"},
		{"--from", "x", "--name", "y", "--stage", "dev"},
		{"--remote", "--host", "a", "--path", "/p", "--stage", "dev", "--bogus"},
		{"--remote", "--host", "a", "--path", "/p", "--stage", "dev", "extra"},
		{"--remote", "--host", "a", "--path", "/p", "--stage", "qa"},
		{"--remote", "--host", "a", "--path", "/p", "--stage", "dev", "--odoo-version", "16"},
		{"--remote", "--host"},
	}
	for _, args := range usage {
		if _, err := parseInitRemoteArgs(args); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: err = %v, want ErrUsage", args, err)
		}
	}

	p, err := parseInitRemoteArgs([]string{"--from", "prod", "--stage", "dev"})
	if err != nil || p.from != "prod" || p.stage != "dev" {
		t.Errorf("--from prod --stage dev: %+v, %v", p, err)
	}
	if p, err := parseInitRemoteArgs([]string{"--from=prod", "--stage=staging"}); err != nil || p.from != "prod" || p.stage != "staging" {
		t.Errorf("inline values: %+v, %v", p, err)
	}

	_, err = parseInitRemoteArgs([]string{"--remote", "--host", "a", "--path", "/p"})
	if !errors.Is(err, ErrNonInteractive) || !strings.Contains(err.Error(), "--stage") {
		t.Errorf("missing --stage: err = %v", err)
	}
}
