package config

import (
	"testing"
	"time"
)

func TestCheckpointStoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk := "proj"
	tk := DeployTargetKey("staging", "/srv/odoo")

	if got := LoadCheckpoints(pk, tk); got != nil {
		t.Fatalf("expected nil before any write, got %v", got)
	}

	e1 := CheckpointEntry{Name: "db__ckpt_a", Method: "db", DB: "db", CreatedAt: time.Unix(1000, 0), DeploySHAs: []string{"sha1"}}
	e2 := CheckpointEntry{Name: "db__ckpt_b", Method: "db", DB: "db", CreatedAt: time.Unix(2000, 0), DeploySHAs: []string{"sha2"}}
	if err := AddCheckpoint(pk, tk, e1); err != nil {
		t.Fatal(err)
	}
	if err := AddCheckpoint(pk, tk, e2); err != nil {
		t.Fatal(err)
	}

	got := LoadCheckpoints(pk, tk)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	// Newest first.
	if got[0].Name != "db__ckpt_b" || got[1].Name != "db__ckpt_a" {
		t.Errorf("order wrong: %s, %s", got[0].Name, got[1].Name)
	}
	if len(got[0].DeploySHAs) != 1 || got[0].DeploySHAs[0] != "sha2" {
		t.Errorf("shas not preserved: %v", got[0].DeploySHAs)
	}
}

func TestCheckpointRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk := "proj"
	tk := DeployTargetKey("staging", "/srv/odoo")

	_ = AddCheckpoint(pk, tk, CheckpointEntry{Name: "a", Method: "db", CreatedAt: time.Unix(1, 0)})
	_ = AddCheckpoint(pk, tk, CheckpointEntry{Name: "b", Method: "db", CreatedAt: time.Unix(2, 0)})

	if err := RemoveCheckpoint(pk, tk, "a"); err != nil {
		t.Fatal(err)
	}
	got := LoadCheckpoints(pk, tk)
	if len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("expected only b, got %v", got)
	}

	// Removing the last one clears the target.
	if err := RemoveCheckpoint(pk, tk, "b"); err != nil {
		t.Fatal(err)
	}
	if got := LoadCheckpoints(pk, tk); got != nil {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestCheckpointTargetsIsolated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk := "proj"
	staging := DeployTargetKey("staging", "/srv/odoo")
	prod := DeployTargetKey("prod", "/srv/odoo")

	_ = AddCheckpoint(pk, staging, CheckpointEntry{Name: "s", Method: "db", CreatedAt: time.Unix(1, 0)})
	if got := LoadCheckpoints(pk, prod); got != nil {
		t.Errorf("prod target should be empty, got %v", got)
	}
	if got := LoadCheckpoints(pk, staging); len(got) != 1 {
		t.Errorf("staging should have 1, got %v", got)
	}
}

func TestCheckpointConfigDefaultsAndOverride(t *testing.T) {
	// Defaults applied when no [checkpoint] section is present.
	cfg := &Config{}
	applyCheckpoint(cfg, nil)
	if cfg.CheckpointMode != "auto" || cfg.CheckpointMethod != "db" || cfg.CheckpointKeep != 2 {
		t.Errorf("defaults wrong: %q/%q/%d", cfg.CheckpointMode, cfg.CheckpointMethod, cfg.CheckpointKeep)
	}
	// A partial section overrides only its non-zero fields.
	cfg2 := &Config{}
	applyCheckpoint(cfg2, &checkpointConfig{Mode: "on"})
	if cfg2.CheckpointMode != "on" || cfg2.CheckpointMethod != "db" || cfg2.CheckpointKeep != 2 {
		t.Errorf("partial override wrong: %q/%q/%d", cfg2.CheckpointMode, cfg2.CheckpointMethod, cfg2.CheckpointKeep)
	}
}
