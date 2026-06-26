package repl

import (
	"reflect"
	"testing"
)

func TestIsFollowLogs(t *testing.T) {
	cases := []struct {
		step string
		want bool
	}{
		{"logs", true},
		{"logs odoo", true},
		{"logs --from=prod", true},
		{"logs --no-follow", false},
		{"logs --copy", false},
		{"logs -c", false},
		{"logs odoo --no-follow -t 50", false},
		{"update --all", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isFollowLogs(c.step); got != c.want {
			t.Errorf("isFollowLogs(%q) = %v, want %v", c.step, got, c.want)
		}
	}
}

func TestReorderLogsLast(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantOut    []string
		wantFollow bool
	}{
		{
			name:       "logs moved to end",
			in:         []string{"update --all", "logs", "test sale"},
			wantOut:    []string{"update --all", "test sale", "logs"},
			wantFollow: true,
		},
		{
			name:       "no-follow logs stays put",
			in:         []string{"logs --no-follow", "update --all"},
			wantOut:    []string{"logs --no-follow", "update --all"},
			wantFollow: false,
		},
		{
			name:       "no logs unchanged",
			in:         []string{"update --all", "test sale"},
			wantOut:    []string{"update --all", "test sale"},
			wantFollow: false,
		},
		{
			name:       "already last",
			in:         []string{"update --all", "logs"},
			wantOut:    []string{"update --all", "logs"},
			wantFollow: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, follow := reorderLogsLast(c.in)
			if !reflect.DeepEqual(out, c.wantOut) {
				t.Errorf("out = %v, want %v", out, c.wantOut)
			}
			if follow != c.wantFollow {
				t.Errorf("follow = %v, want %v", follow, c.wantFollow)
			}
		})
	}
}

func TestBakeRemote(t *testing.T) {
	cases := []struct {
		command string
		line    string
		from    string
		remote  bool
		want    string
	}{
		{"logs", "logs", "prod", false, "logs --from=prod"},
		{"restart", "restart odoo", "", true, "restart odoo --remote"},
		{"logs", "logs --from=prod", "prod", false, "logs --from=prod"},
		// deploy doesn't accept --remote: in link mode it gets no flag and
		// defaults to the project's [connect] binding.
		{"deploy", "deploy --commits=a1b2", "", true, "deploy --commits=a1b2"},
		// deploy with a named target still gets --from.
		{"deploy", "deploy --commits=a1b2", "prod", false, "deploy --commits=a1b2 --from=prod"},
		// i18n-pull likewise has no --remote.
		{"i18n-pull", "i18n-pull sale", "", true, "i18n-pull sale"},
		{"logs", "logs", "", false, "logs"},
	}
	for _, c := range cases {
		if got := bakeRemote(c.command, c.line, c.from, c.remote); got != c.want {
			t.Errorf("bakeRemote(%q,%q,%q,%v) = %q, want %q", c.command, c.line, c.from, c.remote, got, c.want)
		}
	}
}

// TestSequenceCommandsKnown guards the allowlists against typos: every
// sequenceable command must be a real Registry command, and every
// remote-sequenceable command must additionally accept --from.
func TestSequenceCommandsKnown(t *testing.T) {
	known := map[string]bool{}
	for _, n := range Registry {
		known[n] = true
	}
	for _, n := range sequenceCommands {
		if !known[n] {
			t.Errorf("sequenceCommands has unknown command %q", n)
		}
	}
	for _, n := range remoteSequenceCommands {
		if !known[n] {
			t.Errorf("remoteSequenceCommands has unknown command %q", n)
			continue
		}
		hasFrom := false
		for _, f := range commandFlags[n] {
			if f == "--from" {
				hasFrom = true
				break
			}
		}
		if !hasFrom {
			t.Errorf("remoteSequenceCommands %q does not accept --from", n)
		}
	}
}

// TestHelpDescByName covers a couple of commands the sequence picker relies
// on for its secondary column.
func TestHelpDescByName(t *testing.T) {
	desc := helpDescByName()
	if desc["update"] == "" {
		t.Error("expected a description for update")
	}
	if desc["logs"] == "" {
		t.Error("expected a description for logs")
	}
}
