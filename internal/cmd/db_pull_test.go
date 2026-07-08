package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseDBPullArgs(t *testing.T) {
	t.Run("defaults: neutralize auto (nil)", func(t *testing.T) {
		f := parseDBPullArgs([]string{"--from", "prod"})
		if f.from != "prod" || f.remote {
			t.Fatalf("remote flags wrong: from=%q remote=%v", f.from, f.remote)
		}
		if f.neutralize != nil {
			t.Fatalf("neutralize should be nil (auto), got %v", *f.neutralize)
		}
		if f.asName != "" || f.filestore || f.force {
			t.Fatalf("unexpected non-default flags: %+v", f)
		}
	})

	t.Run("forced neutralize on", func(t *testing.T) {
		f := parseDBPullArgs([]string{"--neutralize"})
		if f.neutralize == nil || !*f.neutralize {
			t.Fatalf("--neutralize should force true")
		}
	})

	t.Run("forced neutralize off", func(t *testing.T) {
		f := parseDBPullArgs([]string{"--no-neutralize"})
		if f.neutralize == nil || *f.neutralize {
			t.Fatalf("--no-neutralize should force false")
		}
	})

	t.Run("as/filestore/force + --from value not treated as positional", func(t *testing.T) {
		f := parseDBPullArgs([]string{"--from", "staging", "--as", "clientx_debug", "--filestore", "--force"})
		if f.asName != "clientx_debug" {
			t.Fatalf("asName = %q", f.asName)
		}
		if !f.filestore || !f.force {
			t.Fatalf("filestore/force not set: %+v", f)
		}
		if f.from != "staging" {
			t.Fatalf("from = %q", f.from)
		}
	})

	t.Run("--as= and --remote", func(t *testing.T) {
		f := parseDBPullArgs([]string{"--remote", "--as=foo"})
		if !f.remote || f.asName != "foo" {
			t.Fatalf("remote/as wrong: %+v", f)
		}
	})
}

func TestSanitizeDBName(t *testing.T) {
	cases := map[string]string{
		"muutrade-PROD":   "muutrade_prod",
		"muutrade_prod":   "muutrade_prod",
		"Client X (2024)": "client_x_2024",
		"a--b__c":         "a_b_c",
		"__lead__trail__": "lead_trail",
		"UPPER":           "upper",
	}
	for in, want := range cases {
		if got := sanitizeDBName(in); got != want {
			t.Fatalf("sanitizeDBName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultPullName(t *testing.T) {
	// The default --as is sanitize(remoteDB + "_" + targetLabel).
	got := sanitizeDBName("muutrade" + "_" + "prod")
	if got != "muutrade_prod" {
		t.Fatalf("default name = %q, want muutrade_prod", got)
	}
}

func TestRunSSHToFileHappyPath(t *testing.T) {
	orig := sshToFileCommand
	defer func() { sshToFileCommand = orig }()
	sshToFileCommand = func(ctx context.Context, host, remoteCmd string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf 'hello world'")
	}

	dest := filepath.Join(t.TempDir(), "out.dump")
	var lastProgress int64
	err := runSSHToFile(context.Background(), "ignored", "ignored", dest, func(n int64) {
		lastProgress = n
	})
	if err != nil {
		t.Fatalf("runSSHToFile: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("dest content = %q", string(data))
	}
	if lastProgress != int64(len("hello world")) {
		t.Fatalf("final progress = %d, want %d", lastProgress, len("hello world"))
	}
}

func TestRunSSHToFileFailureLeavesNoPartial(t *testing.T) {
	orig := sshToFileCommand
	defer func() { sshToFileCommand = orig }()
	sshToFileCommand = func(ctx context.Context, host, remoteCmd string) *exec.Cmd {
		// Write a few bytes, then fail — the partial file must be removed.
		return exec.CommandContext(ctx, "sh", "-c", "printf partial; echo boom >&2; exit 1")
	}

	dest := filepath.Join(t.TempDir(), "out.dump")
	err := runSSHToFile(context.Background(), "ignored", "ignored", dest, nil)
	if err == nil {
		t.Fatal("expected error from failing command")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("partial file should be removed, stat err = %v", statErr)
	}
}
