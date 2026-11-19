package cmd

import (
	"context"
	"errors"
	"testing"
)

func TestEvalRelayContainers(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		status  string
		message string
	}{
		{"both running", "/odoo true\n/db true\n", doctorOK, "running"},
		{"odoo stopped", "/odoo false\n/db true\n", doctorFailed, "odoo is not running"},
		{"db missing", "/odoo true\nError: No such object: db\n", doctorFailed, "db does not exist"},
		{"empty output", "", doctorFailed, "odoo does not exist; db does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := evalRelayContainers(tt.out, "odoo", "db")
			if c.Status != tt.status || c.Message != tt.message {
				t.Errorf("got %s %q, want %s %q", c.Status, c.Message, tt.status, tt.message)
			}
		})
	}
}

// scriptDoctorSSH replaces doctorRunSSH with one canned answer and records
// the host and script of every call.
func scriptDoctorSSH(t *testing.T, out string, err error) *[][2]string {
	t.Helper()
	orig := doctorRunSSH
	t.Cleanup(func() { doctorRunSSH = orig })
	var calls [][2]string
	doctorRunSSH = func(ctx context.Context, host, script string, _ []byte) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the batch runs without a timeout")
		}
		calls = append(calls, [2]string{host, script})
		return []byte(out), err
	}
	return &calls
}

func TestDoctorRelayHealthy(t *testing.T) {
	f := newFakeRelay(t)
	cfg, _ := relayTestEnv(t, f.URL)
	calls := scriptDoctorSSH(t, "/relay-fb-stage-odoo true\n/relay-fb-stage-db true\n", nil)
	rec := &logRecorder{}
	res, err := RunDoctor(context.Background(), DoctorOpts{Cfg: cfg, Root: t.TempDir(), Args: []string{"--from", "fb-stage"}, Log: rec.log})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Checks) != 3 || res.Counts.OK != 3 {
		t.Fatalf("checks = %+v", res.Checks)
	}
	relayCheck := wantStatus(t, res, "relay", doctorOK)
	if checkField(relayCheck, "env") != "fb/stage" || checkField(relayCheck, "release") != "11" || checkField(relayCheck, "sha") != "165297d" {
		t.Errorf("relay fields = %v", relayCheck.Fields)
	}
	wantStatus(t, res, "ssh", doctorOK)
	wantStatus(t, res, "containers", doctorOK)
	if res.Target.Stage != "staging" || !res.Target.StageDeclared || res.Target.DB != "fuentebuena" ||
		res.Target.Host != "morwi-fb" || res.Target.Path != "relay:fb/stage" {
		t.Errorf("target = %+v", res.Target)
	}
	wantScript := "docker inspect -f '{{.Name}} {{.State.Running}}' 'relay-fb-stage-odoo' 'relay-fb-stage-db' 2>&1; true"
	if len(*calls) != 1 || (*calls)[0] != [2]string{"morwi-fb", wantScript} {
		t.Errorf("ssh calls = %q", *calls)
	}
	if res.Counts.Failed != 0 || res.Counts.Warn != 0 {
		t.Errorf("counts = %+v, want no failure (exit 0)", res.Counts)
	}
}

func TestDoctorRelayResolveRefused(t *testing.T) {
	f := newFakeRelay(t)
	cfg, _ := relayTestEnv(t, f.URL)
	cfg.RelayToken = "rlt_revoked"
	calls := scriptDoctorSSH(t, "", nil)
	res, err := RunDoctor(context.Background(), DoctorOpts{Cfg: cfg, Root: t.TempDir(), Args: []string{"--from", "fb-stage"}})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, res, "relay", doctorFailed)
	for _, id := range []string{"ssh", "containers"} {
		if c := wantStatus(t, res, id, doctorSkipped); c.Reason != "unresolved" {
			t.Errorf("%s reason = %q", id, c.Reason)
		}
	}
	if len(*calls) != 0 || res.Counts.Failed != 1 {
		t.Errorf("ssh calls = %q, counts = %+v", *calls, res.Counts)
	}
}

func TestDoctorRelaySSHFails(t *testing.T) {
	f := newFakeRelay(t)
	cfg, _ := relayTestEnv(t, f.URL)
	scriptDoctorSSH(t, "", errors.New("ssh: connect to host morwi-fb port 22: Operation timed out"))
	res, err := RunDoctor(context.Background(), DoctorOpts{Cfg: cfg, Root: t.TempDir(), Args: []string{"--from", "fb-stage"}})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, res, "relay", doctorOK)
	if c := wantStatus(t, res, "ssh", doctorFailed); c.Message != "ssh: connect to host morwi-fb port 22: Operation timed out" {
		t.Errorf("ssh message = %q", c.Message)
	}
	if c := wantStatus(t, res, "containers", doctorSkipped); c.Reason != "unreachable" {
		t.Errorf("containers reason = %q", c.Reason)
	}
}
