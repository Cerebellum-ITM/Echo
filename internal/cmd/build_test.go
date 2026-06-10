package cmd

import (
	"reflect"
	"testing"
)

func TestComposeArgs(t *testing.T) {
	tests := []struct {
		name        string
		positionals []string
		flags       []chosenFlag
		want        []string
	}{
		{
			name:        "positionals plus boolean flags",
			positionals: []string{"sale", "account"},
			flags:       []chosenFlag{{name: "--all"}, {name: "--i18n"}},
			want:        []string{"sale", "account", "--all", "--i18n"},
		},
		{
			name:        "flag joined with =",
			positionals: []string{"sale"},
			flags:       []chosenFlag{{name: "--level", value: "debug", sep: "="}},
			want:        []string{"sale", "--level=debug"},
		},
		{
			name:        "flag joined with space emits two tokens",
			positionals: nil,
			flags:       []chosenFlag{{name: "-t", value: "100", sep: " "}},
			want:        []string{"-t", "100"},
		},
		{
			name:        "stable order: positionals then flags as given",
			positionals: []string{"a"},
			flags: []chosenFlag{
				{name: "--level", value: "info", sep: "="},
				{name: "--i18n"},
			},
			want: []string{"a", "--level=info", "--i18n"},
		},
		{
			name: "no positionals no flags",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := composeArgs(tc.positionals, tc.flags)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("composeArgs = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestBuildLine(t *testing.T) {
	if got := BuildLine("update", []string{"sale", "--level=debug"}); got != "update sale --level=debug" {
		t.Fatalf("BuildLine with args = %q", got)
	}
	if got := BuildLine("ps", nil); got != "ps" {
		t.Fatalf("BuildLine no args = %q, want %q", got, "ps")
	}
}

// knownBuildCommands mirrors the repl dispatch names — duplicated locally
// because cmd cannot import repl (that would be an import cycle). Keep in
// sync with dispatchNames in internal/repl/repl.go.
var knownBuildCommands = map[string]bool{
	"help": true, "clear": true, "copy-last": true, "report": true,
	"init": true, "reset": true, "alias": true,
	"up": true, "down": true, "stop": true, "restart": true, "ps": true, "logs": true,
	"install": true, "update": true, "uninstall": true, "test": true,
	"modules": true, "modinfo": true, "view": true,
	"i18n-export": true, "i18n-update": true, "i18n-pull": true,
	"db-backup": true, "db-restore": true, "db-drop": true,
	"db-neutralize": true, "db-list": true,
	"shell": true, "bash": true, "psql": true, "connect": true,
}

// TestBuildRegistriesKnownCommands guards against typos: every command
// that has a positional or flag-value spec must be a real routed command.
func TestBuildRegistriesKnownCommands(t *testing.T) {
	for _, c := range BuildCommands() {
		if !knownBuildCommands[c] {
			t.Errorf("build registry references unknown command %q", c)
		}
	}
	// Spot-check the two registries individually too.
	for c := range buildPositionals {
		if !knownBuildCommands[c] {
			t.Errorf("buildPositionals: unknown command %q", c)
		}
	}
	for c := range buildFlagValues {
		if !knownBuildCommands[c] {
			t.Errorf("buildFlagValues: unknown command %q", c)
		}
	}
}
