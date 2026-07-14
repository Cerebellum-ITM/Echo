package repl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// logviewLevels is the level-filter cycle in the log view: "" is "all"
// (no threshold), then a min-level threshold escalating by severity. `tab`
// steps forward, `shift+tab` back, both wrapping.
var logviewLevels = []string{"", "DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"}

// logview view modes.
const (
	logviewList   = 0 // the run list
	logviewDetail = 1 // one run's log lines
)

// --- pure helpers (unit-testable without a TTY) ------------------------

// filterRuns keeps the records whose full command line contains q
// (case-insensitive substring, the fuzzy picker's matching).
func filterRuns(metas []config.CmdLogMeta, q string) []config.CmdLogMeta {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return metas
	}
	out := make([]config.CmdLogMeta, 0, len(metas))
	for _, m := range metas {
		if strings.Contains(strings.ToLower(m.Cmd), q) {
			out = append(out, m)
		}
	}
	return out
}

// filterLogLines keeps whole log blocks matching both a case-insensitive
// text substring and a min-level threshold. A block is a leveled entry plus
// its unleveled continuation lines (a traceback), so the filter is
// block-aware: it never splits an entry from its traceback. A block passes
// the level gate when its header's level meets the threshold (minLevel == ""
// disables it), and the text gate when ANY line in the block matches — so a
// traceback stays attached to its warning/error even when only the header
// (or only a deep frame) contains the query. A leading unleveled block (no
// header) is kept only on "all", matching the old per-line rule for
// headerless output.
func filterLogLines(lines []config.ReportLine, q, minLevel string) []config.ReportLine {
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]config.ReportLine, 0, len(lines))
	for i := 0; i < len(lines); {
		// Block = lines[i] (a header, or a leading continuation) plus every
		// following continuation line up to the next header.
		end := i + 1
		for end < len(lines) && entryHeaderLevel(lines[end].Text) == "" {
			end++
		}
		block := lines[i:end]
		header := entryHeaderLevel(block[0].Text)

		levelOK := minLevel == "" ||
			(header != "" && levelRank[header] >= levelRank[minLevel])
		textOK := q == ""
		for _, l := range block {
			if textOK {
				break
			}
			if strings.Contains(strings.ToLower(l.Text), q) {
				textOK = true
			}
		}
		if levelOK && textOK {
			out = append(out, block...)
		}
		i = end
	}
	return out
}

// cycleLevel steps the level filter forward (back == false) or backward
// through logviewLevels, wrapping at both ends.
func cycleLevel(cur string, back bool) string {
	idx := 0
	for i, lv := range logviewLevels {
		if lv == cur {
			idx = i
			break
		}
	}
	n := len(logviewLevels)
	if back {
		idx = (idx - 1 + n) % n
	} else {
		idx = (idx + 1) % n
	}
	return logviewLevels[idx]
}

// entryHeaderLevel returns the log level when text starts a NEW log record —
// i.e. it carries a timestamped Odoo or loguru prefix — and "" for a
// continuation line (a traceback frame, a bare message tail). This is the
// block-boundary signal: it is deliberately NOT the stored ReportLine.Level,
// because a traceback line inherits its entry's color (Kind warn/err) and so
// gets tagged with a level during capture — grouping on that level would
// split every traceback frame into its own block. A timestamp only ever
// begins a real record, so it groups an entry with its whole traceback.
func entryHeaderLevel(text string) string {
	if m := odooLogPrefix.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	if m := loguruLogPrefix.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// blockStartOf returns the index of the block that filtered line i belongs to.
// A block is a log entry (a timestamped header) plus every continuation line
// that follows it (a traceback) up to the next header. Continuation lines with
// no header before them form a leading block anchored at index 0.
func blockStartOf(lines []config.ReportLine, i int) int {
	for j := i; j >= 0; j-- {
		if entryHeaderLevel(lines[j].Text) != "" {
			return j
		}
	}
	return 0
}

// blockEndOf returns the exclusive end index of the block anchored at start:
// the next header line after it, or the end of the slice.
func blockEndOf(lines []config.ReportLine, start int) int {
	for j := start + 1; j < len(lines); j++ {
		if entryHeaderLevel(lines[j].Text) != "" {
			return j
		}
	}
	return len(lines)
}

// selectedLines returns, in order, the filtered lines whose block is selected
// — the payload ctrl+o copies when a selection exists.
func selectedLines(lines []config.ReportLine, selected map[int]bool) []config.ReportLine {
	if len(selected) == 0 {
		return nil
	}
	out := make([]config.ReportLine, 0, len(lines))
	for i, l := range lines {
		if selected[blockStartOf(lines, i)] {
			out = append(out, l)
		}
	}
	return out
}

// runStatusLabel maps an exit code to the list's short status token.
func runStatusLabel(exit int) string {
	switch exit {
	case exitOK:
		return "ok"
	case exitCancelled:
		return "cancel"
	default:
		return "err"
	}
}

// logviewTimeLabel formats a record's start time: clock-only when it
// happened on the same calendar day as now, else month-day + clock.
func logviewTimeLabel(t, now time.Time) string {
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04:05")
	}
	return t.Format("Jan 02 15:04")
}

