package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAliasName(t *testing.T) {
	ok := []string{"habitta", "demo_prod", "p1", "Proj-2"}
	for _, n := range ok {
		if err := ValidateAliasName(n); err != nil {
			t.Errorf("ValidateAliasName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "has space", "a/b", "-flag", ".", ".."}
	for _, n := range bad {
		if err := ValidateAliasName(n); err == nil {
			t.Errorf("ValidateAliasName(%q) = nil, want error", n)
		}
	}
}

// withTempConfig points HOME/XDG at a temp dir so LoadGlobal/SaveGlobal hit
// an isolated global.toml, and returns a real local dir for use as a path.
func withTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	return dir
}

func TestSetResolveRemoveProjectAlias(t *testing.T) {
	home := withTempConfig(t)
	proj := filepath.Join(home, "work", "habitta")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SetProjectAlias("habitta", proj); err != nil {
		t.Fatalf("SetProjectAlias: %v", err)
	}
	path, source, ok := ResolveProjectAlias("habitta")
	if !ok || path != proj || source != "alias" {
		t.Fatalf("Resolve = (%q, %q, %v), want (%q, alias, true)", path, source, ok, proj)
	}

	// Unknown name resolves to nothing.
	if _, _, ok := ResolveProjectAlias("nope"); ok {
		t.Error("Resolve(nope) ok = true, want false")
	}

	removed, err := RemoveProjectAlias("habitta")
	if err != nil || !removed {
		t.Fatalf("Remove = (%v, %v), want (true, nil)", removed, err)
	}
	if again, _ := RemoveProjectAlias("habitta"); again {
		t.Error("second Remove = true, want false")
	}
}

func TestResolveProjectAliasConnectFallback(t *testing.T) {
	home := withTempConfig(t)
	local := filepath.Join(home, "srv", "odoo")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	// One connect target whose remote_path is local, one purely remote.
	if err := SaveConnectTarget(ConnectTarget{Name: "onserver", RemotePath: local}); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnectTarget(ConnectTarget{Name: "remote", SSHHost: "erp", RemotePath: "/opt/does-not-exist-here"}); err != nil {
		t.Fatal(err)
	}

	path, source, ok := ResolveProjectAlias("onserver")
	if !ok || path != local || source != "connect" {
		t.Fatalf("Resolve(onserver) = (%q, %q, %v), want (%q, connect, true)", path, source, ok, local)
	}
	if _, _, ok := ResolveProjectAlias("remote"); ok {
		t.Error("Resolve(remote) ok = true, want false (path not local)")
	}
}

func TestMigrateConnectAliases(t *testing.T) {
	home := withTempConfig(t)
	local := filepath.Join(home, "srv", "odoo")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnectTarget(ConnectTarget{Name: "onserver", RemotePath: local}); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnectTarget(ConnectTarget{Name: "remote", SSHHost: "erp", RemotePath: "/opt/nope"}); err != nil {
		t.Fatal(err)
	}

	added, skipped, err := MigrateConnectAliases()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(added) != 1 || added[0] != "onserver" {
		t.Errorf("added = %v, want [onserver]", added)
	}
	if len(skipped) != 1 || skipped[0] != "remote" {
		t.Errorf("skipped = %v, want [remote]", skipped)
	}

	// Idempotent: a second run adds nothing (onserver now already aliased).
	added2, _, err := MigrateConnectAliases()
	if err != nil {
		t.Fatalf("migrate 2: %v", err)
	}
	if len(added2) != 0 {
		t.Errorf("second migrate added = %v, want none", added2)
	}
}
