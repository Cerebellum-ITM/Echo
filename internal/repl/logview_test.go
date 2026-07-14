package repl

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// it wrote — the headless JSON path writes straight to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestLogviewPrintJSON(t *testing.T) {
	sess := cmdLogSession(t)

	metas := []config.CmdLogMeta{
		{Command: "watch-deploy", Cmd: "deploy --commits abc123", Stage: "staging",
			Exit: 0, DeployedTip: "abc123def", Started: time.Now()},
		{Command: "update", Cmd: "update sale", Stage: "dev", Exit: 1, Started: time.Now()},
	}
	out := captureStdout(t, func() { sess.logviewPrintJSON(metas) })

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0]["command"] != "watch-deploy" || got[0]["deployed_tip"] != "abc123def" {
		t.Fatalf("agent-facing keys wrong: %+v", got[0])
	}
	// The non-watch record omits deployed_tip entirely (omitempty), and never
	// leaks the local record path.
	if _, ok := got[1]["deployed_tip"]; ok {
		t.Fatalf("deployed_tip should be omitted on non-watch record: %+v", got[1])
	}
	if _, ok := got[0]["Path"]; ok {
		t.Fatalf("local record Path must not be serialized: %+v", got[0])
	}
	if sess.exitCode != exitOK {
		t.Fatalf("exit = %d, want exitOK", sess.exitCode)
	}
}

func TestLogviewPrintJSONEmpty(t *testing.T) {
	sess := cmdLogSession(t)
	out := captureStdout(t, func() { sess.logviewPrintJSON(nil) })
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty history should print []: %q", out)
	}
	if sess.exitCode != exitOK {
		t.Fatalf("exit = %d, want exitOK", sess.exitCode)
	}
}

// lvLines models a real captured excerpt: a plain INFO, a WARNING with a
// two-line traceback, then an ERROR. Headers carry the timestamped Odoo
// prefix; the traceback lines have NO timestamp but DO carry an inherited
// WARNING level (color inheritance during capture) — exactly the case that
// used to split each frame into its own block. Blocks (by timestamp header):
// [INFO], [WARNING + 2 traceback lines], [ERROR].
func lvLines() []config.ReportLine {
	return []config.ReportLine{
		{Level: "INFO", Text: "2026-07-13 22:19:29,410 17 INFO habitta_prod odoo: loading registry"},
		{Level: "WARNING", Text: "2026-07-13 22:19:32,852 17 WARNING habitta_prod py.warnings: deprecated field cache"},
		{Level: "WARNING", Text: `  File "fields.py", line 830`},
		{Level: "WARNING", Text: "    warnings.warn()"},
		{Level: "ERROR", Text: "2026-07-13 22:19:33,000 17 ERROR habitta_prod odoo: boom traceback"},
	}
}

func TestFilterLogLinesEmpty(t *testing.T) {
	lines := lvLines()
	got := filterLogLines(lines, "", "")
	if len(got) != len(lines) {
		t.Fatalf("empty filter should be identity: got %d, want %d", len(got), len(lines))
	}
}

func TestFilterLogLinesTextKeepsWholeBlock(t *testing.T) {
	// "cache" appears only in the WARNING header, but the whole 3-line block
	// (header + traceback) must survive — the traceback stays attached.
	got := filterLogLines(lvLines(), "cache", "")
	if len(got) != 3 {
		t.Fatalf("text match should keep the whole 3-line block, got %d: %+v", len(got), got)
	}
	if got[0].Level != "WARNING" || got[2].Text != "    warnings.warn()" {
		t.Fatalf("block not kept intact: %+v", got)
	}
}

func TestFilterLogLinesTextMatchesDeepFrame(t *testing.T) {
	// A query that only matches a deep traceback frame still keeps the block,
	// including its WARNING header.
	got := filterLogLines(lvLines(), "fields.py", "")
	if len(got) != 3 || got[0].Level != "WARNING" {
		t.Fatalf("deep-frame match should keep the whole block with its header: %+v", got)
	}
}

func TestFilterLogLinesLevelThresholdKeepsTraceback(t *testing.T) {
	got := filterLogLines(lvLines(), "", "WARNING")
	// WARNING+ keeps the WARNING block (3 lines incl. its traceback) and the
	// ERROR block (1 line); the traceback continuation lines travel with the
	// WARNING header instead of being dropped.
	if len(got) != 4 {
		t.Fatalf("WARNING+ should keep 4 lines (3-line WARNING block + ERROR), got %d: %+v", len(got), got)
	}
	var sawContinuation bool
	for _, l := range got {
		if strings.Contains(l.Text, "warnings.warn") {
			sawContinuation = true
		}
	}
	if !sawContinuation {
		t.Fatal("traceback continuation must survive under WARNING+ (attached to its header)")
	}
}