// --- bubbletea model ---------------------------------------------------

type logviewModel struct {
	mode    int
	metas   []config.CmdLogMeta // all runs, newest first
	now     time.Time
	retDays int

	// load opens a run's full record from its meta — config.LoadCmdLog for a
	// local browse, or a lookup into the SSH-preloaded map for a remote one.
	load func(config.CmdLogMeta) (config.CmdLogRecord, bool)
	// fromLabel names the remote target when browsing remotely ("" = local).
	fromLabel string

	// list state
	listFilter string
	cursor     int // index into the filtered run list
	listOffset int

	// detail state
	record       config.CmdLogRecord
	recordMeta   config.CmdLogMeta
	textFilter   string
	minLevel     string
	lineOffset   int
	detailCursor int          // cursor line index into the filtered lines
	selected     map[int]bool // selected block starts (indices into filtered lines)
	opened       bool         // a run was opened at least once (drives the close frame)

	height  int
	width   int
	palette theme.Palette
	accent  lipgloss.Color
	styles  theme.Styles

	quit   bool // ctrl+x: close Echo entirely
	copied int  // lines copied via ctrl+o
}

func (m logviewModel) Init() tea.Cmd { return nil }

// bar renders the stage-tinted "│ " prefix every logview line carries.
func (m logviewModel) bar() string {
	return lipgloss.NewStyle().Foreground(m.accent).Render("│ ")
}

// bodyBudget turns a measured chrome height into the scroll window's row
// budget: the terminal height minus the chrome minus two rows held back for
// the `↑/↓ N more` indicators. Never below 1, so at least one line always
// shows. Both the list and detail views measure their own chrome — including
// footers/headers that themselves soft-wrap at narrow widths, which a
// fixed chrome count would ignore and which caused the header-off-screen
// overflow.
func (m logviewModel) bodyBudget(chromeRows int) int {
	if m.height <= 0 {
		return defaultHelpRows
	}
	r := m.height - chromeRows - 2
	if r < 1 {
		return 1
	}
	return r
}

// detailBudget is the detail view's visual-row body budget, computed from the
// live head/filter/footer heights (each may wrap).
func (m logviewModel) detailBudget() int {
	chrome := visualRows(m.detailHead(), m.width) +
		visualRows(m.detailFilterLine(), m.width) +
		2 + // the two blank bar lines
		visualRows(m.detailFooterLine(), m.width)
	return m.bodyBudget(chrome)
}

// listBudget is the run-list body budget. List rows are single-line
// (truncated to cmdBudget), so this doubles as a line count.
func (m logviewModel) listBudget(rowCount int) int {
	chrome := visualRows(m.listTitleLine(rowCount), m.width) +
		visualRows(m.listFilterLine(), m.width) +
		2 + // the two blank bar lines
		visualRows(m.listFooterLine(), m.width)
	return m.bodyBudget(chrome)
}

// visualRows reports how many terminal rows a rendered line occupies once the
// terminal soft-wraps it to the viewport width. lipgloss.Width ignores ANSI
// escapes, so the wrap count is measured on visible columns. A zero/unknown
// width (before the first WindowSizeMsg) counts every line as a single row.
func visualRows(rendered string, width int) int {
	if width <= 0 {
		return 1
	}
	w := lipgloss.Width(rendered)
	if w <= width {
		return 1
	}
	return (w + width - 1) / width
}

