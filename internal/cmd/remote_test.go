package cmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// fakeSSH substitutes the ssh binary with a local `sh -c <script>` for the
// duration of the test; the host/remoteCmd args are recorded into got.
func fakeSSH(t *testing.T, script string, got *[]string) {
	t.Helper()
	orig := sshStreamCommand
	sshStreamCommand = func(ctx context.Context, host, remoteCmd string) *exec.Cmd {
		if got != nil {
			*got = append(*got, host, remoteCmd)
		}
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
	t.Cleanup(func() { sshStreamCommand = orig })
}

func TestRunSSHStreamDeliversLines(t *testing.T) {
	fakeSSH(t, `echo one; echo two >&2; echo three`, nil)

	var lines []string
	err := runSSHStream(context.Background(), "host", "cmd", nil, func(l string) {
		lines = append(lines, l)
	})
	if err != nil {
		t.Fatalf("runSSHStream: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %v", len(lines), lines)
	}
	// stdout ordering is preserved even with stderr interleaved.
	var stdout []string
	for _, l := range lines {
		if l == "one" || l == "three" {
			stdout = append(stdout, l)
		}
	}
	if strings.Join(stdout, ",") != "one,three" {
		t.Fatalf("stdout order broken: %v", lines)
	}
}

func TestRunSSHStreamFailureKeepsLastStderr(t *testing.T) {
	fakeSSH(t, `echo ok; echo "boom: disk full" >&2; exit 3`, nil)

	var lines []string
	err := runSSHStream(context.Background(), "host", "cmd", nil, func(l string) {
		lines = append(lines, l)
	})
	if err == nil {
		t.Fatal("expected error on exit 3")
	}
	if !strings.Contains(err.Error(), "boom: disk full") {
		t.Fatalf("error %q does not carry last stderr line", err)
	}
	if len(lines) != 2 {
		t.Fatalf("stream lost lines before the failure: %v", lines)
	}
}

func TestRunSSHStreamNilOnLine(t *testing.T) {
	fakeSSH(t, `echo ignored`, nil)
	if err := runSSHStream(context.Background(), "host", "cmd", nil, nil); err != nil {
		t.Fatalf("nil onLine must discard output, got %v", err)
	}
}

func TestRemoteComposeCmd(t *testing.T) {
	got := remoteComposeCmd("/srv/odoo/my proj", "docker compose", "up", "-d")
	want := `cd '/srv/odoo/my proj' && docker compose 'up' '-d'`
	if got != want {
		t.Fatalf("remoteComposeCmd = %q, want %q", got, want)
	}
}
