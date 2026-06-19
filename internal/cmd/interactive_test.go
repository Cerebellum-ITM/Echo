package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

func TestRequireTTY(t *testing.T) {
	orig := stdinIsTTY
	defer func() { stdinIsTTY = orig }()

	stdinIsTTY = func() bool { return false }
	if err := requireTTY("do X"); !errors.Is(err, ErrNonInteractive) {
		t.Fatalf("no TTY: want ErrNonInteractive, got %v", err)
	}

	stdinIsTTY = func() bool { return true }
	if err := requireTTY("do X"); err != nil {
		t.Fatalf("with TTY: want nil, got %v", err)
	}
}

// TestInteractiveHelpersFailClosed asserts every blocking call site
// returns ErrNonInteractive (instead of hanging on a prompt) when stdin
// is not a TTY — the contract script mode relies on.
func TestInteractiveHelpersFailClosed(t *testing.T) {
	orig := stdinIsTTY
	defer func() { stdinIsTTY = orig }()
	stdinIsTTY = func() bool { return false }

	var pal theme.Palette

	if _, err := runFuzzyPicker("t", []string{"a"}, pal); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("runFuzzyPicker: want ErrNonInteractive, got %v", err)
	}
	if _, _, err := runFuzzyPickerCore("t", []string{"a"}, nil, nil, pal, ""); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("runFuzzyPickerCore: want ErrNonInteractive, got %v", err)
	}
	if _, err := runSingleFuzzyPicker("t", []string{"a"}, pal); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("runSingleFuzzyPicker: want ErrNonInteractive, got %v", err)
	}
	if err := confirmProd(pal, "bash", "db"); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("confirmProd: want ErrNonInteractive, got %v", err)
	}
	if err := confirmDrop(pal, "db"); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("confirmDrop: want ErrNonInteractive, got %v", err)
	}
	if err := confirmNeutralize(pal, "db"); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("confirmNeutralize: want ErrNonInteractive, got %v", err)
	}
	if err := confirmI18nProd(pal, "db", "es_MX"); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("confirmI18nProd: want ErrNonInteractive, got %v", err)
	}
	if _, err := RunBuild(context.Background(), BuildOpts{
		Cfg: &config.Config{}, Command: "update", Flags: []string{"--all"}, Palette: pal,
	}); !errors.Is(err, ErrNonInteractive) {
		t.Errorf("RunBuild: want ErrNonInteractive, got %v", err)
	}
}
