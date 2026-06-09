package config

import (
	"testing"
	"time"
)

func TestLastUpdateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	key := "deadbeef"
	now := time.Now().Truncate(time.Second)
	if err := SaveLastUpdate(key, "demo", LastUpdate{
		Modules: []string{"sale", "account"}, Level: "debug", SavedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok := LoadLastUpdate(key, "demo")
	if !ok {
		t.Fatal("record missing after save")
	}
	if len(got.Modules) != 2 || got.Modules[0] != "sale" || got.Modules[1] != "account" {
		t.Errorf("modules round-trip mismatch: %+v", got.Modules)
	}
	if got.All {
		t.Error("All should be false for a module list")
	}
	if got.Level != "debug" {
		t.Errorf("level mismatch: got %q", got.Level)
	}
	if !got.SavedAt.Equal(now) {
		t.Errorf("saved_at mismatch: got %v want %v", got.SavedAt, now)
	}
}

func TestLastUpdateAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	key := "feedface"
	if err := SaveLastUpdate(key, "prod", LastUpdate{All: true, Level: "warn"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := LoadLastUpdate(key, "prod")
	if !ok {
		t.Fatal("record missing")
	}
	if !got.All || len(got.Modules) != 0 {
		t.Errorf("--all round-trip mismatch: %+v", got)
	}
}

func TestLastUpdatePerDBIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	key := "cafebabe"
	_ = SaveLastUpdate(key, "demo", LastUpdate{Modules: []string{"sale"}})
	_ = SaveLastUpdate(key, "prod", LastUpdate{Modules: []string{"stock"}})

	// Saving a second DB must not clobber the first.
	demo, ok := LoadLastUpdate(key, "demo")
	if !ok || len(demo.Modules) != 1 || demo.Modules[0] != "sale" {
		t.Errorf("demo entry mismatch: %+v ok=%v", demo, ok)
	}
	prod, ok := LoadLastUpdate(key, "prod")
	if !ok || len(prod.Modules) != 1 || prod.Modules[0] != "stock" {
		t.Errorf("prod entry mismatch: %+v ok=%v", prod, ok)
	}
}

func TestLastUpdateUpsert(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	key := "0badf00d"
	_ = SaveLastUpdate(key, "demo", LastUpdate{Modules: []string{"sale"}})
	_ = SaveLastUpdate(key, "demo", LastUpdate{Modules: []string{"account"}})

	got, _ := LoadLastUpdate(key, "demo")
	if len(got.Modules) != 1 || got.Modules[0] != "account" {
		t.Errorf("upsert did not overwrite: %+v", got.Modules)
	}
}

func TestLoadLastUpdateMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, ok := LoadLastUpdate("nope", "demo"); ok {
		t.Fatal("missing recall must report ok=false")
	}
	// Present project file, absent DB key.
	key := "abc123"
	_ = SaveLastUpdate(key, "demo", LastUpdate{Modules: []string{"sale"}})
	if _, ok := LoadLastUpdate(key, "other"); ok {
		t.Fatal("absent db key must report ok=false")
	}
}