func TestFilterLogLinesLeadingHeaderlessBlockOnlyOnAll(t *testing.T) {
	lines := []config.ReportLine{
		{Level: "", Text: "headerless intro"},
		{Level: "INFO", Text: "2026-07-13 22:19:29,410 17 INFO db odoo: started"},
	}
	if all := filterLogLines(lines, "", ""); len(all) != 2 {
		t.Fatalf("all should keep both, got %d", len(all))
	}
	got := filterLogLines(lines, "", "INFO")
	if len(got) != 1 || !strings.Contains(got[0].Text, "started") {
		t.Fatalf("threshold should drop the leading headerless block: %+v", got)
	}
}

func TestFilterLogLinesComposedAnd(t *testing.T) {
	// text "cache" + WARNING+ → the whole WARNING block (header + traceback).
	got := filterLogLines(lvLines(), "cache", "WARNING")
	if len(got) != 3 || got[0].Level != "WARNING" {
		t.Fatalf("composed AND filter should keep the whole WARNING block: %+v", got)
	}
}

func TestCycleLevelForwardWrap(t *testing.T) {
	want := []string{"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL", ""}
	cur := ""
	for i, w := range want {
		cur = cycleLevel(cur, false)
		if cur != w {
			t.Fatalf("forward step %d: got %q, want %q", i, cur, w)
		}
	}
}

func TestCycleLevelBackwardWrap(t *testing.T) {
	// From "all" backwards should land on the most severe first.
	want := []string{"CRITICAL", "ERROR", "WARNING", "INFO", "DEBUG", ""}
	cur := ""
	for i, w := range want {
		cur = cycleLevel(cur, true)
		if cur != w {
			t.Fatalf("backward step %d: got %q, want %q", i, cur, w)
		}
	}
}

func TestFilterRuns(t *testing.T) {
	metas := []config.CmdLogMeta{
		{Cmd: "update sale --level debug"},
		{Cmd: "install account"},
		{Cmd: "UPDATE stock"},
	}
	got := filterRuns(metas, "update")
	if len(got) != 2 {
		t.Fatalf("case-insensitive 'update' should match 2, got %d", len(got))
	}
	if len(filterRuns(metas, "")) != 3 {
		t.Fatal("empty query should be identity")
	}
	if len(filterRuns(metas, "zzz")) != 0 {
		t.Fatal("no-match query should be empty")
	}
}

func TestRunStatusLabel(t *testing.T) {
	cases := map[int]string{
		exitOK:        "ok",
		exitCancelled: "cancel",
		exitError:     "err",
		exitUsage:     "err",
	}
	for exit, want := range cases {
		if got := runStatusLabel(exit); got != want {
			t.Fatalf("runStatusLabel(%d) = %q, want %q", exit, got, want)
		}
	}
}

func TestLogviewTimeLabel(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	sameDay := time.Date(2026, 7, 8, 9, 30, 15, 0, time.UTC)
	older := time.Date(2026, 6, 1, 18, 5, 0, 0, time.UTC)

	if got := logviewTimeLabel(sameDay, now); got != "09:30:15" {
		t.Fatalf("same-day label = %q, want 09:30:15", got)
	}
	if got := logviewTimeLabel(older, now); got != "Jun 01 18:05" {
		t.Fatalf("older label = %q, want 'Jun 01 18:05'", got)
	}
}

func TestParseLogviewArgs(t *testing.T) {
	list, last, clear, force, remote, jsonOut, from, unknown := parseLogviewArgs([]string{"--clear", "--force"})
	if !clear || !force || list || last || remote || jsonOut || from != "" || unknown != "" {
		t.Fatalf("parse --clear --force wrong: %v %v %v %v %v %v %q %q", list, last, clear, force, remote, jsonOut, from, unknown)
	}
	if _, _, _, _, _, _, _, u := parseLogviewArgs([]string{"--bogus"}); u != "--bogus" {
		t.Fatalf("unknown flag not surfaced: %q", u)
	}
	// --from consumes its value token; --remote is a bare switch.
	if _, _, _, _, _, _, f, u := parseLogviewArgs([]string{"--from", "prod"}); f != "prod" || u != "" {
		t.Fatalf("--from prod → from=%q unknown=%q", f, u)
	}
	if _, _, _, _, _, _, f, _ := parseLogviewArgs([]string{"--from=staging"}); f != "staging" {
		t.Fatalf("--from=staging → from=%q", f)
	}
	if _, _, _, _, r, _, _, _ := parseLogviewArgs([]string{"--remote"}); !r {
		t.Fatalf("--remote not parsed")
	}
	if _, _, _, _, _, j, _, _ := parseLogviewArgs([]string{"--json"}); !j {
		t.Fatalf("--json not parsed")
	}
}

