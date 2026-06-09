package repl

import "testing"

func TestClassifyCommand(t *testing.T) {
	cases := []struct {
		token string
		want  cmdState
	}{
		{"", cmdTyping},            // empty buffer
		{"install", cmdValid},      // exact command
		{"db-list", cmdValid},      // exact (hyphenated) command
		{"exit", cmdValid},         // handled in Start, still valid
		{"quit", cmdValid},         // ditto
		{"ins", cmdTyping},         // prefix of install
		{"db-", cmdTyping},         // prefix of db-backup/db-restore/...
		{"e", cmdTyping},           // prefix of exit (not in Registry)
		{"exi", cmdTyping},         // prefix of exit
		{"xyz", cmdInvalid},        // cannot become any command
		{"installx", cmdInvalid},   // past an exact command, no longer a prefix
		{"connectt", cmdInvalid},   // typo past a real command
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
