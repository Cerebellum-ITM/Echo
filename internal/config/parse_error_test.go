package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const brokenTOML = "theme = \"tokyo\"\nlogo = = \"echo\"\n"

func requireParseError(t *testing.T, err error, path string) *ParseError {
	t.Helper()
	var perr *ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want a *ParseError", err)
	}
	if perr.Path != path {
		t.Errorf("ParseError.Path = %q, want %q", perr.Path, path)
	}
	return perr
}

func requireUnchanged(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s was rewritten:\n%s", path, got)
	}
}

func TestLoadRefusesUnparseableGlobal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeGlobal(t, brokenTOML)

	_, err := Load("/test/project")
	perr := requireParseError(t, err, path)
	if want := "cannot parse ~/.config/echo/global.toml: line 2, column"; !strings.HasPrefix(perr.Error(), want) {
		t.Errorf("message = %q, want prefix %q", perr.Error(), want)
	}
	_, err = LoadGlobal()
	requireParseError(t, err, path)
	requireUnchanged(t, path, brokenTOML)
}

func TestLoadRefusesUnparseableProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, _ := configRoot()
	path := filepath.Join(root, "projects", projectKey("/test/project")+".toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stage = \"prod\"\nstage = \"dev\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load("/test/project")
	requireParseError(t, err, path)
}

func TestWritersRefuseUnparseableGlobal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeGlobal(t, brokenTOML)

	writers := map[string]func() error{
		"SaveGlobal":            func() error { return SaveGlobal(&Config{Theme: "charm"}) },
		"SaveConnectTarget":     func() error { return SaveConnectTarget(ConnectTarget{Name: "stg", SSHHost: "h", RemotePath: "/srv"}) },
		"SavePromoteBranch":     func() error { return SavePromoteBranch("develop") },
		"SetProjectAlias":       func() error { return SetProjectAlias("erp", "/srv/erp") },
		"MigrateConnectAliases": func() error { _, _, err := MigrateConnectAliases(); return err },
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			requireParseError(t, write(), path)
			requireUnchanged(t, path, brokenTOML)
		})
	}
}

func TestSaveProjectRefusesUnparseableProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &Config{ProjectPath: "/test/project", ProjectKey: projectKey("/test/project"), Stage: "dev"}
	root, _ := configRoot()
	path := filepath.Join(root, "projects", cfg.ProjectKey+".toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(brokenTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	requireParseError(t, SaveProject(cfg), path)
	requireUnchanged(t, path, brokenTOML)
}

func TestParseRemoteProfileErrors(t *testing.T) {
	_, err := ParseRemoteProfile([]byte(brokenTOML), nil)
	requireParseError(t, err, GlobalFileName)

	_, err = ParseRemoteProfile(nil, []byte("db_name = \"erp\"\nstage = \n"))
	perr := requireParseError(t, err, projectProfileName)
	if !strings.HasPrefix(perr.Detail(), "line 2") {
		t.Errorf("Detail = %q, want the line first", perr.Detail())
	}
	if _, err := ParseProjectInfo([]byte(brokenTOML)); err == nil {
		t.Error("ParseProjectInfo accepted a broken profile")
	}
}

func TestCheckpointWriteKeepsCorruptStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := checkpointStorePath("proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const corrupt = "[targets.abc\nentries = 1\n"
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	tk := DeployTargetKey("stg", "/srv")
	if got := LoadCheckpoints("proj", tk); got != nil {
		t.Fatalf("a corrupt store should read as empty, got %v", got)
	}

	entry := CheckpointEntry{Name: "db__ckpt", Method: "db", DB: "db", CreatedAt: time.Unix(1000, 0)}
	if err := AddCheckpoint("proj", tk, entry); err != nil {
		t.Fatal(err)
	}
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if len(aside) != 1 {
		t.Fatalf("corrupt copies = %v, want one", aside)
	}
	requireUnchanged(t, aside[0], corrupt)
	if got := LoadCheckpoints("proj", tk); len(got) != 1 || got[0].Name != "db__ckpt" {
		t.Errorf("new store = %v", got)
	}

	if err := AddCheckpoint("proj", tk, entry); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(path + ".corrupt-*"); len(again) != 1 {
		t.Errorf("a parseable store was moved aside: %v", again)
	}
}
