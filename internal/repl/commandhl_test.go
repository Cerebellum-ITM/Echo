package repl

import "testing"

func TestClassifyCommand(t *testing.T) {
	cases := []struct {
		token string
		want  cmdState
	}{
		{"", cmdTyping},          // empty buffer
		{"install", cmdValid},    // exact command
		{"db-list", cmdValid},    // exact (hyphenated) command
		{"exit", cmdValid},       // handled in Start, still valid
		{"quit", cmdValid},       // ditto
		{"ins", cmdTyping},       // prefix of install
		{"db-", cmdTyping},       // prefix of db-backup/db-restore/...
		{"e", cmdTyping},         // prefix of exit (not in Registry)
		{"exi", cmdTyping},       // prefix of exit
		{"xyz", cmdInvalid},      // cannot become any command
		{"installx", cmdInvalid}, // past an exact command, no longer a prefix
		{"connectt", cmdInvalid}, // typo past a real command
	}
	for _, c := range cases {
		if got := classifyCommand(c.token); got != c.want {
			t.Errorf("classifyCommand(%q) = %d, want %d", c.token, got, c.want)
		}
	}
}

func TestFirstToken(t *testing.T) {
	cases := []struct {
		buf, token, rest string
	}{
		{"install", "install", ""},
		{"install sale", "install", " sale"},
		{"update --all", "update", " --all"},
		{"  leadingspace", "", "  leadingspace"},
		{"", "", ""},
	}
	for _, c := range cases {
		tok, rest := firstToken(c.buf)
		if tok != c.token || rest != c.rest {
			t.Errorf("firstToken(%q) = (%q, %q), want (%q, %q)", c.buf, tok, rest, c.token, c.rest)
		}
	}
}

// Every command the REPL dispatches (and exit/quit) must classify as valid,
// guarding against commandSet drifting from Registry.
func TestAllRegistryCommandsValid(t *testing.T) {
	for _, name := range Registry {
		if classifyCommand(name) != cmdValid {
			t.Errorf("Registry command %q does not classify as valid", name)
		}
	}
	for _, name := range []string{"exit", "quit"} {
		if classifyCommand(name) != cmdValid {
			t.Errorf("%q does not classify as valid", name)
		}
	}
}

func TestClassifyFlag(t *testing.T) {
	cases := []struct {
		command, token string
		want           flagState
	}{
		{"db-restore", "--force", flagKnown},
		{"db-restore", "--as", flagKnown},
		{"update", "--all", flagKnown},
		{"test", "--tags=:TestX.test_y", flagKnown}, // value part ignored
		{"logs", "-t", flagKnown},
		{"update", "--nope", flagUnknown},    // not a flag of update
		{"db-restore", "--all", flagUnknown}, // valid elsewhere, not here
		{"down", "--whatever", flagUnknown},  // passthrough → unknown, not error
		{"bash", "--anything", flagUnknown},  // command with no declared flags
		// Universal flags classify as known on any command (Unit 51).
		{"update", "--build", flagKnown},
		{"bash", "--build", flagKnown},
		{"ps", "-b", flagKnown},
		{"db-restore", "-b", flagKnown},
	}
	for _, c := range cases {
		if got := classifyFlag(c.command, c.token); got != c.want {
			t.Errorf("classifyFlag(%q, %q) = %d, want %d", c.command, c.token, got, c.want)
		}
	}
}

func TestFlagsWithPrefix(t *testing.T) {
	// Command flags come first (declaration order), then any matching
	// universal flag (Unit 51): `--build` matches the `--` prefix.
	got := flagsWithPrefix("db-restore", "--")
	want := []string{"--as", "--force", "--neutralize", "--build"}
	if len(got) != len(want) {
		t.Fatalf("flagsWithPrefix(db-restore, --) = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("flagsWithPrefix(db-restore, --)[%d] = %q, want %q", i, got[i], w)
		}
	}
	if g := flagsWithPrefix("db-restore", "--f"); len(g) != 1 || g[0] != "--force" {
		t.Errorf("flagsWithPrefix(db-restore, --f) = %v, want [--force]", g)
	}
	// A command with no declared flags still completes the universal ones.
	if g := flagsWithPrefix("ps", "-"); len(g) != 2 || g[0] != "--build" || g[1] != "-b" {
		t.Errorf("flagsWithPrefix(ps, -) = %v, want [--build -b]", g)
	}
	if g := flagsWithPrefix("ps", "-b"); len(g) != 1 || g[0] != "-b" {
		t.Errorf("flagsWithPrefix(ps, -b) = %v, want [-b]", g)
	}
}

// Guard: every command that declares flags is a real REPL command.
func TestCommandFlagsKeysAreCommands(t *testing.T) {
	for cmd := range commandFlags {
		if !isCommandName(cmd) {
			t.Errorf("commandFlags key %q is not a known command", cmd)
		}
	}
}
