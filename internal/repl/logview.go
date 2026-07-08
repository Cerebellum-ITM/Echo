package repl

import (
	"context"
	"fmt"
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

// logviewChrome is the count of non-body lines the browser always renders
// (header, filter line, blank, footer), subtracted from the terminal
// height to size the scroll window.
const logviewChrome = 4

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

// filterLogLines keeps lines matching both a case-insensitive text
// substring and a min-level threshold. minLevel == "" disables the level
// gate (all lines); with a threshold, unleveled lines (level == "") are
// hidden — they only show on "all".
func filterLogLines(lines []config.ReportLine, q, minLevel string) []config.ReportLine {
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]config.ReportLine, 0, len(lines))
	for _, l := range lines {
		if q != "" && !strings.Contains(strings.ToLower(l.Text), q) {
			continue
		}
		if minLevel != "" {
			if l.Level == "" || levelRank[l.Level] < levelRank[minLevel] {
				continue
			}
		}
		out = append(out, l)
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

	// list state
	listFilter string
	cursor     int // index into the filtered run list
	listOffset int

	// detail state
	record     config.CmdLogRecord
	recordMeta config.CmdLogMeta
	textFilter string
	minLevel   string
	lineOffset int
	opened     bool // a run was opened at least once (drives the close frame)

	height  int
	palette theme.Palette
	accent  lipgloss.Color
	styles  theme.Styles

	quit   bool // ctrl+x: close Echo entirely
	copied int  // lines copied via ctrl+o
}

func (m logviewModel) Init() tea.Cmd { return nil }

func (m logviewModel) maxRows() int {
	if m.height <= 0 {
		return defaultHelpRows
	}
	r := m.height - logviewChrome
	if r < 3 {
		return 3
	}
	return r
}

