package repl

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestRunLogSinkTee(t *testing.T) {
	var buf strings.Builder
	runLogSink = &buf
	defer func() { runLogSink = nil }()

	teeRunLog("plain step output")
	if got := buf.String(); got != "plain step output\n" {
		t.Fatalf("teeRunLog wrote %q", got)
	}

	// plainOdooLogFields must carry the structured fields and no ANSI.
	line := plainOdooLogFields("ERROR", "echo.run", "stopped at step 2/3",
		[]logField{{"exit", "1"}}, "mydb")
	if strings.Contains(line, "\x1b[") {
		t.Errorf("plain line contains ANSI escape: %q", line)
	}
	for _, want := range []string{"ERRO", "echo.run:", "stopped at step 2/3", "exit=1", "mydb"} {
		if !strings.Contains(line, want) {
			t.Errorf("plain line %q missing %q", line, want)
		}
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
		args       []string
		path       string
		cont       bool
		logDest    string
		logEnabled bool
		pick       bool
		wantErr    bool
	}{
		{[]string{"update.echo"}, "update.echo", false, "", false, false, false},
		{[]string{"-"}, "-", false, "", false, false, false},
		{[]string{}, "", false, "", false, false, false},
		{[]string{"r.echo", "--continue-on-error"}, "r.echo", true, "", false, false, false},
		{[]string{"--continue-on-error", "r.echo"}, "r.echo", true, "", false, false, false},
		{[]string{"r.echo", "--log"}, "r.echo", false, "", true, false, false},
		{[]string{"--log=/tmp/x.log", "r.echo"}, "r.echo", false, "/tmp/x.log", true, false, false},
		{[]string{"r.echo", "--log", "--continue-on-error"}, "r.echo", true, "", true, false, false},
		{[]string{"--pick"}, "", false, "", false, true, false},
		{[]string{"--pick", "--log"}, "", false, "", true, true, false},
		{[]string{"--pick", "--continue-on-error"}, "", true, "", false, true, false},
		{[]string{"--pick", "r.echo"}, "", false, "", false, false, true},
		{[]string{"--bogus"}, "", false, "", false, false, true},
		{[]string{"a.echo", "b.echo"}, "", false, "", false, false, true},
	}
	for _, c := range cases {
		path, cont, logDest, logEnabled, pick, err := parseRecipeArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("parseRecipeArgs(%v) err = %v, wantErr %v", c.args, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if path != c.path || cont != c.cont || logDest != c.logDest || logEnabled != c.logEnabled || pick != c.pick {
			t.Errorf("parseRecipeArgs(%v) = (%q, %v, %q, %v, %v), want (%q, %v, %q, %v, %v)",
				c.args, path, cont, logDest, logEnabled, pick, c.path, c.cont, c.logDest, c.logEnabled, c.pick)
		}
	}
}

func TestEchoRecipesIn(t *testing.T) {
	dir := t.TempDir()
	// Two .echo recipes, plus noise that must be ignored.
	for _, f := range []string{"deploy.echo", "backup.echo", "notes.txt", "README"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("up\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.echo"), 0o700); err != nil {
		t.Fatal(err) // a directory named *.echo must not be listed
	}

	got, err := echoRecipesIn(dir)
	if err != nil {
		t.Fatalf("echoRecipesIn: %v", err)
	}
	want := []string{"backup.echo", "deploy.echo"} // sorted, files only
	if !reflect.DeepEqual(got, want) {
		t.Errorf("echoRecipesIn = %v, want %v", got, want)
	}
}

func TestResolveLogDest(t *testing.T) {
	tmp := t.TempDir()

	// Empty → default location (caller resolves).
	if got := resolveLogDest("", "deploy.echo"); got != "" {
		t.Errorf("empty dest = %q, want \"\"", got)
	}
	// An existing directory → <dir>/<recipe>.log.
	if got, want := resolveLogDest(tmp, "/x/deploy.echo"), filepath.Join(tmp, "deploy.log"); got != want {
		t.Errorf("dir dest = %q, want %q", got, want)
	}
	// "." (current dir) → ./<recipe>.log (relative).
	if got := resolveLogDest(".", "deploy.echo"); got != "deploy.log" {
		t.Errorf("dot dest = %q, want %q", got, "deploy.log")
	}
	// A non-directory value is an explicit file path, unchanged.
	if got := resolveLogDest("out.log", "deploy.echo"); got != "out.log" {
		t.Errorf("file dest = %q, want %q", got, "out.log")
	}
}

func TestStripSilent(t *testing.T) {
	cases := []struct {
		args     []string
		clean    []string
		suppress int
		label    string
		bad      string
	}{
		{[]string{"sale"}, []string{"sale"}, -1, "", ""},
		{[]string{"sale", "--silent"}, []string{"sale"}, silentAll, "all", ""},
		{[]string{"--silent", "sale", "account"}, []string{"sale", "account"}, silentAll, "all", ""},
		{[]string{"sale", "--silent=info"}, []string{"sale"}, levelRank["INFO"], "info", ""},
		{[]string{"--silent=warn", "sale"}, []string{"sale"}, levelRank["WARNING"], "warning", ""},
		{[]string{"sale", "--silent=bogus"}, []string{"sale"}, -1, "", "bogus"},
	}
	for _, c := range cases {
		clean, suppress, label, bad := stripSilent(c.args)
		if !reflect.DeepEqual(clean, c.clean) || suppress != c.suppress || label != c.label || bad != c.bad {
			t.Errorf("stripSilent(%v) = (%v, %d, %q, %q), want (%v, %d, %q, %q)",
				c.args, clean, suppress, label, bad, c.clean, c.suppress, c.label, c.bad)
		}
	}
}

func TestOutputSuppressed(t *testing.T) {
	defer func() { suppressLevel = -1 }()

	// Inactive: nothing is suppressed.
	suppressLevel = -1
	for _, lvl := range []string{"", "INFO", "WARNING", "ERROR", "CRITICAL"} {
		if outputSuppressed(lvl) {
			t.Errorf("inactive: %q should not be suppressed", lvl)
		}
	}
	// --silent (all): everything, including plain ("") and CRITICAL.
	suppressLevel = silentAll
	for _, lvl := range []string{"", "INFO", "WARNING", "CRITICAL"} {
		if !outputSuppressed(lvl) {
			t.Errorf("silent=all: %q should be suppressed", lvl)
		}
	}
	// --silent=info: DEBUG/INFO/plain suppressed, WARNING+ shown.
	suppressLevel = levelRank["INFO"]
	for _, lvl := range []string{"", "DEBUG", "INFO"} {
		if !outputSuppressed(lvl) {
			t.Errorf("silent=info: %q should be suppressed", lvl)
		}
	}
	for _, lvl := range []string{"WARNING", "ERROR", "CRITICAL"} {
		if outputSuppressed(lvl) {
			t.Errorf("silent=info: %q should NOT be suppressed", lvl)
		}
	}
}

func TestEchoRecipesInEmpty(t *testing.T) {
	got, err := echoRecipesIn(t.TempDir())
	if err != nil {
		t.Fatalf("echoRecipesIn: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no recipes, got %v", got)
	}
}

func TestRunRecipeStepsFailFast(t *testing.T) {
	steps := []string{"stop", "update bad", "restart"}
	var ran []string
	runStep := func(name string, args []string, _ int) stepOutcome {
		ran = append(ran, name)
		if name == "update" {
			return stepOutcome{code: exitError}
		}
		return stepOutcome{code: exitOK}
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
	runStep := func(name string, args []string, _ int) stepOutcome {
		ran = append(ran, name)
		if name == "update" {
			return stepOutcome{code: exitError}
		}
		return stepOutcome{code: exitOK}
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
	runStep := func(name string, args []string, _ int) stepOutcome { return stepOutcome{code: exitOK} }
	if code := runRecipeSteps(steps, false, runStep, func(string, string, ...logField) {}); code != exitOK {
		t.Errorf("all-pass exit = %d, want %d", code, exitOK)
	}
}

// logCall captures one emitted log line for assertions.
type logCall struct {
	level  string
	msg    string
	fields map[string]string
}

func captureLog(calls *[]logCall) func(string, string, ...logField) {
	return func(level, msg string, fields ...logField) {
		m := map[string]string{}
		for _, f := range fields {
			m[f.key] = f.value
		}
		*calls = append(*calls, logCall{level: level, msg: msg, fields: m})
	}
}

func findCall(calls []logCall, msg string) (logCall, bool) {
	for _, c := range calls {
		if c.msg == msg {
			return c, true
		}
	}
	return logCall{}, false
}

func TestRunRecipeStepsSummaryFailFast(t *testing.T) {
	steps := []string{"stop", "update bad", "restart"}
	runStep := func(name string, args []string, _ int) stepOutcome {
		if name == "update" {
			return stepOutcome{code: exitError, errors: 2, warnings: 1}
		}
		if name == "stop" {
			return stepOutcome{code: exitOK, warnings: 3}
		}
		return stepOutcome{code: exitOK}
	}
	var calls []logCall
	runRecipeSteps(steps, false, runStep, captureLog(&calls))

	// Step 1 ran ok with warnings; recap line present at INFO.
	if c, ok := findCall(calls, "step 1/3 ok"); !ok {
		t.Error("missing recap for step 1")
	} else if c.level != "INFO" || c.fields["cmd"] != "stop" || c.fields["warnings"] != "3" {
		t.Errorf("step 1 recap = %+v", c)
	}
	// Step 2 failed: ERROR, exit + errors fields.
	if c, ok := findCall(calls, "step 2/3 failed"); !ok {
		t.Error("missing recap for failed step 2")
	} else if c.level != "ERROR" || c.fields["exit"] != strconv.Itoa(exitError) || c.fields["errors"] != "2" {
		t.Errorf("step 2 recap = %+v", c)
	}
	// Step 3 never ran → skipped, WARNING, no took.
	if c, ok := findCall(calls, "step 3/3 skipped"); !ok {
		t.Error("missing recap for skipped step 3")
	} else if c.level != "WARNING" || c.fields["cmd"] != "restart" {
		t.Errorf("step 3 recap = %+v", c)
	} else if _, has := c.fields["took"]; has {
		t.Error("skipped step must not carry a took field")
	}
	// Totals line: failed=1, skipped=1, errors=2 (from the failed step),
	// warnings total = 4 (3 on step 1 + 1 on step 2), ERROR.
	if c, ok := findCall(calls, "run summary"); !ok {
		t.Error("missing run summary")
	} else if c.level != "ERROR" || c.fields["steps"] != "3" || c.fields["failed"] != "1" ||
		c.fields["skipped"] != "1" || c.fields["ok"] != "1" ||
		c.fields["errors"] != "2" || c.fields["warnings"] != "4" {
		t.Errorf("totals = %+v", c)
	}
}

func TestRunRecipeStepsSummaryAllPass(t *testing.T) {
	steps := []string{"stop", "up"}
	runStep := func(name string, args []string, _ int) stepOutcome { return stepOutcome{code: exitOK} }
	var calls []logCall
	runRecipeSteps(steps, false, runStep, captureLog(&calls))

	c, ok := findCall(calls, "run summary")
	if !ok {
		t.Fatal("missing run summary")
	}
	if c.level != "INFO" || c.fields["steps"] != "2" || c.fields["ok"] != "2" ||
		c.fields["errors"] != "0" || c.fields["warnings"] != "0" {
		t.Errorf("totals = %+v", c)
	}
	if _, has := c.fields["failed"]; has {
		t.Error("clean run must not carry a failed field")
	}
}
