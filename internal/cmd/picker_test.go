package cmd

import (
	"errors"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

func TestNewFuzzyPickerRecentMarking(t *testing.T) {
	var pal theme.Palette
	m := newFuzzyPicker("t", []string{"sale", "account", "stock"}, []string{"account"}, nil, pal)

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
	m := newFuzzyPicker("t", []string{"sale", "account"}, nil, nil, pal)
	for _, it := range m.items {
		if it.recent {
			t.Errorf("%s: recent should be false with nil recent", it.name)
		}
	}
	if m.hasRecent() {
		t.Error("hasRecent should be false with nil recent")
	}
}

func TestNewFuzzyPickerDeployedMarking(t *testing.T) {
	var pal theme.Palette
	m := newFuzzyPicker("t", []string{"a1 subj", "b2 subj", "c3 subj"},
		nil, []string{"b2 subj"}, pal)

	want := map[string]bool{"a1 subj": false, "b2 subj": true, "c3 subj": false}
	for _, it := range m.items {
		if it.deployed != want[it.name] {
			t.Errorf("%s: deployed=%v, want %v", it.name, it.deployed, want[it.name])
		}
	}
	if !m.hasDeployed() {
		t.Error("hasDeployed should be true when a deployed item is present")
	}
}

func TestSplitLabel(t *testing.T) {
	cases := []struct{ in, head, tail string }{
		{"develop         Ionos:/srv/odoo", "develop", "         Ionos:/srv/odoo"},
		{"account", "account", ""},
		{"! admin   Alice Johnson", "! admin", "   Alice Johnson"},
		{"single", "single", ""},
	}
	for _, c := range cases {
		head, tail := splitLabel(c.in)
		if head != c.head || tail != c.tail {
			t.Errorf("splitLabel(%q) = (%q, %q), want (%q, %q)", c.in, head, tail, c.head, c.tail)
		}
		if head+tail != c.in {
			t.Errorf("splitLabel(%q): head+tail must reconstruct the input", c.in)
		}
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
