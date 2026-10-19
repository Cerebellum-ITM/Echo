package repl

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

var undocumentedFlags = map[string]bool{"-E": true, "--env": true}

func TestCommandDocsFollowRegistry(t *testing.T) {
	var want []string
	for _, name := range Registry {
		if name != "quit" {
			want = append(want, name)
		}
	}
	var got []string
	for _, d := range commandDocs {
		got = append(got, d.name)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("commandDocs order mismatch\nregistry: %v\ndocs:     %v", want, got)
	}
	if _, ok := docFor("quit"); !ok {
		t.Fatal("help quit has no page")
	}
}

func TestHelpTopicsDoNotShadowCommands(t *testing.T) {
	for _, topic := range helpTopics {
		for _, name := range Registry {
			if topic.name == name {
				t.Errorf("topic %q collides with a command", topic.name)
			}
		}
	}
}

func TestCommandDocsCoverFlags(t *testing.T) {
	for _, d := range commandDocs {
		documented := map[string]bool{}
		for _, it := range append(append([]helpItem{}, d.subs...), d.flags...) {
			if strings.ContainsAny(it.name, "/ ") {
				t.Errorf("%s: item name %q must be one token", d.name, it.name)
			}
		}
		for _, f := range d.flags {
			if documented[f.name] {
				t.Errorf("%s: flag %s documented twice", d.name, f.name)
			}
			documented[f.name] = true
		}
		known := map[string]bool{}
		for _, f := range commandFlags[d.name] {
			known[f] = true
			if !undocumentedFlags[f] && !documented[f] {
				t.Errorf("%s: flag %s is in commandFlags but not documented", d.name, f)
			}
		}
		for f := range documented {
			if !known[f] {
				t.Errorf("%s: documented flag %s is not in commandFlags", d.name, f)
			}
			if undocumentedFlags[f] {
				t.Errorf("%s: deprecated flag %s must not be documented per command", d.name, f)
			}
		}
	}
}

func TestCommandDocsRequiredNotesAndExamples(t *testing.T) {
	for _, d := range commandDocs {
		required := d.name == "promote" || d.name == "sequence"
		for _, f := range commandFlags[d.name] {
			if f == "--from" || f == "--remote" || f == "--json" {
				required = true
			}
		}
		if !required {
			continue
		}
		if len(d.notes) == 0 {
			t.Errorf("%s: notes are required", d.name)
		}
		if len(d.examples) == 0 || len(d.examples) > 3 {
			t.Errorf("%s: needs 1 to 3 examples, has %d", d.name, len(d.examples))
		}
	}
}

func TestCommandDocExamplesUseKnownFlags(t *testing.T) {
	for _, d := range commandDocs {
		known := map[string]bool{}
		for _, f := range commandFlags[d.name] {
			known[f] = true
		}
		for _, f := range universalFlags {
			known[f] = true
		}
		for _, ex := range d.examples {
			fields := strings.Fields(ex.cmd)
			if len(fields) == 0 || fields[0] != d.name {
				t.Errorf("%s: example %q must start with the command name", d.name, ex.cmd)
				continue
			}
			for _, tok := range fields[1:] {
				if !strings.HasPrefix(tok, "-") {
					continue
				}
				flag, _, _ := strings.Cut(tok, "=")
				if !known[flag] {
					t.Errorf("%s: example %q uses unknown flag %s", d.name, ex.cmd, flag)
				}
			}
		}
	}
}

func TestHelpRequest(t *testing.T) {
	cases := []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{"deploy", "--help"}, "deploy", true},
		{[]string{"deploy", "--from", "x", "--help"}, "deploy", true},
		{[]string{"deploy", "-h"}, "deploy", true},
		{[]string{"deploy", "-h", "x"}, "", false},
		{[]string{"deploy", "--", "--help"}, "", false},
		{[]string{"shell-run", "x", "--help"}, "", false},
		{[]string{"psql", "--help"}, "psql", true},
		{[]string{"quit", "--help"}, "quit", true},
		{[]string{"help", "--help"}, "", false},
		{[]string{"nope", "--help"}, "", false},
		{[]string{"deploy"}, "", false},
	}
	for _, tc := range cases {
		got, ok := HelpRequest(tc.args)
		if got != tc.want || ok != tc.ok {
			t.Errorf("HelpRequest(%v) = %q, %v; want %q, %v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}

func runHelpCaptured(t *testing.T, args ...string) (string, int) {
	t.Helper()
	origIn, origOut := stdinIsTTY, stdoutIsTTY
	defer func() { stdinIsTTY, stdoutIsTTY = origIn, origOut }()
	stdinIsTTY = func() bool { return false }
	stdoutIsTTY = func() bool { return false }
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan []byte)
	go func() {
		out, _ := io.ReadAll(r)
		read <- out
	}()
	orig := os.Stdout
	os.Stdout = w
	sess := newRenderSession()
	sess.runHelp(args)
	w.Close()
	os.Stdout = orig
	return string(<-read), sess.exitCode
}

func TestHelpJSONShapes(t *testing.T) {
	out, code := runHelpCaptured(t, "--json")
	if code != exitOK {
		t.Fatalf("help --json exit %d", code)
	}
	var index map[string]any
	if err := json.Unmarshal([]byte(out), &index); err != nil {
		t.Fatalf("help --json is not one JSON document: %v", err)
	}
	if n := len(index["commands"].([]any)); n != len(Registry)-1 {
		t.Errorf("help --json has %d commands, want %d", n, len(Registry)-1)
	}

	out, _ = runHelpCaptured(t, "checkpoint", "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("help checkpoint --json: %v", err)
	}
	for _, key := range []string{"subcommands", "flags", "notes", "examples"} {
		if _, isList := doc[key].([]any); !isList {
			t.Errorf("help checkpoint --json: %s is not a list", key)
		}
	}
	if doc["remote"] != true || doc["json_output"] != true {
		t.Errorf("help checkpoint --json: remote/json_output = %v/%v", doc["remote"], doc["json_output"])
	}

	out, _ = runHelpCaptured(t, "clear", "--json")
	if strings.Contains(out, "null") {
		t.Errorf("help clear --json has a null list: %s", out)
	}

	out, _ = runHelpCaptured(t, "agents", "--json")
	var topic helpTopicJSON
	if err := json.Unmarshal([]byte(out), &topic); err != nil || topic.Topic != "agents" || len(topic.Rows) == 0 {
		t.Errorf("help agents --json = %+v, %v", topic, err)
	}
}

func TestHelpSearch(t *testing.T) {
	var deployFlag, command bool
	for _, m := range searchHelp("checkpoint") {
		if m.Command == "deploy" && m.Item == "--no-checkpoint" {
			deployFlag = true
		}
		if m.Command == "checkpoint" && m.Item == "" {
			command = true
		}
	}
	if !deployFlag || !command {
		t.Errorf("search checkpoint: deploy --no-checkpoint=%v, checkpoint=%v", deployFlag, command)
	}
	if _, code := runHelpCaptured(t, "zzz"); code != exitUsage {
		t.Errorf("help zzz exit %d, want %d", code, exitUsage)
	}
	if _, code := runHelpCaptured(t, "deploy", "push"); code != exitUsage {
		t.Errorf("help with two arguments exit %d, want %d", code, exitUsage)
	}
}

func TestHelpFlatIndexIsShort(t *testing.T) {
	out, _ := runHelpCaptured(t)
	if n := strings.Count(out, "\n"); n > 80 {
		t.Errorf("flat help index has %d lines, want <= 80", n)
	}
}
