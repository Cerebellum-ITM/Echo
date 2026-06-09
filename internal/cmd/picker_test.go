package cmd

import (
	"errors"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

func TestNewFuzzyPickerRecentMarking(t *testing.T) {
	var pal theme.Palette
	m := newFuzzyPicker("t", []string{"sale", "account", "stock"}, []string{"account"}, pal)

	want := map[string]bool{"sale": false, "account": true, "stock": false}
	for _, it := range m.items {
		if it.recent != want[it.name] {
			t.Errorf("%s: recent=%v, want %v", it.name, it.recent, want[it.name])
		}
	}
	if !m.hasRecent() {
		t.Error("hasRecent should be true when a recent item is present")
	}
}

func TestNewFuzzyPickerNoRecent(t *testing.T) {
	var pal theme.Palette
	m := newFuzzyPicker("t", []string{"sale", "account"}, nil, pal)
	for _, it := range m.items {
		if it.recent {
			t.Errorf("%s: recent should be false with nil recent", it.name)
		}
	}
	if m.hasRecent() {
		t.Error("hasRecent should be false with nil recent")
	}
}

// confirmRepeatLast must skip silently (return nil) when stdin is not a
// TTY — it's only ever reached on the interactive empty-picker path, and a
// TTY-less caller (script mode) should never block or fail here.
func TestConfirmRepeatLastSkipsWithoutTTY(t *testing.T) {
	orig := stdinIsTTY
	defer func() { stdinIsTTY = orig }()
	stdinIsTTY = func() bool { return false }

	var pal theme.Palette
	if err := confirmRepeatLast(pal, []string{"sale"}); err != nil {
		t.Errorf("want nil (skip) without TTY, got %v", err)
	}
	if errors.Is(confirmRepeatLast(pal, []string{"sale"}), ErrCancelled) {
		t.Error("must not report ErrCancelled when skipping")
	}
}
