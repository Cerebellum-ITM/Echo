package config

import (
	"testing"
	"time"
)

func TestConnectSessionRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	key := "deadbeef"
	now := time.Now().Truncate(time.Second)
	if err := SaveConnectSession(key, ConnectSession{
		Login: "admin", UID: 2, SID: "abc123", BaseURL: "https://erp.example.com", MintedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// A second login in the same target file must not clobber the first.
	if err := SaveConnectSession(key, ConnectSession{
		Login: "jdoe", UID: 7, SID: "xyz789", BaseURL: "https://erp.example.com", MintedAt: now,
	}); err != nil {
		t.Fatalf("save 2: %v", err)
	}

	got := LoadConnectSessions(key)
	if len(got) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got))
	}
	admin, ok := got["admin"]
	if !ok {
		t.Fatal("admin session missing")
	}
	if admin.SID != "abc123" || admin.UID != 2 || admin.BaseURL != "https://erp.example.com" {
		t.Errorf("admin round-trip mismatch: %+v", admin)
	}
	if !admin.MintedAt.Equal(now) {
		t.Errorf("minted_at mismatch: got %v want %v", admin.MintedAt, now)
	}
}

func TestConnectSessionUpsert(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	key := "feedface"
	_ = SaveConnectSession(key, ConnectSession{Login: "admin", UID: 2, SID: "old"})
	_ = SaveConnectSession(key, ConnectSession{Login: "admin", UID: 2, SID: "new"})

	got := LoadConnectSessions(key)
	if len(got) != 1 {
		t.Fatalf("upsert should keep one entry, got %d", len(got))
	}
	if got["admin"].SID != "new" {
		t.Errorf("upsert did not overwrite: got SID %q", got["admin"].SID)
	}
}

func TestLoadConnectSessionsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := LoadConnectSessions("nope")
	if got == nil {
		t.Fatal("missing cache must return a non-nil empty map")
	}
	if len(got) != 0 {
		t.Fatalf("missing cache should be empty, got %d", len(got))
	}
}