// bodyWindow returns how many consecutive lines, starting at offset, fit
// within a budget of visual rows given each line's wrapped height (rowsOf).
// At least the top line is always shown, even if it alone overflows, so the
// cursor never lands on an invisible line.
func bodyWindow(rowsOf []int, offset, budget int) int {
	used, count := 0, 0
	for i := offset; i < len(rowsOf); i++ {
		if used+rowsOf[i] > budget && count > 0 {
			break
		}
		used += rowsOf[i]
		count++
	}
	return count
}

// maxTopOffset is the largest start offset from which every remaining line
// still fits in budget visual rows — i.e. how far down the window can scroll
// while keeping the last line reachable.
func maxTopOffset(rowsOf []int, budget int) int {
	used, off := 0, len(rowsOf)
	for i := len(rowsOf) - 1; i >= 0; i-- {
		if used+rowsOf[i] > budget && off != len(rowsOf) {
			break
		}
		used += rowsOf[i]
		off = i
	}
	return off
}

func (m logviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = ws.Height
		m.width = ws.Width
		return m, nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.mode == logviewList {
		return m.updateList(k)
	}
	return m.updateDetail(k)
}

func (m logviewModel) updateList(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := filterRuns(m.metas, m.listFilter)
	switch k.String() {
	case "ctrl+x":
		m.quit = true
		return m, tea.Quit
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.listFilter != "" {
			m.listFilter = ""
			m.cursor, m.listOffset = 0, 0
			return m, nil
		}
		return m, tea.Quit
	case "up", "ctrl+p":
		m.cursor--
	case "down", "ctrl+n":
		m.cursor++
	case "pgup":
		m.cursor -= m.listBudget(len(rows))
	case "pgdown":
		m.cursor += m.listBudget(len(rows))
	case "enter":
		if len(rows) == 0 {
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(rows) {
			sel := rows[m.cursor]
			if rec, ok := m.load(sel); ok {
				m.record = rec
				m.recordMeta = sel
				m.mode = logviewDetail
				m.textFilter, m.minLevel, m.lineOffset = "", "", 0
				m.detailCursor = 0
				m.selected = map[int]bool{}
				m.opened = true
			}
		}
		return m, nil
	case "ctrl+u":
		m.listFilter = ""
		m.cursor, m.listOffset = 0, 0
		return m, nil
	case "backspace":
		if m.listFilter != "" {
			m.listFilter = trimLastRune(m.listFilter)
			m.cursor, m.listOffset = 0, 0
		}
		return m, nil
	default:
		if s := typedText(k); s != "" {
			m.listFilter += s
			m.cursor, m.listOffset = 0, 0
			return m, nil
		}
		return m, nil
	}
	m.clampListCursor(len(filterRuns(m.metas, m.listFilter)))
	return m, nil
}

func (m logviewModel) updateDetail(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+x":
		m.quit = true
		return m, tea.Quit
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		// esc peels one layer at a time: the text filter, then the block
		// selection, then back to the run list.
		if m.textFilter != "" {
			m.resetDetailView()
			return m, nil
		}
		if len(m.selected) > 0 {
			m.selected = map[int]bool{}
			return m, nil
		}
		m.mode = logviewList
		m.minLevel = ""
		return m, nil
	case "tab":
		m.minLevel = cycleLevel(m.minLevel, false)
		m.resetDetailAnchor()
		return m, nil
	case "shift+tab":
		m.minLevel = cycleLevel(m.minLevel, true)
		m.resetDetailAnchor()
		return m, nil
	case " ", "space":
		// Toggle selection of the block under the cursor. (Space arrives as
		// either " " or "space" depending on the terminal/key path.)
		lines := filterLogLines(m.record.Lines, m.textFilter, m.minLevel)
		if len(lines) > 0 {
			start := blockStartOf(lines, clampInt(m.detailCursor, 0, len(lines)-1))
			if m.selected == nil {
				m.selected = map[int]bool{}
			}
			if m.selected[start] {
				delete(m.selected, start)
			} else {
				m.selected[start] = true
			}
		}
		return m, nil
	case "ctrl+o":
		lines := filterLogLines(m.record.Lines, m.textFilter, m.minLevel)
		payload := selectedLines(lines, m.selected)
		if payload == nil {
			payload = lines // nothing selected → copy everything visible
		}
		if len(payload) > 0 {
			var b strings.Builder
			for _, l := range payload {
				b.WriteString(l.Text)
				b.WriteByte('\n')
			}
			if clipboard.WriteAll(b.String()) == nil {
				m.copied = len(payload)
			}
		}
		return m, tea.Quit
	case "up", "ctrl+p":
		m.detailCursor--
	case "down", "ctrl+n":
		m.detailCursor++
	case "pgup":
		rowsOf := m.detailRowsOf()
		m.detailCursor -= pageBack(rowsOf, m.detailCursor, m.detailBudget())
	case "pgdown":
		rowsOf := m.detailRowsOf()
		m.detailCursor += bodyWindow(rowsOf, clampInt(m.detailCursor, 0, len(rowsOf)), m.detailBudget())
	case "ctrl+u":
		if m.textFilter != "" {
			m.resetDetailView()
		}
		return m, nil
	case "backspace":
		if m.textFilter != "" {
			m.textFilter = trimLastRune(m.textFilter)
			m.resetDetailAnchor()
		}
		return m, nil
	default:
		if s := typedText(k); s != "" {
			m.textFilter += s
			m.resetDetailAnchor()
			return m, nil
		}
		return m, nil
	}
	m.clampDetailCursor()
	return m, nil
}

