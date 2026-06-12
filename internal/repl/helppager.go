package repl

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/theme"
	"golang.org/x/term"
)

// stdinIsTTY / stdoutIsTTY report whether the standard streams are real
// terminals. They are package-level seams so tests can force the flat
// (non-pager) help path without a real TTY (mirrors stdinIsTTY in cmd).
var (
	stdinIsTTY  = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	stdoutIsTTY = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
)

// helpPage is one pager screen: a section title (the tab label) and its
// pre-rendered entry lines.
type helpPage struct {
	title string
	lines []string
}

// helpPager is the interactive `help` viewer: one help section per page,
// ←/→ (or tab) to move between sections, ↑/↓ to scroll inside a tall
// section. It renders in the same "log-framed" style as the fuzzy picker —
// a single left bar `│` tinted by the project stage — so it blends with
// the rest of the REPL output.
type helpPager struct {
	pages   []helpPage
	page    int
	offset  int // first rendered line within the current page (scroll)
	height  int // terminal height; 0 until the first WindowSizeMsg
	palette theme.Palette
	accent  lipgloss.Color
	quit    bool // ctrl+x: close Echo entirely, not just the pager
}

// helpChromeLines is the number of non-body lines the pager always renders
// (tab header, blank, blank, footer) — subtracted from the terminal height
// to size the scrollable window.
const helpChromeLines = 4

// maxRows is how many body lines fit in the current viewport.
func (m helpPager) maxRows() int {
	if m.height <= 0 {
		return defaultHelpRows
	}
	r := m.height - helpChromeLines
	if r < 3 {
		return 3
	}
	return r
}

// defaultHelpRows is the window size used before the first WindowSizeMsg.
const defaultHelpRows = 20

func (m helpPager) Init() tea.Cmd { return nil }

func (m helpPager) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = ws.Height
		m.clampScroll()
		return m, nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "esc", "q", "enter", "ctrl+c":
		return m, tea.Quit
	case "ctrl+x":
		m.quit = true
		return m, tea.Quit
	case "right", "l", "tab":
		m.page = (m.page + 1) % len(m.pages)
		m.offset = 0
	case "left", "h", "shift+tab":
		m.page = (m.page - 1 + len(m.pages)) % len(m.pages)
		m.offset = 0
	case "up", "k", "ctrl+p":
		m.offset--
		m.clampScroll()
	case "down", "j", "ctrl+n":
		m.offset++
		m.clampScroll()
	case "pgup":
		m.offset -= m.maxRows()
		m.clampScroll()
	case "pgdown":
		m.offset += m.maxRows()
		m.clampScroll()
	}
	return m, nil
}

// clampScroll keeps offset within the current page's bounds.
func (m *helpPager) clampScroll() {
	maxOffset := len(m.pages[m.page].lines) - m.maxRows()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m helpPager) View() string {
	p := m.palette
	bar := lipgloss.NewStyle().Foreground(m.accent).Render("│ ")
	dim := lipgloss.NewStyle().Foreground(p.Dim)
	faint := lipgloss.NewStyle().Foreground(p.Faint)
	active := lipgloss.NewStyle().Foreground(m.accent).Bold(true)

	// Tab header: every section title, the current one highlighted.
	tabs := make([]string, len(m.pages))
	for i, pg := range m.pages {
		if i == m.page {
			tabs[i] = active.Render(pg.title)
		} else {
			tabs[i] = dim.Render(pg.title)
		}
	}
	var b strings.Builder
	b.WriteString(bar + dim.Render("help ") +
		faint.Render(fmt.Sprintf("(%d/%d)  ", m.page+1, len(m.pages))) +
		strings.Join(tabs, faint.Render(" · ")) + "\n")
	b.WriteString(bar + "\n")

	lines := m.pages[m.page].lines
	rows := m.maxRows()
	start := m.offset
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}
	if start > 0 {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↑ %d more", start)) + "\n")
	}
	for _, line := range lines[start:end] {
		b.WriteString(bar + line + "\n")
	}
	if end < len(lines) {
		b.WriteString(bar + dim.Render(fmt.Sprintf("↓ %d more", len(lines)-end)) + "\n")
	}

	b.WriteString(bar + "\n")
	b.WriteString(bar + faint.Render("←→/tab section · ↑↓ scroll · esc close · ctrl+x quit"))
	return b.String()
}

// helpPages builds the pager pages: one per help section, plus the
// Scripting and Build-mode extras (which live outside helpSections() so the
// registry cross-check stays clean).
func (sess *session) helpPages() []helpPage {
	var pages []helpPage
	for _, sec := range helpSections() {
		pages = append(pages, helpPage{title: sec.title, lines: renderHelpEntries(sess.styles, sec.items)})
	}
	pages = append(pages,
		helpPage{title: "Scripting", lines: renderHelpEntries(sess.styles, scriptingHelpEntries)},
		helpPage{title: "Build", lines: renderHelpEntries(sess.styles, buildHelpEntries)},
	)
	return pages
}

// renderHelpEntries renders a section's rows in the same two-column style
// the flat help uses: padded command label in Info, description in Out.
func renderHelpEntries(s theme.Styles, items []helpEntry) []string {
	out := make([]string, len(items))
	for i, it := range items {
		label := lipgloss.NewStyle().Width(22).Render(it.cmd)
		out[i] = "  " + s.Info.Render(label) + s.Out.Render(it.desc)
	}
	return out
}

// runHelpPager shows the paginated help. Returns cmd.ErrQuit on Ctrl+X so
// the caller can close Echo like any picker does; any other outcome
// (including esc) is a normal close.
func (sess *session) runHelpPager() error {
	m := helpPager{
		pages:   sess.helpPages(),
		palette: sess.palette,
		accent:  sess.palette.PromptColor(sess.stage),
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	if final.(helpPager).quit {
		return cmd.ErrQuit
	}
	return nil
}