func TestVisualRows(t *testing.T) {
	if got := visualRows("short", 0); got != 1 {
		t.Fatalf("unknown width should be 1 row, got %d", got)
	}
	if got := visualRows("abcdef", 80); got != 1 {
		t.Fatalf("fits in width = 1 row, got %d", got)
	}
	// 100 visible cols at width 40 → ceil(100/40) = 3 rows.
	if got := visualRows(strings.Repeat("x", 100), 40); got != 3 {
		t.Fatalf("wrap count = %d, want 3", got)
	}
	// ANSI escapes don't count toward width.
	styled := "\x1b[38;2;1;2;3m" + strings.Repeat("y", 20) + "\x1b[0m"
	if got := visualRows(styled, 40); got != 1 {
		t.Fatalf("ANSI-styled 20-col line = %d rows, want 1", got)
	}
}

func TestBodyWindow(t *testing.T) {
	rows := []int{1, 2, 3, 1, 1} // visual heights
	// budget 4 from offset 0: 1+2 fit (3), +3 overflows → 2 lines.
	if got := bodyWindow(rows, 0, 4); got != 2 {
		t.Fatalf("bodyWindow(0,4) = %d, want 2", got)
	}
	// budget smaller than the top line still shows it (never zero).
	if got := bodyWindow([]int{5, 1}, 0, 3); got != 1 {
		t.Fatalf("oversized top line should still show 1, got %d", got)
	}
	// from an inner offset, fills forward.
	if got := bodyWindow(rows, 3, 2); got != 2 {
		t.Fatalf("bodyWindow(3,2) = %d, want 2", got)
	}
}

func TestMaxTopOffset(t *testing.T) {
	rows := []int{1, 1, 1, 1, 1}
	// budget 2 → last two lines fit → top offset 3.
	if got := maxTopOffset(rows, 2); got != 3 {
		t.Fatalf("maxTopOffset budget 2 = %d, want 3", got)
	}
	// budget ≥ total → offset 0 (everything fits).
	if got := maxTopOffset(rows, 10); got != 0 {
		t.Fatalf("maxTopOffset budget 10 = %d, want 0", got)
	}
	// a final line taller than the budget is still reachable (offset = last).
	if got := maxTopOffset([]int{1, 1, 5}, 2); got != 2 {
		t.Fatalf("oversized last line offset = %d, want 2", got)
	}
}

func TestPageBack(t *testing.T) {
	rows := []int{1, 1, 1, 1, 1}
	if got := pageBack(rows, 4, 2); got != 2 {
		t.Fatalf("pageBack(4,2) = %d, want 2", got)
	}
	if got := pageBack(rows, 0, 3); got != 0 {
		t.Fatalf("pageBack at top = %d, want 0", got)
	}
}

func blockLines() []config.ReportLine {
	// An INFO entry with no continuation, an ERROR entry with a 2-line
	// traceback, then a WARNING entry. Headers are timestamped; the traceback
	// lines carry an inherited ERROR level but no timestamp (the real capture
	// shape) — blocks group by the timestamp, not the level.
	return []config.ReportLine{
		{Level: "INFO", Text: "2026-07-13 10:00:00,000 1 INFO db odoo: starting"},         // 0 block 0
		{Level: "ERROR", Text: "2026-07-13 10:00:01,000 1 ERROR db odoo: boom"},           // 1 block 1
		{Level: "ERROR", Text: "  File x.py line 3"},                                      // 2 block 1 (continuation)
		{Level: "ERROR", Text: "  ValueError: nope"},                                      // 3 block 1 (continuation)
		{Level: "WARNING", Text: "2026-07-13 10:00:02,000 1 WARNING db odoo: slow query"}, // 4 block 4
	}
}

func TestBlockStartEnd(t *testing.T) {
	l := blockLines()
	for i, want := range []int{0, 1, 1, 1, 4} {
		if got := blockStartOf(l, i); got != want {
			t.Fatalf("blockStartOf(%d) = %d, want %d", i, got, want)
		}
	}
	if got := blockEndOf(l, 1); got != 4 {
		t.Fatalf("blockEndOf(1) = %d, want 4 (ERROR + 2 traceback lines)", got)
	}
	if got := blockEndOf(l, 4); got != 5 {
		t.Fatalf("blockEndOf(4) = %d, want 5", got)
	}
}

