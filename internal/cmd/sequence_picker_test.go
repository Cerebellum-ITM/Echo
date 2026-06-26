package cmd

import (
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

func newTestSeqPicker(names ...string) sequencePicker {
	items := make([]SeqItem, len(names))
	for i, n := range names {
		items[i] = SeqItem{Name: n}
	}
	return newSequencePicker("t", items, theme.PaletteByName(""))
}

func TestSequencePickerCycle(t *testing.T) {
	m := newTestSeqPicker("up", "update", "logs")
	// Cycle "up": off → run.
	m.cycle(0)
	if m.items[0].state != seqRun {
		t.Fatalf("after 1 cycle state = %d, want seqRun", m.items[0].state)
	}
	if m.orderOf(0) != 1 {
		t.Fatalf("orderOf(0) = %d, want 1", m.orderOf(0))
	}
	// Cycle "logs": off → run (order 2).
	m.cycle(2)
	if m.orderOf(2) != 2 {
		t.Fatalf("orderOf(2) = %d, want 2", m.orderOf(2))
	}
	// Cycle "up" again: run → build, keeps order 1.
	m.cycle(0)
	if m.items[0].state != seqBuild {
		t.Fatalf("state = %d, want seqBuild", m.items[0].state)
	}
	if m.orderOf(0) != 1 {
		t.Fatalf("orderOf(0) after build = %d, want 1", m.orderOf(0))
	}
	picks := m.picks()
	want := []SeqPick{{Command: "up", Build: true}, {Command: "logs", Build: false}}
	if !reflect.DeepEqual(picks, want) {
		t.Fatalf("picks = %v, want %v", picks, want)
	}
}

func TestSequencePickerRenumberOnRemove(t *testing.T) {
	m := newTestSeqPicker("a", "b", "c")
	m.cycle(0) // a → 1
	m.cycle(1) // b → 2
	m.cycle(2) // c → 3
	// Remove b: build → off requires two more cycles (run→build→off).
	m.cycle(1) // b run → build
	m.cycle(1) // b build → off (removed from order)
	if m.items[1].state != seqOff {
		t.Fatalf("b state = %d, want seqOff", m.items[1].state)
	}
	if m.orderOf(0) != 1 || m.orderOf(2) != 2 {
		t.Fatalf("renumber wrong: a=%d c=%d, want a=1 c=2", m.orderOf(0), m.orderOf(2))
	}
	picks := m.picks()
	want := []SeqPick{{Command: "a"}, {Command: "c"}}
	if !reflect.DeepEqual(picks, want) {
		t.Fatalf("picks = %v, want %v", picks, want)
	}
}

func TestSequencePickerEmptyPicks(t *testing.T) {
	m := newTestSeqPicker("a", "b")
	if len(m.picks()) != 0 {
		t.Fatal("expected no picks for an untouched picker")
	}
}
