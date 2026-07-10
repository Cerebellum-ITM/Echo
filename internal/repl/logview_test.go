package repl

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

func lvLines() []config.ReportLine {
	return []config.ReportLine{
		{Level: "DEBUG", Text: "debug detail cache miss"},
		{Level: "INFO", Text: "loading registry"},
		{Level: "WARNING", Text: "deprecated field cache"},
		{Level: "ERROR", Text: "boom traceback"},
		{Level: "", Text: "unleveled tail line"},
	}
}

func TestFilterLogLinesEmpty(t *testing.T) {
	lines := lvLines()
	got := filterLogLines(lines, "", "")
	if len(got) != len(lines) {
		t.Fatalf("empty filter should be identity: got %d, want %d", len(got), len(lines))
	}
}

func TestFilterLogLinesTextOnly(t *testing.T) {
	got := filterLogLines(lvLines(), "cache", "")
	// "cache" appears in the DEBUG and WARNING lines.
	if len(got) != 2 {
		t.Fatalf("expected 2 matches for 'cache', got %d: %+v", len(got), got)
	}
}

func TestFilterLogLinesLevelThreshold(t *testing.T) {
	got := filterLogLines(lvLines(), "", "WARNING")
	// WARNING+ keeps WARNING and ERROR; hides DEBUG/INFO and the unleveled.
	if len(got) != 2 {
		t.Fatalf("WARNING+ should keep 2 lines, got %d: %+v", len(got), got)
	}
	for _, l := range got {
		if l.Level != "WARNING" && l.Level != "ERROR" {
			t.Fatalf("unexpected level survived WARNING+: %q", l.Level)
		}
	}
}

func TestFilterLogLinesUnleveledOnlyOnAll(t *testing.T) {
	// Unleveled line survives on "all"...
	all := filterLogLines(lvLines(), "unleveled", "")
	if len(all) != 1 {
		t.Fatalf("unleveled line should show on all, got %d", len(all))
	}
	// ...but never under any threshold.
	for _, lv := range []string{"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"} {
		if got := filterLogLines(lvLines(), "unleveled", lv); len(got) != 0 {
			t.Fatalf("unleveled line must be hidden under %s, got %d", lv, len(got))
		}
	}
}

func TestFilterLogLinesComposedAnd(t *testing.T) {
	// text "cache" + WARNING+ → only the WARNING "deprecated field cache".
	got := filterLogLines(lvLines(), "cache", "WARNING")
	if len(got) != 1 || got[0].Level != "WARNING" {
		t.Fatalf("composed AND filter wrong: %+v", got)
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
	list, last, clear, force, remote, from, unknown := parseLogviewArgs([]string{"--clear", "--force"})
	if !clear || !force || list || last || remote || from != "" || unknown != "" {
		t.Fatalf("parse --clear --force wrong: %v %v %v %v %v %q %q", list, last, clear, force, remote, from, unknown)
	}
	if _, _, _, _, _, _, u := parseLogviewArgs([]string{"--bogus"}); u != "--bogus" {
		t.Fatalf("unknown flag not surfaced: %q", u)
	}
	// --from consumes its value token; --remote is a bare switch.
	if _, _, _, _, _, f, u := parseLogviewArgs([]string{"--from", "prod"}); f != "prod" || u != "" {
		t.Fatalf("--from prod → from=%q unknown=%q", f, u)
	}
	if _, _, _, _, _, f, _ := parseLogviewArgs([]string{"--from=staging"}); f != "staging" {
		t.Fatalf("--from=staging → from=%q", f)
	}
	if _, _, _, _, r, _, _ := parseLogviewArgs([]string{"--remote"}); !r {
		t.Fatalf("--remote not parsed")
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
	// traceback (unleveled), then a WARNING entry.
	return []config.ReportLine{
		{Level: "INFO", Text: "starting"},      // 0 block 0
		{Level: "ERROR", Text: "boom"},         // 1 block 1
		{Text: "  File x.py line 3"},           // 2 block 1 (continuation)
		{Text: "  ValueError: nope"},           // 3 block 1 (continuation)
		{Level: "WARNING", Text: "slow query"}, // 4 block 4
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
		{Text: "preamble a"}, {Text: "preamble b"}, {Level: "INFO", Text: "go"},
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
	if len(got) != 3 || got[0].Text != "boom" || got[2].Text != "  ValueError: nope" {
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
