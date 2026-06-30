package config

import "testing"

func TestDeployTargetKeyDistinct(t *testing.T) {
	a := DeployTargetKey("host1", "/srv/odoo")
	b := DeployTargetKey("host1", "/srv/other")
	c := DeployTargetKey("host2", "/srv/odoo")
	if a == b || a == c || b == c {
		t.Errorf("target keys should differ per host/path: %s %s %s", a, b, c)
	}
	if a != DeployTargetKey("host1", "/srv/odoo") {
		t.Error("target key must be stable for the same host/path")
	}
}

func TestDeployHistoryRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk := "deadbeef"
	tk := DeployTargetKey("prod", "/srv/odoo")

	// Empty before any write.
	if got := LoadDeployedSHAs(pk, tk); len(got) != 0 {
		t.Fatalf("expected empty set, got %v", got)
	}

	if err := MarkDeployed(pk, tk, []string{"sha1", "sha2"}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got := LoadDeployedSHAs(pk, tk)
	if !got["sha1"] || !got["sha2"] {
		t.Errorf("expected sha1+sha2 deployed, got %v", got)
	}
}

func TestMarkDeployedMergesAndDedups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk, tk := "k", DeployTargetKey("t", "/p")

	_ = MarkDeployed(pk, tk, []string{"a", "b"})
	_ = MarkDeployed(pk, tk, []string{"b", "c"}) // b repeated

	got := LoadDeployedSHAs(pk, tk)
	if len(got) != 3 || !got["a"] || !got["b"] || !got["c"] {
		t.Errorf("merge/dedup failed: %v", got)
	}
}

func TestMarkDeployedPerTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk := "k"
	staging := DeployTargetKey("staging", "/p")
	prod := DeployTargetKey("prod", "/p")

	_ = MarkDeployed(pk, staging, []string{"x"})

	if !LoadDeployedSHAs(pk, staging)["x"] {
		t.Error("x should be deployed to staging")
	}
	if LoadDeployedSHAs(pk, prod)["x"] {
		t.Error("x must NOT count as deployed to prod (per-target scope)")
	}
}

func TestMarkDeployedEmptyNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := MarkDeployed("k", "t", nil); err != nil {
		t.Errorf("empty mark should be a no-op, got %v", err)
	}
}

func TestUpdateDeployedMarksAddAndRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk, tk := "k", DeployTargetKey("prod", "/p")

	// Add-only seeds the set (manual ctrl+d / ctrl+a on never-deployed commits).
	if err := UpdateDeployedMarks(pk, tk, []string{"a", "b"}, nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	got := LoadDeployedSHAs(pk, tk)
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("after add: %v", got)
	}

	// Mixed delta in one write: add c, remove a.
	if err := UpdateDeployedMarks(pk, tk, []string{"c"}, []string{"a"}); err != nil {
		t.Fatalf("mixed: %v", err)
	}
	got = LoadDeployedSHAs(pk, tk)
	if got["a"] || !got["b"] || !got["c"] {
		t.Errorf("mixed delta failed: %v", got)
	}
}

func TestUnmarkDeployedAbsentNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pk, tk := "k", DeployTargetKey("prod", "/p")
	_ = MarkDeployed(pk, tk, []string{"keep"})

	// Removing an absent SHA leaves the set intact.
	if err := UnmarkDeployed(pk, tk, []string{"ghost"}); err != nil {
		t.Fatalf("unmark absent: %v", err)
	}
	got := LoadDeployedSHAs(pk, tk)
	if !got["keep"] || got["ghost"] || len(got) != 1 {
		t.Errorf("absent removal must be a no-op on the set: %v", got)
	}

	// Removing a present SHA drops exactly it.
	if err := UnmarkDeployed(pk, tk, []string{"keep"}); err != nil {
		t.Fatalf("unmark present: %v", err)
	}
	if len(LoadDeployedSHAs(pk, tk)) != 0 {
		t.Error("removing the only SHA should empty the set")
	}
}

func TestUpdateDeployedMarksEmptyNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := UpdateDeployedMarks("k", "t", nil, nil); err != nil {
		t.Errorf("empty add+remove should be a no-op, got %v", err)
	}
}
