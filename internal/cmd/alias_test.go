package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestRunAliasUsageErrors(t *testing.T) {
	cases := [][]string{
		{"--bogus"},    // unknown flag
		{"one", "two"}, // two positional names
		{"--rm"},       // --rm without a name
		{"has space"},  // invalid alias name
	}
	for _, args := range cases {
		_, err := RunAlias(AliasOpts{Cfg: &config.Config{}, Root: t.TempDir(), Args: args})
		if err == nil || !errors.Is(err, ErrAliasUsage) {
			t.Errorf("RunAlias(%v) err = %v, want ErrAliasUsage", args, err)
		}
	}
}

func TestRunAliasSetAndList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := RunAlias(AliasOpts{Cfg: &config.Config{}, Root: root, Args: []string{"proj"}})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.Action != "set" || res.Name != "proj" || res.Path != root {
		t.Fatalf("set result = %+v", res)
	}

	list, err := RunAlias(AliasOpts{Cfg: &config.Config{}, Root: root, Args: nil})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Action != "list" || len(list.Aliases) != 1 || list.Aliases[0].Name != "proj" {
		t.Fatalf("list result = %+v", list)
	}
}