func TestBlockLeadingUnleveled(t *testing.T) {
	l := []config.ReportLine{
		{Text: "preamble a"}, {Text: "preamble b"},
		{Level: "INFO", Text: "2026-07-13 10:00:00,000 1 INFO db odoo: go"},
	}
	// Leading unleveled lines anchor to block 0.
	if blockStartOf(l, 0) != 0 || blockStartOf(l, 1) != 0 {
		t.Fatal("leading unleveled lines should anchor to block 0")
	}
	if blockStartOf(l, 2) != 2 {
		t.Fatal("the INFO line starts its own block")
	}
	if got := blockEndOf(l, 0); got != 2 {
		t.Fatalf("leading block end = %d, want 2", got)
	}
}

func TestSelectedLines(t *testing.T) {
	l := blockLines()
	// Select the ERROR block (start index 1) → its 3 lines come back in order.
	got := selectedLines(l, map[int]bool{1: true})
	if len(got) != 3 || !strings.Contains(got[0].Text, "boom") || got[2].Text != "  ValueError: nope" {
		t.Fatalf("selected ERROR block wrong: %#v", got)
	}
	// No selection → nil (caller falls back to copying everything).
	if selectedLines(l, nil) != nil {
		t.Fatal("empty selection should return nil")
	}
}

func TestDetailSpaceTogglesBlock(t *testing.T) {
	m := logviewModel{
		mode:     logviewDetail,
		record:   config.CmdLogRecord{Lines: blockLines()},
		selected: map[int]bool{},
		height:   40,
		width:    100,
	}
	// Cursor on the traceback line (index 2) — its block is the ERROR at 1.
	m.detailCursor = 2
	updated, _ := m.updateDetail(tea.KeyMsg{Type: tea.KeySpace})
	m2 := updated.(logviewModel)
	if !m2.selected[1] {
		t.Fatalf("space should select the cursor's block (start 1); selected=%v", m2.selected)
	}
	// Space again clears it.
	updated, _ = m2.updateDetail(tea.KeyMsg{Type: tea.KeySpace})
	if updated.(logviewModel).selected[1] {
		t.Fatal("second space should deselect the block")
	}
}

func TestDetailFilterResetsSelection(t *testing.T) {
	m := logviewModel{
		mode:     logviewDetail,
		record:   config.CmdLogRecord{Lines: blockLines()},
		selected: map[int]bool{1: true},
		height:   40, width: 100,
		detailCursor: 3,
	}
	// Typing into the filter changes the line set → selection + cursor reset.
	updated, _ := m.updateDetail(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m2 := updated.(logviewModel)
	if len(m2.selected) != 0 || m2.detailCursor != 0 || m2.textFilter != "x" {
		t.Fatalf("filter edit should reset selection/cursor: sel=%v cur=%d f=%q",
			m2.selected, m2.detailCursor, m2.textFilter)
	}
}

// TestDetailViewFitsHeight is the regression guard for the overflow bug: the
// detail view, rendered at a known width/height with long (wrapping) lines,
// must never produce more visual rows than the terminal height — otherwise the
// terminal scrolls and the header (with the filter) leaves the viewport.
func TestDetailViewFitsHeight(t *testing.T) {
	lines := make([]config.ReportLine, 40)
	for i := range lines {
		lines[i] = config.ReportLine{
			Level: "INFO",
			Text:  "2026-07-08 10:00:00 7 INFO db odoo.modules.loading: " + strings.Repeat("word ", 25),
		}
	}
	// Realistic terminal sizes: the body area holds at least one (wrapping)
	// log line. A degenerate case where a single line is taller than the whole
	// body area can't fit by definition and isn't the reported bug.
	p := theme.PaletteByName("")
	for _, dim := range []struct{ w, h int }{{80, 24}, {100, 30}, {120, 40}, {70, 20}} {
		m := logviewModel{
			mode:    logviewDetail,
			record:  config.CmdLogRecord{Cmd: "update", DB: "db", Lines: lines},
			palette: p,
			accent:  p.PromptColor(theme.StageDev),
			styles:  theme.New(p, theme.StageDev),
			width:   dim.w,
			height:  dim.h,
		}
		out := m.viewDetail()
		visual := 0
		for _, ln := range strings.Split(out, "\n") {
			visual += visualRows(ln, dim.w)
		}
		if visual > dim.h {
			t.Fatalf("detail view at %dx%d rendered %d visual rows > height %d",
				dim.w, dim.h, visual, dim.h)
		}
	}
}
