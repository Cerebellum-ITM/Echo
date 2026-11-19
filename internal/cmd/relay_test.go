package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/relay"
)

const relayResolveBody = `{"app":"fb","env":"stage","stage":"staging","branch":"stage","odoo_version":"19.0",
	"server":"morwi-fb","ssh":{"host":"54.151.87.198","port":22},"compose_project":"relay-fb-stage",
	"containers":{"odoo":"relay-fb-stage-odoo","db":"relay-fb-stage-db"},"db":{"name":"fuentebuena","user":"odoo"},
	"odoo_conf":"/etc/odoo/odoo.conf","release":{"deploy_id":11,"app_sha":"165297d159028635cfad56c6d54978c12308b63f"}}`

// fakeRelay serves resolve and the deploy routes. deployStates are what
// successive reads of the deploy return.
type fakeRelay struct {
	*httptest.Server
	mu           sync.Mutex
	created      []relay.DeployRequest
	deployStates []string
}

func newFakeRelay(t *testing.T, deployStates ...string) *fakeRelay {
	t.Helper()
	f := &fakeRelay{deployStates: deployStates}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rlt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"unauthorized","message":"invalid or revoked token"}}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/resolve/fb/stage":
			_, _ = io.WriteString(w, relayResolveBody)
		case r.URL.Path == "/api/v1/resolve/fb/qa":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"unknown_env","message":"unknown env \"qa\" in app \"fb\""}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/deploys":
			var req relay.DeployRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.created = append(f.created, req)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":12,"state":"queued","ref":"refs/heads/stage"}`)
		case r.URL.Path == "/api/v1/deploys/12":
			state := f.deployStates[0]
			if len(f.deployStates) > 1 {
				f.deployStates = f.deployStates[1:]
			}
			_ = json.NewEncoder(w).Encode(relay.Deploy{ID: 12, State: state, Ref: "refs/heads/stage", AppSHA: "abc1234def",
				Error: map[bool]string{true: "upgrade failed"}[state == relay.StateRolledBack],
				Steps: []relay.Step{{Seq: 1, Name: "build", State: "succeeded"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// relayTestEnv isolates HOME, puts a recording ssh first on PATH and returns
// a config with one Relay target (fb-stage) and one classic target.
func relayTestEnv(t *testing.T, relayURL string) (*config.Config, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	mustWrite(t, filepath.Join(bin, "ssh"), `#!/bin/sh
while [ "$1" = "-o" ] || [ "$1" = "-tt" ]; do
  if [ "$1" = "-o" ]; then shift 2; else shift; fi
done
host="$1"; shift
cmd="$*"
printf '%s\t%s\n' "$host" "$cmd" >> "`+logPath+`"
case "$cmd" in
  *"ps' '--format' 'json'"*) printf '{"Service":"odoo","Name":"relay-fb-stage-odoo","State":"running","Status":"Up 5 minutes"}\n' ;;
  *"'--log-level=warn'"*) cat >/dev/null; printf '@@ECHO_PROBE@@ 1 1 eyJvayI6IHRydWUsICJraW5kIjogImV2YWwiLCAicmVzdWx0IjogMywgInN0ZG91dCI6ICIifQ==\n' ;;
  *"'odoo' 'shell'"*) cat >/dev/null; printf '19.0.4.1.1\n' ;;
esac
`)
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{
		RelayURL:   relayURL,
		RelayToken: "rlt_test",
		ConnectTargets: []config.ConnectTarget{
			{Name: "fb-stage", SSHHost: "morwi-fb", RemotePath: config.RelayPath("fb/stage"), Relay: "fb/stage"},
			{Name: "classic", SSHHost: "Ionos", RemotePath: "/srv/odoo"},
		},
	}
	return cfg, logPath
}

func sshLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestRelayGuard(t *testing.T) {
	cfg := &config.Config{ConnectTargets: []config.ConnectTarget{
		{Name: "fb-stage", SSHHost: "morwi-fb", RemotePath: config.RelayPath("fb/stage"), Relay: "fb/stage"},
		{Name: "classic", SSHHost: "Ionos", RemotePath: "/srv/odoo"},
	}}
	bound := *cfg
	bound.ConnectSSHHost, bound.ConnectRemotePath = "morwi-fb", config.RelayPath("fb/stage")

	tests := []struct {
		name    string
		cfg     *config.Config
		command string
		args    []string
		refused bool
	}{
		{"supported verb", cfg, "shell-run", []string{"--from", "fb-stage", "x.py"}, false},
		{"deploy is delegated", cfg, "deploy", []string{"--from", "fb-stage"}, false},
		{"uninstall runs in the container", cfg, "uninstall", []string{"--from", "fb-stage", "sale"}, false},
		{"probe on the env", cfg, "probe", []string{"-c", "1", "--from", "fb-stage"}, false},
		{"probe --remote through the binding", &bound, "probe", []string{"-c", "1", "--remote"}, false},
		{"doctor on the env", cfg, "doctor", []string{"--from", "fb-stage"}, false},
		{"doctor through the binding", &bound, "doctor", nil, false},
		{"push refused", cfg, "push", []string{"--from=fb-stage"}, true},
		{"restart refused", cfg, "restart", []string{"--from", "fb-stage"}, true},
		{"db-admin refused", cfg, "db-admin", []string{"--from", "fb-stage"}, true},
		{"push through the binding", &bound, "push", nil, true},
		{"--remote through the binding", &bound, "stop", []string{"--remote"}, true},
		{"local verb in a bound directory", &bound, "stop", nil, false},
		{"classic target untouched", cfg, "push", []string{"--from", "classic"}, false},
		{"unknown target left to the command", cfg, "push", []string{"--from", "nope"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RelayGuard(tt.cfg, tt.command, tt.args)
			if (err != nil) != tt.refused {
				t.Fatalf("RelayGuard = %v, refused want %v", err, tt.refused)
			}
			if err != nil && (!errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "fb/stage")) {
				t.Errorf("err = %v, want a usage error naming the env", err)
			}
		})
	}
}

func TestRelayShellRunRunsInTheContainer(t *testing.T) {
	f := newFakeRelay(t)
	cfg, logPath := relayTestEnv(t, f.URL)
	var out []string
	err := runShellScriptRemote(context.Background(), ShellScriptOpts{
		Cfg: cfg, From: "fb-stage", Stdin: strings.NewReader("print(1)\n"),
		StreamOut: func(l string) { out = append(out, l) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "19.0.4.1.1" {
		t.Errorf("output = %q", out)
	}
	want := "morwi-fb\tdocker exec -i 'relay-fb-stage-odoo' 'odoo' 'shell' '-d' 'fuentebuena' '--db_user' 'odoo' '--no-http'"
	if got := sshLog(t, logPath); len(got) != 1 || got[0] != want {
		t.Errorf("ssh ran %q\nwant %q", got, want)
	}
}

func TestRelayProbeRunsInTheContainer(t *testing.T) {
	f := newFakeRelay(t)
	cfg, logPath := relayTestEnv(t, f.URL)
	res, err := RunProbe(context.Background(), ProbeOpts{Cfg: cfg, Args: []string{"-c", "1+2", "--from", "fb-stage"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Result) != "3" || res.DB != "fuentebuena" || res.Target != "fb-stage" {
		t.Errorf("result = %+v", res)
	}
	want := "morwi-fb\tdocker exec -i 'relay-fb-stage-odoo' 'odoo' 'shell' '-d' 'fuentebuena' '--db_user' 'odoo' '--no-http' '--log-level=warn'"
	if got := sshLog(t, logPath); len(got) != 1 || got[0] != want {
		t.Errorf("ssh ran %q\nwant %q", got, want)
	}
}

func TestRelayPSAndLogs(t *testing.T) {
	f := newFakeRelay(t)
	cfg, logPath := relayTestEnv(t, f.URL)
	var rows []docker.PSContainer
	err := runRemotePS(context.Background(), DockerOpts{Cfg: cfg}, "fb-stage",
		func(r []docker.PSContainer, db string) { rows = r })
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Service != "relay-fb-stage-odoo" {
		t.Errorf("rows = %+v", rows)
	}
	if err := runRemoteLogs(context.Background(), DockerOpts{Cfg: cfg}, "fb-stage", false, false, false, "5", nil); err != nil {
		t.Fatal(err)
	}
	if err := runRemoteLogs(context.Background(), DockerOpts{Cfg: cfg}, "fb-stage", true, false, false, "", []string{"db"}); err != nil {
		t.Fatal(err)
	}
	if err := runRemoteLogs(context.Background(), DockerOpts{Cfg: cfg}, "fb-stage", false, false, true, "", nil); err != nil {
		t.Fatal(err)
	}
	err = runRemoteLogs(context.Background(), DockerOpts{Cfg: cfg}, "fb-stage", false, false, false, "", []string{"odoo", "db"})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("two containers: err = %v, want usage", err)
	}
	want := []string{
		"morwi-fb\tdocker compose -p 'relay-fb-stage' 'ps' '--format' 'json'",
		"morwi-fb\tdocker 'logs' '--tail' '5' 'relay-fb-stage-odoo'",
		"morwi-fb\tdocker 'logs' '-f' 'relay-fb-stage-db'",
		"morwi-fb\tdocker compose -p 'relay-fb-stage' 'logs' '--no-log-prefix'",
	}
	if got := sshLog(t, logPath); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ssh ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDeployOnRelayIsDelegated(t *testing.T) {
	relayDeployPoll = time.Millisecond
	t.Cleanup(func() { relayDeployPoll = 2 * time.Second })

	t.Run("followed to success", func(t *testing.T) {
		f := newFakeRelay(t, "building", "succeeded")
		cfg, _ := relayTestEnv(t, f.URL)
		res, err := RunDeploy(context.Background(), DeployOpts{Cfg: cfg, Args: []string{"--from", "fb-stage", "--dry-run"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 1 || f.created[0] != (relay.DeployRequest{App: "fb", Env: "stage", DryRun: true}) {
			t.Errorf("created %+v", f.created)
		}
		if res.DB != "fuentebuena" || res.Ref != "refs/heads/stage" || !res.Planned {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("a deploy that rolled back fails", func(t *testing.T) {
		f := newFakeRelay(t, "upgrading", "rolled_back")
		cfg, _ := relayTestEnv(t, f.URL)
		_, err := RunDeploy(context.Background(), DeployOpts{Cfg: cfg, Args: []string{"--from", "fb-stage"}})
		if err == nil || !strings.Contains(err.Error(), "rolled_back") || !strings.Contains(err.Error(), "upgrade failed") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an Echo deploy flag is refused before anything is queued", func(t *testing.T) {
		f := newFakeRelay(t, "succeeded")
		cfg, _ := relayTestEnv(t, f.URL)
		_, err := RunDeploy(context.Background(), DeployOpts{Cfg: cfg, Args: []string{"--from", "fb-stage", "--modules", "sale"}})
		if !errors.Is(err, ErrUsage) || len(f.created) != 0 {
			t.Errorf("err = %v, created %d", err, len(f.created))
		}
	})
}

func TestLinkAddRelay(t *testing.T) {
	f := newFakeRelay(t)
	cfg, _ := relayTestEnv(t, f.URL)
	root := t.TempDir()
	opts := LinkOpts{Cfg: cfg, Root: root, Args: []string{"--add", "fb-stage-2", "--relay", "fb/stage", "--ssh-host", "morwi-fb"}}
	if err := RunLink(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	var saved *config.ConnectTarget
	for i := range g.ConnectTargets {
		if g.ConnectTargets[i].Name == "fb-stage-2" {
			saved = &g.ConnectTargets[i]
		}
	}
	if saved == nil || saved.Relay != "fb/stage" || saved.SSHHost != "morwi-fb" || saved.DBName != "fuentebuena" {
		t.Fatalf("saved target = %+v", saved)
	}
	if cfg.ConnectRemotePath != config.RelayPath("fb/stage") || cfg.ConnectSSHHost != "morwi-fb" {
		t.Errorf("binding = %s %s", cfg.ConnectSSHHost, cfg.ConnectRemotePath)
	}

	opts.Args = []string{"--add", "fb-qa", "--relay", "fb/qa"}
	if err := RunLink(context.Background(), opts); !errors.Is(err, relay.ErrNotFound) {
		t.Errorf("unknown env: err = %v, want not found", err)
	}
	for _, args := range [][]string{
		{"--relay", "fb/stage"},
		{"--add", "--relay", "fb/stage"},
		{"--add", "x", "--relay", "fb"},
		{"--show", "--relay", "fb/stage"},
	} {
		opts.Args = args
		if err := RunLink(context.Background(), opts); !errors.Is(err, ErrUsage) {
			t.Errorf("link %v: err = %v, want usage", args, err)
		}
	}
}

func TestI18nUpdateOnRelayUsesTheContainerConf(t *testing.T) {
	f := newFakeRelay(t)
	cfg, logPath := relayTestEnv(t, f.URL)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "fb_mod", "__manifest__.py"), "{'name': 'fb_mod'}\n")
	mustWrite(t, filepath.Join(root, "fb_mod", "i18n", "es_MX.po"), "msgid \"\"\nmsgstr \"\"\n")
	cfg.ProjectPath = root

	err := RunI18nUpdate(context.Background(), I18nOpts{Cfg: cfg, Root: root, Args: []string{"fb_mod", "es_MX", "--from", "fb-stage"}})
	if err != nil {
		t.Fatal(err)
	}
	got := sshLog(t, logPath)
	if len(got) != 3 {
		t.Fatalf("ssh ran %d commands:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.HasPrefix(got[0], "morwi-fb\tdocker exec -i 'relay-fb-stage-odoo' 'sh' '-c' 'cat > /tmp/echo-i18n-") {
		t.Errorf("copy = %s", got[0])
	}
	if !strings.Contains(got[1], "'odoo' 'i18n' 'import' '-c' '/etc/odoo/odoo.conf' '-d' 'fuentebuena' '-l' 'es_MX' '-w' '/tmp/echo-i18n-") {
		t.Errorf("import = %s", got[1])
	}
	if !strings.Contains(got[2], "'rm' '-f' '/tmp/echo-i18n-") || strings.Contains(got[2], ".conf") {
		t.Errorf("cleanup = %s, want only the .po removed", got[2])
	}
}