// resetDetailAnchor re-anchors the detail view — cursor, scroll, and block
// selection all reset — without touching the text filter. Called on any change
// to the filtered line set (filter edit, level cycle), since block selection
// is keyed by line position and would otherwise point at the wrong blocks.
func (m *logviewModel) resetDetailAnchor() {
	m.detailCursor, m.lineOffset = 0, 0
	m.selected = map[int]bool{}
}

// resetDetailView clears the text filter as well, then re-anchors — used when
// esc/ctrl+u drop the whole filter.
func (m *logviewModel) resetDetailView() {
	m.textFilter = ""
	m.resetDetailAnchor()
}

// clampDetailCursor keeps the cursor inside the filtered lines and scrolls the
// window (wrap-aware) so the cursor line stays visible.
func (m *logviewModel) clampDetailCursor() {
	rowsOf := m.detailRowsOf()
	n := len(rowsOf)
	if n == 0 {
		m.detailCursor, m.lineOffset = 0, 0
		return
	}
	m.detailCursor = clampInt(m.detailCursor, 0, n-1)
	if m.detailCursor < m.lineOffset {
		m.lineOffset = m.detailCursor
	}
	// Scroll down until the cursor fits in the window from the current offset.
	for m.detailCursor >= m.lineOffset+bodyWindow(rowsOf, m.lineOffset, m.detailBudget()) {
		m.lineOffset++
	}
	if m.lineOffset < 0 {
		m.lineOffset = 0
	}
}

