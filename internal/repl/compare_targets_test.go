package repl

import (
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/cmd"
)

func targetsResult(rows ...cmd.TargetRow) cmd.CompareTargetsResult {
	return cmd.CompareTargetsResult{
		A:       cmd.TargetSide{Name: "dev", LockState: "found"},
		B:       cmd.TargetSide{Name: "staging", LockState: "found"},
		Modules: rows,
		Counts:  cmd.TargetCounts{Same: 1, Differs: 1, OnlyA: 1},
	}
}

func renderTargets(t *testing.T, res cmd.CompareTargetsResult) (table string, logs []string) {
	t.Helper()
	sess := newRenderSession()
	logFn := func(level, sub, msg, db string, fields ...[2]string) {
		line := level + " " + sub + " " + msg
		for _, f := range fields {
			line += " " + f[0] + "=" + f[1]
		}
		logs = append(logs, line)
	}
	table = captureStdout(t, func() { sess.renderCompareTargets(res, logFn) })
	return table, logs
}

func TestRenderCompareTargetsHidesSame(t *testing.T) {
	res := targetsResult(
		cmd.TargetRow{Name: "flow", Status: "differs", Newer: "dev",
			A: &cmd.LockModule{Source: "ref", SHA: "99f2109aa", Version: "1.4.0", Verified: true},
			B: &cmd.LockModule{Source: "commit", SHA: "a1c4e02bb", Version: "1.3.0", Verified: true}},
		cmd.TargetRow{Name: "crm", Status: "only", Only: "dev",
			A: &cmd.LockModule{Source: "worktree", SHA: "0b6fc41cc", Dirty: true, Version: "1.2.0"}},
		cmd.TargetRow{Name: "quiet", Status: "same",
			A: &cmd.LockModule{Source: "ref", SHA: "1"}, B: &cmd.LockModule{Source: "ref", SHA: "1"}},
	)
	table, logs := renderTargets(t, res)
	for _, want := range []string{"module", "staging", "ref@99f2109 1.4.0", "commit@a1c4e02 1.3.0",
		"only dev", "worktree@0b6fc41+dirty 1.2.0 unv.", "none"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "quiet") {
		t.Errorf("a same row was printed:\n%s", table)
	}
	want := "INFO targets targets compared a=dev b=staging same=1 differs=1 unknown=0 only_dev=1 only_staging=0"
	if len(logs) != 1 || logs[0] != want {
		t.Errorf("closing line = %q, want %q", logs, want)
	}
}

func TestRenderCompareTargetsAllSame(t *testing.T) {
	same := cmd.TargetRow{Name: "quiet", Status: "same",
		A: &cmd.LockModule{Source: "ref", SHA: "1"}, B: &cmd.LockModule{Source: "ref", SHA: "1"}}
	table, logs := renderTargets(t, targetsResult(same))
	if strings.TrimSpace(table) != "" || len(logs) != 1 {
		t.Fatalf("all same printed a table:\n%s\n%v", table, logs)
	}

	same.Named = true
	table, _ = renderTargets(t, targetsResult(same))
	if !strings.Contains(table, "quiet") {
		t.Fatalf("a module named as a positional was hidden:\n%s", table)
	}
}
