package repl

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRecipeLines(t *testing.T) {
	in := `# update the instance
stop

   db-backup            # safety net before touching anything
up
# a full-line comment
update ventas contabilidad   # 8d7a7e0 — label dup
   # indented comment
`
	got, err := parseRecipeLines(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseRecipeLines: %v", err)
	}
	want := []string{"stop", "db-backup", "up", "update ventas contabilidad"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

func TestStripComment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"update sale", "update sale"},
		{"update sale # a fix", "update sale "},
		{"update sale\t# tabbed", "update sale\t"},
		{"# full line", ""},
		{"   # indented", "   "},
		{"db-restore --as 2", "db-restore --as 2"},
		{"foo#bar", "foo#bar"}, // # not preceded by whitespace → kept
	}
	for _, c := range cases {
		if got := stripComment(c.in); got != c.want {
			t.Errorf("stripComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseRecipeArgs(t *testing.T) {
	cases := []struct {
		args    []string
		path    string
		cont    bool
		wantErr bool
	}{
		{[]string{"update.echo"}, "update.echo", false, false},
		{[]string{"-"}, "-", false, false},
		{[]string{}, "", false, false},
		{[]string{"r.echo", "--continue-on-error"}, "r.echo", true, false},
		{[]string{"--continue-on-error", "r.echo"}, "r.echo", true, false},
		{[]string{"--bogus"}, "", false, true},
		{[]string{"a.echo", "b.echo"}, "", false, true},
	}
	for _, c := range cases {
		path, cont, err := parseRecipeArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("parseRecipeArgs(%v) err = %v, wantErr %v", c.args, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if path != c.path || cont != c.cont {
			t.Errorf("parseRecipeArgs(%v) = (%q, %v), want (%q, %v)", c.args, path, cont, c.path, c.cont)
		}
	}
}

func TestRunRecipeStepsFailFast(t *testing.T) {
	steps := []string{"stop", "update bad", "restart"}
	var ran []string
	runStep := func(name string, args []string) int {
		ran = append(ran, name)
		if name == "update" {
			return exitError
		}
		return exitOK
	}
	code := runRecipeSteps(steps, false, runStep, func(string, string, ...logField) {})

	if code != exitError {
		t.Errorf("fail-fast exit = %d, want %d", code, exitError)
	}
	if !reflect.DeepEqual(ran, []string{"stop", "update"}) {
		t.Errorf("ran = %v, want stop then update only (restart must be skipped)", ran)
	}
}

func TestRunRecipeStepsContinueOnError(t *testing.T) {
	steps := []string{"stop", "update bad", "restart"}
	var ran []string
	runStep := func(name string, args []string) int {
		ran = append(ran, name)
		if name == "update" {
			return exitError
		}
		return exitOK
	}
	code := runRecipeSteps(steps, true, runStep, func(string, string, ...logField) {})

	if code != exitError {
		t.Errorf("continue-on-error exit = %d, want %d (a step failed)", code, exitError)
	}
	if len(ran) != 3 {
		t.Errorf("ran = %v, want all 3 steps", ran)
	}
}

func TestRunRecipeStepsAllPass(t *testing.T) {
	steps := []string{"stop", "up", "restart"}
	runStep := func(name string, args []string) int { return exitOK }
	if code := runRecipeSteps(steps, false, runStep, func(string, string, ...logField) {}); code != exitOK {
		t.Errorf("all-pass exit = %d, want %d", code, exitOK)
	}
}