// clampInt confines v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampListCursor keeps the cursor and scroll offset inside the filtered
// list bounds, scrolling the window to keep the cursor visible.
func (m *logviewModel) clampListCursor(n int) {
	if n == 0 {
		m.cursor, m.listOffset = 0, 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	rows := m.listBudget(n)
	if m.cursor < m.listOffset {
		m.listOffset = m.cursor
	}
	if m.cursor >= m.listOffset+rows {
		m.listOffset = m.cursor - rows + 1
	}
	if m.listOffset < 0 {
		m.listOffset = 0
	}
}

// detailRenderedRows renders every filtered detail line with its stage-tinted
// bar and a 3-column gutter (a `❯` cursor mark and a `✓` selection mark, both
// blank when off), returning the rendered strings alongside each one's wrapped
// visual height at the current width. The gutter is part of the measured line
// so the wrap math accounts for it.
func (m logviewModel) detailRenderedRows() (rendered []string, rowsOf []int) {
	bar := lipgloss.NewStyle().Foreground(m.accent).Render("│ ")
	cursorStyle := lipgloss.NewStyle().Foreground(m.accent).Bold(true)
	selStyle := lipgloss.NewStyle().Foreground(m.palette.Success)
	lines := filterLogLines(m.record.Lines, m.textFilter, m.minLevel)
	rendered = make([]string, len(lines))
	rowsOf = make([]int, len(lines))
	for i, l := range lines {
		cur := " "
		if i == m.detailCursor {
			cur = cursorStyle.Render("❯")
		}
		sel := " "
		if m.selected[blockStartOf(lines, i)] {
			sel = selStyle.Render("✓")
		}
		full := bar + cur + sel + " " + m.renderLogLine(l)
		rendered[i] = full
		rowsOf[i] = visualRows(full, m.width)
	}
	return rendered, rowsOf
}

// detailRowsOf is detailRenderedRows without the rendered strings — the
// per-line wrapped heights the scroll math needs in Update.
func (m logviewModel) detailRowsOf() []int {
	_, rowsOf := m.detailRenderedRows()
	return rowsOf
}

// pageBack counts how many lines a PageUp from offset should step back — the
// lines that fill one budget of visual rows ending just above offset.
func pageBack(rowsOf []int, offset, budget int) int {
	used, count := 0, 0
	for i := offset - 1; i >= 0; i-- {
		if used+rowsOf[i] > budget && count > 0 {
			break
		}
		used += rowsOf[i]
		count++
	}
	return count
}

func (m logviewModel) View() string {
	if m.mode == logviewList {
		return m.viewList()
	}
	return m.viewDetail()
}

// listTitleLine / listFilterLine / listFooterLine build the run list's chrome
// lines (with the bar) so both the renderer and the budget measure the same
// strings.
func (m logviewModel) listTitleLine(rowCount int) string {
	dim := lipgloss.NewStyle().Foreground(m.palette.Dim)
	faint := lipgloss.NewStyle().Foreground(m.palette.Faint)
	title := fmt.Sprintf("logview — %d run%s", rowCount, plural(rowCount))
	if m.fromLabel != "" {
		title += " · " + m.fromLabel
	}
	out := m.bar() + dim.Render(title)
	if m.retDays > 0 {
		out += faint.Render(fmt.Sprintf("  (%dd retention)", m.retDays))
	}
	return out
}

func (m logviewModel) listFilterLine() string {
	faint := lipgloss.NewStyle().Foreground(m.palette.Faint)
	return m.bar() + faint.Render("filter › ") + m.filterEcho(m.listFilter)
}

func (m logviewModel) listFooterLine() string {
	faint := lipgloss.NewStyle().Foreground(m.palette.Faint)
	return m.bar() + faint.Render("↑↓ move · enter open · type filter · esc close · ctrl+x quit")
}

func (m logviewModel) viewList() string {
	bar := m.bar()
	dim := lipgloss.NewStyle().Foreground(m.palette.Dim)
	faint := lipgloss.NewStyle().Foreground(m.palette.Faint)
	accent := lipgloss.NewStyle().Foreground(m.accent)

	rows := filterRuns(m.metas, m.listFilter)

	var b strings.Builder
	b.WriteString(m.listTitleLine(len(rows)) + "\n")
	b.WriteString(m.listFilterLine() + "\n")
	b.WriteString(bar + "\n")

	if len(rows) == 0 {
		b.WriteString(bar + faint.Render("no runs match") + "\n")
		b.WriteString(bar + "\n")
		b.WriteString(bar + faint.Render("type filter · esc close · ctrl+x quit"))
		return b.String()
	}

	maxR := m.listBudget(len(rows))
	start := m.listOffset
	end := start + maxR
	if end > len(rows) {
		end = len(rows)
	}
	if start > 0 {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↑ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		cursor := "  "
		if i == m.cursor {
			cursor = accent.Render("❯ ")
		}
		b.WriteString(bar + cursor + m.runRow(rows[i]) + "\n")
	}
	if end < len(rows) {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↓ %d more", len(rows)-end)) + "\n")
	}
	b.WriteString(bar + "\n")
	b.WriteString(m.listFooterLine())
	return b.String()
}

// runRow renders one list row: time · cmd · status · line count · db.
func (m logviewModel) runRow(meta config.CmdLogMeta) string {
	p := m.palette
	dim := lipgloss.NewStyle().Foreground(p.Dim)
	faint := lipgloss.NewStyle().Foreground(p.Faint)

	tm := dim.Render(logviewTimeLabel(meta.Started, m.now))
	cmdText := truncate(meta.Cmd, m.cmdBudget())
	status := m.statusStyled(meta.Exit)
	count := faint.Render(fmt.Sprintf("%d lines", meta.LineCount))
	db := dim.Render(meta.DB)

	return tm + "  " + m.styles.Out.Render(cmdText) + "  " + status + "  " + count + "  " + db
}

// cmdBudget is how many runes of the command line a row shows before
// truncation, scaled to the viewport width (fallback 40 before the first
// WindowSizeMsg). The time/status/count/db columns and their separators eat
// ~44 cols; the command gets the rest, clamped to a readable band.
func (m logviewModel) cmdBudget() int {
	if m.width <= 0 {
		return 40
	}
	b := m.width - 44
	if b < 20 {
		b = 20
	}
	if b > 80 {
		b = 80
	}
	return b
}

func (m logviewModel) statusStyled(exit int) string {
	label := runStatusLabel(exit)
	p := m.palette
	switch label {
	case "ok":
		return lipgloss.NewStyle().Foreground(p.Dim).Render(label)
	case "err":
		return lipgloss.NewStyle().Foreground(p.Error).Render(label)
	default: // cancel
		return lipgloss.NewStyle().Foreground(p.Faint).Render(label)
	}
}

// detailHead / detailFilterLine / detailFooterLine build the detail view's
// chrome lines (with the bar) so the renderer and the budget agree on their
// wrapped heights.
func (m logviewModel) detailHead() string {
	dim := lipgloss.NewStyle().Foreground(m.palette.Dim)
	head := fmt.Sprintf("%s — %s · %s · %s",
		m.record.Cmd,
		logviewTimeLabel(m.recordMeta.Started, m.now),
		runStatusLabel(m.record.Exit),
		m.record.DB)
	if m.fromLabel != "" {
		head += " · " + m.fromLabel
	}
	return m.bar() + dim.Render(head)
}

func (m logviewModel) detailFilterLine() string {
	dim := lipgloss.NewStyle().Foreground(m.palette.Dim)
	faint := lipgloss.NewStyle().Foreground(m.palette.Faint)
	lvlLabel, lvlStyle := "all", faint
	if m.minLevel != "" {
		lvlLabel, lvlStyle = m.minLevel+"+", dim
	}
	return m.bar() + faint.Render("filter › ") + m.filterEcho(m.textFilter) +
		faint.Render("   level › ") + lvlStyle.Render(lvlLabel)
}

func (m logviewModel) detailFooterLine() string {
	faint := lipgloss.NewStyle().Foreground(m.palette.Faint)
	return m.bar() + faint.Render(m.detailFooter())
}

func (m logviewModel) viewDetail() string {
	bar := m.bar()
	dim := lipgloss.NewStyle().Foreground(m.palette.Dim)
	faint := lipgloss.NewStyle().Foreground(m.palette.Faint)

	rendered, rowsOf := m.detailRenderedRows()
	total := len(rendered)

	var b strings.Builder
	b.WriteString(m.detailHead() + "\n")
	b.WriteString(m.detailFilterLine() + "\n")
	b.WriteString(bar + "\n")

	if total == 0 {
		b.WriteString(bar + faint.Render("no lines match") + "\n")
		b.WriteString(bar + "\n")
		b.WriteString(bar + faint.Render("tab level · type filter · esc back · ctrl+x quit"))
		return b.String()
	}

	start := clampInt(m.lineOffset, 0, total-1)
	end := start + bodyWindow(rowsOf, start, m.detailBudget())
	if start > 0 {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↑ %d more", start)) + "\n")
	}
	for _, r := range rendered[start:end] {
		b.WriteString(r + "\n")
	}
	if end < total {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↓ %d more", total-end)) + "\n")
	}
	b.WriteString(bar + "\n")
	b.WriteString(m.detailFooterLine())
	return b.String()
}

// detailFooter is the detail view's key hint. It reflects whether a block
// selection is active so ctrl+o's target ("selection" vs "all") is clear.
func (m logviewModel) detailFooter() string {
	if len(m.selected) > 0 {
		return "↑↓ move · space select · ctrl+o copy selection · esc clear · ctrl+x quit"
	}
	return "↑↓ move · space select · tab level · type filter · ctrl+o copy all · esc back · ctrl+x quit"
}

// renderLogLine colors a stored line the same way the live REPL did: the
// rich Odoo-log renderer when the text matches, else a level→style fallback.
func (m logviewModel) renderLogLine(l config.ReportLine) string {
	if rendered, ok := renderLogLine(l.Text, m.styles, m.palette); ok {
		return rendered
	}
	s := m.styles
	switch kindFromLevel(l.Level) {
	case "faint":
		return s.Faint.Render(l.Text)
	case "info":
		return s.Info.Render(l.Text)
	case "warn":
		return s.Warn.Render(l.Text)
	case "err":
		return s.Err.Render(l.Text)
	default:
		return s.Out.Render(l.Text)
	}
}

// filterEcho renders the current filter text with a Faint placeholder when
// empty.
func (m logviewModel) filterEcho(f string) string {
	if f == "" {
		return lipgloss.NewStyle().Foreground(m.palette.Faint).Render("type to filter…")
	}
	return m.styles.Out.Render(f)
}

// --- command entry point -----------------------------------------------

// runLogview implements `logview [--list|--last|--clear [--force]]`: an
// interactive browser over the per-project command-log history (Unit 81),
// plus headless escape hatches. It is a meta command — it never resets
// lastOutput, so `copy-last` still copies the previous command.
func (sess *session) runLogview(ctx context.Context, args []string) {
	list, last, clear, force, remote, jsonOut, from, unknown := parseLogviewArgs(args)
	if unknown != "" {
		sess.print(Line{Kind: "warn", Text: "logview: unknown flag " + unknown})
		sess.exitCode = exitUsage
		return
	}

	isRemote := remote || from != ""

	if clear {
		if jsonOut {
			sess.print(Line{Kind: "warn", Text: "logview --json has no --clear payload"})
			sess.exitCode = exitUsage
			return
		}
		if isRemote {
			sess.print(Line{Kind: "warn", Text: "logview --clear is local-only"})
			sess.exitCode = exitUsage
			return
		}
		sess.logviewClear(force)
		return
	}

	// Resolve the log source: the local cmd-logs dir, or a remote target's
	// history streamed over SSH (read-only). The loader lets the detail view
	// open a run's full record the same way in both cases.
	var (
		metas     []config.CmdLogMeta
		fromLabel string
		loader    = func(mm config.CmdLogMeta) (config.CmdLogRecord, bool) { return config.LoadCmdLog(mm.Path) }
	)
	if isRemote {
		rmetas, records, name, err := cmd.FetchRemoteCmdLogs(ctx, sess.cfg, sess.palette,
			sess.projectDir, from, sess.cmdOdooLogger("logview"))
		if err != nil {
			if errors.Is(err, cmd.ErrCancelled) || errors.Is(err, huh.ErrUserAborted) {
				sess.finalize("logview", 0, 0, cmd.ErrCancelled)
				return
			}
			emitOdooLog("ERROR", "echo.logview", "remote history unavailable: "+err.Error(),
				nil, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitError
			return
		}
		metas, fromLabel = rmetas, name
		loader = func(mm config.CmdLogMeta) (config.CmdLogRecord, bool) {
			r, ok := records[mm.Path]
			return r, ok
		}
	} else {
		metas, _ = config.ListCmdLogs(sess.projectDir)
	}

	if jsonOut {
		sess.logviewPrintJSON(metas)
		return
	}

	if list {
		sess.logviewPrintList(metas)
		return
	}

	if len(metas) == 0 {
		emitOdooLog("WARNING", "echo.logview", "no runs recorded yet",
			nil, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitUsage
		return
	}

	if !stdinIsTTY() || !stdoutIsTTY() {
		sess.print(Line{Kind: "warn", Text: "logview: needs a terminal — use --list"})
		sess.exitCode = exitUsage
		return
	}

	m := logviewModel{
		mode:      logviewList,
		metas:     metas,
		now:       time.Now(),
		retDays:   sess.cfg.CmdLogsRetentionDays,
		load:      loader,
		fromLabel: fromLabel,
		selected:  map[int]bool{},
		palette:   sess.palette,
		accent:    sess.palette.PromptColor(sess.stage),
		styles:    sess.styles,
	}
	if isRemote {
		m.retDays = 0 // retention is the remote's own concern
	}
	if last {
		if rec, ok := loader(metas[0]); ok {
			m.mode = logviewDetail
			m.record = rec
			m.recordMeta = metas[0]
			m.opened = true
		}
	}

	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		emitOdooLog("ERROR", "echo.logview", "browser failed: "+err.Error(),
			nil, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	fm := final.(logviewModel)
	if fm.quit {
		sess.handleQuit(cmd.ErrQuit)
		return
	}
	sess.logviewCloseFrame(fm)
}

// logviewCloseFrame emits the single echo.logview INFO line describing how
// the session ended: a viewed run (with the shown/total counts and any
// copy), else the run-list summary.
func (sess *session) logviewCloseFrame(fm logviewModel) {
	if fm.opened {
		shown := len(filterLogLines(fm.record.Lines, fm.textFilter, fm.minLevel))
		fields := []logField{
			{"run", fm.record.Cmd},
			{"lines", fmt.Sprintf("%d/%d", shown, len(fm.record.Lines))},
		}
		if fm.copied > 0 {
			fields = append(fields, logField{"copied", strconv.Itoa(fm.copied)})
		}
		emitOdooLog("INFO", "echo.logview", "run viewed", fields,
			sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}
	emitOdooLog("INFO", "echo.logview", "browsed",
		[]logField{{"runs", strconv.Itoa(len(fm.metas))}},
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}

// logviewPrintList prints the run list as a plain, non-interactive table
// (headless / piped path).
func (sess *session) logviewPrintList(metas []config.CmdLogMeta) {
	if len(metas) == 0 {
		emitOdooLog("WARNING", "echo.logview", "no runs recorded yet",
			nil, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitUsage
		return
	}
	now := time.Now()
	for _, mmeta := range metas {
		row := fmt.Sprintf("%-13s  %-40s  %-6s  %5d lines  %s",
			logviewTimeLabel(mmeta.Started, now),
			truncate(mmeta.Cmd, 40),
			runStatusLabel(mmeta.Exit),
			mmeta.LineCount,
			mmeta.DB)
		sess.print(Line{Kind: "out", Text: row})
	}
	emitOdooLog("INFO", "echo.logview", "runs listed",
		[]logField{{"runs", strconv.Itoa(len(metas))}},
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}

// logviewPrintJSON dumps the resolved run list as a JSON array to stdout —
// the headless / agent path. Payload goes straight to os.Stdout (the
// finishActionsJSON convention); an empty history prints `[]` with exit 0 so
// a caller reads "no runs yet" as data, not an error. Local or remote metas
// serialize identically; DeployedTip is present on `watch-deploy` records.
func (sess *session) logviewPrintJSON(metas []config.CmdLogMeta) {
	if metas == nil {
		metas = []config.CmdLogMeta{}
	}
	b, err := json.Marshal(metas)
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.logview", "encode failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
	sess.exitCode = exitOK
}

// logviewClear deletes the project's command-log history after a confirm
// (skipped with --force; non-TTY without --force fails closed).
func (sess *session) logviewClear(force bool) {
	if !force {
		if !stdinIsTTY() || !stdoutIsTTY() {
			sess.print(Line{Kind: "warn", Text: "logview --clear: needs a terminal — pass --force"})
			sess.exitCode = exitUsage
			return
		}
		confirmed := false
		form := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Delete this project's command-log history?").
				Description("Removes every saved run under cmd-logs/. This cannot be undone.").
				Affirmative("Delete").
				Negative("Cancel").
				Value(&confirmed),
		)).WithTheme(cmd.BuildHuhTheme(sess.palette))
		if err := form.Run(); err != nil || !confirmed {
			sess.finalize("logview", 0, 0, cmd.ErrCancelled)
			return
		}
	}
	removed, err := config.ClearCmdLogs(sess.projectDir)
	if err != nil {
		emitOdooLog("ERROR", "echo.logview", "clear failed: "+err.Error(),
			nil, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	emitOdooLog("INFO", "echo.logview", "history cleared",
		[]logField{{"removed", strconv.Itoa(removed)}},
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}

// parseLogviewArgs pulls the known flags; the first unrecognized token is
// returned so the caller can fail with a usage error. `--from <t>` / `--from=t`
// name a connect target and `--remote` selects this directory's link — both
// switch the browser to the remote target's log history.
func parseLogviewArgs(args []string) (list, last, clear, force, remote, jsonOut bool, from, unknown string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--list":
			list = true
		case a == "--last":
			last = true
		case a == "--clear":
			clear = true
		case a == "--force":
			force = true
		case a == "--remote":
			remote = true
		case a == "--json":
			jsonOut = true
		case a == "--from":
			if i+1 < len(args) {
				from = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--from="):
			from = strings.TrimPrefix(a, "--from=")
		default:
			if unknown == "" {
				unknown = a
			}
		}
	}
	return
}

// --- small shared helpers ----------------------------------------------

// typedText returns the printable text a key event contributes to a filter
// (rune input or a literal space), or "" for control keys.
func typedText(k tea.KeyMsg) string {
	switch k.Type {
	case tea.KeyRunes:
		return string(k.Runes)
	case tea.KeySpace:
		return " "
	}
	return ""
}

func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
