package config

import (
	"os"
	"path/filepath"
	"testing"
)

// seedProjectState writes one file per keyed location, so a migration that
// moves only the project toml is visibly incomplete.
func seedProjectState(t *testing.T, home, root string) string {
	t.Helper()
	key := ProjectKey(root)
	for _, dir := range projectStateFiles {
		full := filepath.Join(home, configDir, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, key+".toml"), []byte("x = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logs := filepath.Join(home, configDir, "cmd-logs", key)
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestMigrateProjectKeyMovesEveryLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldRoot, newRoot := "/repo/subdir", "/repo"
	oldKey := seedProjectState(t, home, oldRoot)
	newKey := ProjectKey(newRoot)

	moved, err := MigrateProjectKey(oldRoot, newRoot)
	if err != nil || !moved {
		t.Fatalf("MigrateProjectKey = (%v, %v), want (true, nil)", moved, err)
	}
	for _, dir := range projectStateFiles {
		if _, err := os.Stat(filepath.Join(home, configDir, dir, newKey+".toml")); err != nil {
			t.Errorf("%s not migrated: %v", dir, err)
		}
		if _, err := os.Stat(filepath.Join(home, configDir, dir, oldKey+".toml")); err == nil {
			t.Errorf("%s left behind at the old key", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(home, configDir, "cmd-logs", newKey)); err != nil {
		t.Errorf("cmd-logs not migrated: %v", err)
	}
}

func TestMigrateProjectKeyRefusesToOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldRoot, newRoot := "/repo/subdir", "/repo"
	seedProjectState(t, home, oldRoot)
	newKey := ProjectKey(newRoot)
	dest := filepath.Join(home, configDir, "projects", newKey+".toml")
	if err := os.WriteFile(dest, []byte("keep = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := MigrateProjectKey(oldRoot, newRoot)
	if err != nil || moved {
		t.Fatalf("MigrateProjectKey = (%v, %v), want (false, nil)", moved, err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "keep = true\n" {
		t.Errorf("destination was clobbered: %q, %v", data, err)
	}
}

func TestMigrateProjectKeyIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldRoot, newRoot := "/repo/subdir", "/repo"
	seedProjectState(t, home, oldRoot)

	if moved, err := MigrateProjectKey(oldRoot, newRoot); err != nil || !moved {
		t.Fatalf("first run = (%v, %v), want (true, nil)", moved, err)
	}
	if moved, err := MigrateProjectKey(oldRoot, newRoot); err != nil || moved {
		t.Fatalf("second run = (%v, %v), want (false, nil)", moved, err)
	}
}

func TestFindProjectStateReturnsDeepestMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedProjectState(t, home, "/repo/a/b")

	if got := FindProjectState("/repo/a/b/c", "/repo"); got != "/repo/a/b" {
		t.Errorf("FindProjectState = %q, want /repo/a/b", got)
	}
	if got := FindProjectState("/repo/a/b/c", "/repo/a/b/c"); got != "" {
		t.Errorf("FindProjectState stopped too late: %q", got)
	}
}
