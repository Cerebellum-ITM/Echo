package config

import "testing"

func TestSideloadHistoryRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk := "deadbeef"
	tk := DeployTargetKey("prod", "/srv/odoo")

	// Empty before any write.
	if got := LoadSideloadHashes(pk, tk); len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}

	if err := RecordSideloadHashes(pk, tk, map[string]string{"mod_a": "h1", "mod_b": "h2"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got := LoadSideloadHashes(pk, tk)
	if got["mod_a"] != "h1" || got["mod_b"] != "h2" {
		t.Errorf("round-trip mismatch: %v", got)
	}
}

func TestRecordSideloadHashesMergesAndKeepsPrior(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk, tk := "k", DeployTargetKey("t", "/p")

	_ = RecordSideloadHashes(pk, tk, map[string]string{"mod_a": "h1"})
	// A second record for a different addon must not forget mod_a, and an
	// updated hash for an existing addon overwrites.
	_ = RecordSideloadHashes(pk, tk, map[string]string{"mod_b": "h2", "mod_a": "h1b"})

	got := LoadSideloadHashes(pk, tk)
	if got["mod_a"] != "h1b" {
		t.Errorf("mod_a should be overwritten to h1b, got %q", got["mod_a"])
	}
	if got["mod_b"] != "h2" {
		t.Errorf("mod_b should be h2, got %q", got["mod_b"])
	}
}

func TestRecordSideloadHashesEmptyNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk, tk := "k", DeployTargetKey("t", "/p")
	if err := RecordSideloadHashes(pk, tk, nil); err != nil {
		t.Fatalf("empty record should be a no-op, got %v", err)
	}
	if got := LoadSideloadHashes(pk, tk); len(got) != 0 {
		t.Fatalf("expected empty after no-op, got %v", got)
	}
}

func TestSideloadTargetKeyedSeparately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk := "k"
	staging := DeployTargetKey("staging", "/srv/odoo")
	prod := DeployTargetKey("prod", "/srv/odoo")

	_ = RecordSideloadHashes(pk, staging, map[string]string{"mod": "hs"})
	if got := LoadSideloadHashes(pk, prod); len(got) != 0 {
		t.Fatalf("prod should be unaffected by a staging record, got %v", got)
	}
}
