package repl

import (
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/cmd"
)

func TestStripBuildFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantClean []string
		wantBuild bool
	}{
		{"long form alone", []string{"--build"}, nil, true},
		{"short form alone", []string{"-b"}, nil, true},
		{"with other args keeps them", []string{"sale", "--build", "--level"}, []string{"sale", "--level"}, true},
		{"short in the middle", []string{"sale", "-b", "account"}, []string{"sale", "account"}, true},
		{"no flag", []string{"sale", "--all"}, []string{"sale", "--all"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clean, build := stripBuildFlag(tc.args)
			if build != tc.wantBuild {
				t.Errorf("build = %v, want %v", build, tc.wantBuild)
			}
			if !reflect.DeepEqual(clean, tc.wantClean) {
				t.Errorf("clean = %#v, want %#v", clean, tc.wantClean)
			}
		})
	}
}

func TestBuildFlagsDropsAliases(t *testing.T) {
	got := buildFlags("logs")
	for _, f := range got {
		if f == "-c" {
			t.Errorf("buildFlags(logs) must drop -c (alias of --copy); got %#v", got)
		}
	}
	// Order is preserved and --copy survives.
	if !reflect.DeepEqual(got, []string{"-t", "--no-follow", "--copy", "--all"}) {
		t.Errorf("buildFlags(logs) = %#v", got)
	}
	// A command without aliases is returned verbatim.
	if got := buildFlags("update"); !reflect.DeepEqual(got, commandFlags["update"]) {
		t.Errorf("buildFlags(update) = %#v, want %#v", got, commandFlags["update"])
	}
}

// TestBuildValueFlagsExist guards against typos: every flag with a
// build-mode value spec must be a declared flag of that command.
func TestBuildValueFlagsExist(t *testing.T) {
	for _, c := range cmd.BuildCommands() {
		for _, f := range cmd.BuildValueFlags(c) {
			if !isKnownFlag(c, f) {
				t.Errorf("buildFlagValues[%q] has %q, absent from commandFlags[%q]", c, f, c)
			}
		}
	}
}