func (m logviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = ws.Height
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
		m.cursor -= m.maxRows()
	case "pgdown":
		m.cursor += m.maxRows()
	case "enter":
		if len(rows) == 0 {
			return m, nil
		}
		if m.cursor >= 0 && m.cursor < len(rows) {
			sel := rows[m.cursor]
			if rec, ok := config.LoadCmdLog(sel.Path); ok {
				m.record = rec
				m.recordMeta = sel
				m.mode = logviewDetail
				m.textFilter, m.minLevel, m.lineOffset = "", "", 0
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
		if m.textFilter != "" {
			m.textFilter = ""
			m.lineOffset = 0
			return m, nil
		}
		// Back to the run list; the level filter resets.
		m.mode = logviewList
		m.minLevel = ""
		return m, nil
	case "tab":
		m.minLevel = cycleLevel(m.minLevel, false)
		m.lineOffset = 0
		return m, nil
	case "shift+tab":
		m.minLevel = cycleLevel(m.minLevel, true)
		m.lineOffset = 0
		return m, nil
	case "ctrl+o":
		lines := filterLogLines(m.record.Lines, m.textFilter, m.minLevel)
		if len(lines) > 0 {
			var b strings.Builder
			for _, l := range lines {
				b.WriteString(l.Text)
				b.WriteByte('\n')
			}
			if clipboard.WriteAll(b.String()) == nil {
				m.copied = len(lines)
			}
		}
		return m, tea.Quit
	case "up", "ctrl+p":
		m.lineOffset--
	case "down", "ctrl+n":
		m.lineOffset++
	case "pgup":
		m.lineOffset -= m.maxRows()
	case "pgdown":
		m.lineOffset += m.maxRows()
	case "ctrl+u":
		m.textFilter = ""
		m.lineOffset = 0
		return m, nil
	case "backspace":
		if m.textFilter != "" {
			m.textFilter = trimLastRune(m.textFilter)
			m.lineOffset = 0
		}
		return m, nil
	default:
		if s := typedText(k); s != "" {
			m.textFilter += s
			m.lineOffset = 0
			return m, nil
		}
		return m, nil
	}
	m.clampLineOffset(len(filterLogLines(m.record.Lines, m.textFilter, m.minLevel)))
	return m, nil
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
	rows := m.maxRows()
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

// clampLineOffset keeps the detail scroll offset valid for n filtered lines.
func (m *logviewModel) clampLineOffset(n int) {
	maxOffset := n - m.maxRows()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.lineOffset > maxOffset {
		m.lineOffset = maxOffset
	}
	if m.lineOffset < 0 {
		m.lineOffset = 0
	}
}

func (m logviewModel) View() string {
	if m.mode == logviewList {
		return m.viewList()
	}
	return m.viewDetail()
}

func (m logviewModel) viewList() string {
	p := m.palette
	bar := lipgloss.NewStyle().Foreground(m.accent).Render("│ ")
	dim := lipgloss.NewStyle().Foreground(p.Dim)
	faint := lipgloss.NewStyle().Foreground(p.Faint)
	accent := lipgloss.NewStyle().Foreground(m.accent)

	rows := filterRuns(m.metas, m.listFilter)

	var b strings.Builder
	title := fmt.Sprintf("logview — %d run%s", len(rows), plural(len(rows)))
	if m.retDays > 0 {
		title += faint.Render(fmt.Sprintf("  (%dd retention)", m.retDays))
	}
	b.WriteString(bar + dim.Render(title) + "\n")
	b.WriteString(bar + faint.Render("filter › ") + m.filterEcho(m.listFilter) + "\n")
	b.WriteString(bar + "\n")

	if len(rows) == 0 {
		b.WriteString(bar + faint.Render("no runs match") + "\n")
		b.WriteString(bar + "\n")
		b.WriteString(bar + faint.Render("type filter · esc close · ctrl+x quit"))
		return b.String()
	}

	maxR := m.maxRows()
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
	b.WriteString(bar + faint.Render("↑↓ move · enter open · type filter · esc close · ctrl+x quit"))
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
// truncation, scaled to the terminal width (fallback 40).
func (m logviewModel) cmdBudget() int {
	if m.height == 0 { // width isn't tracked separately; use a sane default
		return 40
	}
	return 48
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

func (m logviewModel) viewDetail() string {
	p := m.palette
	bar := lipgloss.NewStyle().Foreground(m.accent).Render("│ ")
	dim := lipgloss.NewStyle().Foreground(p.Dim)
	faint := lipgloss.NewStyle().Foreground(p.Faint)

	lines := filterLogLines(m.record.Lines, m.textFilter, m.minLevel)

	var b strings.Builder
	head := fmt.Sprintf("%s — %s · %s · %s",
		m.record.Cmd,
		logviewTimeLabel(m.recordMeta.Started, m.now),
		runStatusLabel(m.record.Exit),
		m.record.DB)
	b.WriteString(bar + dim.Render(head) + "\n")

	lvlLabel := "all"
	lvlStyle := faint
	if m.minLevel != "" {
		lvlLabel = m.minLevel + "+"
		lvlStyle = dim
	}
	b.WriteString(bar + faint.Render("filter › ") + m.filterEcho(m.textFilter) +
		faint.Render("   level › ") + lvlStyle.Render(lvlLabel) + "\n")
	b.WriteString(bar + "\n")

	if len(lines) == 0 {
		b.WriteString(bar + faint.Render("no lines match") + "\n")
		b.WriteString(bar + "\n")
		b.WriteString(bar + faint.Render("tab level · type filter · esc back · ctrl+x quit"))
		return b.String()
	}

	maxR := m.maxRows()
	start := m.lineOffset
	end := start + maxR
	if end > len(lines) {
		end = len(lines)
	}
	if start > 0 {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↑ %d more", start)) + "\n")
	}
	for _, l := range lines[start:end] {
		b.WriteString(bar + m.renderLogLine(l) + "\n")
	}
	if end < len(lines) {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↓ %d more", len(lines)-end)) + "\n")
	}
	b.WriteString(bar + "\n")
	b.WriteString(bar + faint.Render("↑↓ scroll · tab level · type filter · ctrl+o copy · esc back · ctrl+x quit"))
	return b.String()
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
	list, last, clear, force, unknown := parseLogviewArgs(args)
	if unknown != "" {
		sess.print(Line{Kind: "warn", Text: "logview: unknown flag " + unknown})
		sess.exitCode = exitUsage
		return
	}

	if clear {
		sess.logviewClear(force)
		return
	}

	metas, _ := config.ListCmdLogs(sess.projectDir)

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
		mode:    logviewList,
		metas:   metas,
		now:     time.Now(),
		retDays: sess.cfg.CmdLogsRetentionDays,
		palette: sess.palette,
		accent:  sess.palette.PromptColor(sess.stage),
		styles:  sess.styles,
	}
	if last {
		if rec, ok := config.LoadCmdLog(metas[0].Path); ok {
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
// returned so the caller can fail with a usage error.
func parseLogviewArgs(args []string) (list, last, clear, force bool, unknown string) {
	for _, a := range args {
		switch a {
		case "--list":
			list = true
		case "--last":
			last = true
		case "--clear":
			clear = true
		case "--force":
			force = true
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
