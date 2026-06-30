package cmd

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/pascualchavez/echo/internal/theme"
)

func TestNewFuzzyPickerRecentMarking(t *testing.T) {
	var pal theme.Palette
	m := newFuzzyPicker("t", []string{"sale", "account", "stock"}, []string{"account"}, nil, nil, pal)

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
	m := newFuzzyPicker("t", []string{"sale", "account"}, nil, nil, nil, pal)
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
		nil, []string{"b2 subj"}, nil, pal)

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

// ctrl+d toggles the deployed mark on the highlighted markable row only;
// a non-markable row (no SHA) is left untouched.
func TestPickerCtrlDTogglesMarkable(t *testing.T) {
	var pal theme.Palette
	m := newFuzzyPicker("t", []string{"a1 subj", "b2 subj"},
		nil, nil, []string{"a1 subj"}, pal) // only a1 is markable

	if !m.hasMarkable() {
		t.Fatal("hasMarkable should be true with a markable row")
	}

	// Cursor on a1 (markable) → ctrl+d marks it deployed.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = m2.(fuzzyPicker)
	if !m.items[0].deployed {
		t.Error("ctrl+d should mark the highlighted markable row deployed")
	}
	// Toggle again → un-marks.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = m2.(fuzzyPicker)
	if m.items[0].deployed {
		t.Error("ctrl+d should un-mark a deployed row")
	}

	// Move cursor to b2 (not markable) → ctrl+d is a no-op.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = m2.(fuzzyPicker)
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = m2.(fuzzyPicker)
	if m.items[1].deployed {
		t.Error("ctrl+d must not mark a non-markable row")
	}
}

// ctrl+a marks every visible markable row at once, and un-marks them all
// when they are already marked. Non-markable rows are never touched.
func TestPickerCtrlABulkMark(t *testing.T) {
	var pal theme.Palette
	m := newFuzzyPicker("t", []string{"a1 subj", "b2 subj", "~ mod dirty"},
		nil, nil, []string{"a1 subj", "b2 subj"}, pal)

	// First ctrl+a: some pending → mark all markable rows.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = m2.(fuzzyPicker)
	if !m.items[0].deployed || !m.items[1].deployed {
		t.Error("ctrl+a should mark all markable rows deployed")
	}
	if m.items[2].deployed {
		t.Error("ctrl+a must not mark the non-markable row")
	}
	if len(m.deployedNames()) != 2 {
		t.Errorf("deployedNames = %v, want 2", m.deployedNames())
	}

	// Second ctrl+a: all already marked → un-mark all.
	m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m = m2.(fuzzyPicker)
	if m.items[0].deployed || m.items[1].deployed {
		t.Error("ctrl+a should un-mark all when every markable row is already deployed")
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
