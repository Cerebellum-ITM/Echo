package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// fuzzyPicker is a fzf-style multi-select: filter is always active, no
// `/` prefix needed. Tab toggles, Enter confirms, Esc cancels. When
// `single` is true the picker disables Tab and Enter returns the
// highlighted item directly.
type fuzzyPicker struct {
	filter   textinput.Model
	items    []pickerItem
	visible  []int
	cursor   int
	offset   int // index into visible of the first rendered row (scroll)
	height   int // terminal height; 0 until the first WindowSizeMsg
	title    string
	palette  theme.Palette
	accent   lipgloss.Color // highlight color; stage-tinted when known
	canceled bool
	quit     bool // ctrl+x: close Echo entirely, not just this picker
	single   bool
}

// chromeLines is the number of non-list lines the picker always renders
// (title, filter, help, +1 safety) — subtracted from the terminal height to
// size the scrollable window. The log-framed style dropped the divider and
// blank line that the old boxy style carried.
const chromeLines = 4

// defaultListRows is the window size used before the first WindowSizeMsg
// arrives (or when the height is unknown).
const defaultListRows = 15

// maxRows is how many list items fit in the current viewport.
func (m fuzzyPicker) maxRows() int {
	if m.height <= 0 {
		return defaultListRows
	}
	r := m.height - chromeLines
	if r < 3 {
		return 3
	}
	return r
}

type pickerItem struct {
	name     string
	selected bool
	recent   bool // part of the previous run (e.g. last `update`); tinted
}

// filterWidth gives the filter input a non-zero Width. bubbles' textinput
// truncates its placeholder to a single rune when Width is 0 (it sizes the
// placeholder buffer to Width+1), so "type to filter…" rendered as just "t".
// A fixed width is plenty for a fuzzy filter and makes the placeholder show
// in full; the trailing padding is invisible spaces.
const filterWidth = 48

func newFuzzyPicker(title string, available, recent []string, palette theme.Palette) fuzzyPicker {
	ti := textinput.New()
	ti.Placeholder = "type to filter…"
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(palette.Faint)
	ti.Width = filterWidth
	ti.Focus()

	recentSet := make(map[string]bool, len(recent))
	for _, r := range recent {
		recentSet[r] = true
	}
	items := make([]pickerItem, len(available))
	for i, n := range available {
		items[i] = pickerItem{name: n, recent: recentSet[n]}
	}

	m := fuzzyPicker{
		filter:  ti,
		items:   items,
		title:   title,
		palette: palette,
	}
	m.setAccent(palette.Accent)
	m.recompute()
	return m
}

// setAccent sets the picker's highlight color (cursor, selected name, filter
// caret) and rebuilds the filter prompt to match. Stage-aware callers pass
// palette.PromptColor(stage); the default is palette.Accent.
func (m *fuzzyPicker) setAccent(c lipgloss.Color) {
	m.accent = c
	m.filter.Prompt = lipgloss.NewStyle().Foreground(m.palette.Faint).Render("filter ") +
		lipgloss.NewStyle().Foreground(c).Render("› ")
}

func (m *fuzzyPicker) recompute() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	m.visible = m.visible[:0]
	for i, it := range m.items {
		if q == "" || strings.Contains(strings.ToLower(it.name), q) {
			m.visible = append(m.visible, i)
		}
	}
	if m.cursor >= len(m.visible) {
		if len(m.visible) > 0 {
			m.cursor = len(m.visible) - 1
		} else {
			m.cursor = 0
		}
	}
	m.clampScroll()
}

// clampScroll keeps offset within bounds and the cursor inside the
// visible window.
func (m *fuzzyPicker) clampScroll() {
	rows := m.maxRows()
	maxOffset := len(m.visible) - rows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	} else if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m fuzzyPicker) Init() tea.Cmd { return textinput.Blink }

func (m fuzzyPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = ws.Height
		m.clampScroll()
		return m, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			return m, tea.Quit
		case "ctrl+x":
			// Quit Echo entirely (nano-style), mirroring Ctrl+X at the REPL
			// prompt — not just cancel this picker.
			m.quit = true
			return m, tea.Quit
		case "enter":
			return m, tea.Quit
		case "tab":
			if m.single {
				return m, nil
			}
			if len(m.visible) > 0 {
				idx := m.visible[m.cursor]
				m.items[idx].selected = !m.items[idx].selected
			}
			return m, nil
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
				m.clampScroll()
			}
			return m, nil
		case "down", "ctrl+n":
			if m.cursor < len(m.visible)-1 {
				m.cursor++
				m.clampScroll()
			}
			return m, nil
		case "pgup":
			m.cursor -= m.maxRows()
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.clampScroll()
			return m, nil
		case "pgdown":
			m.cursor += m.maxRows()
			if m.cursor > len(m.visible)-1 {
				m.cursor = len(m.visible) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.clampScroll()
			return m, nil
		}
	}

	prev := m.filter.Value()
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	if m.filter.Value() != prev {
		m.recompute()
	}
	return m, cmd
}

// splitLabel divides a columnar picker label at its first run of 2+ spaces:
// head is the name, tail (including the gap) is the secondary column
// (host:path, full name, …) rendered dim. A single-word label has no tail.
func splitLabel(s string) (head, tail string) {
	if i := strings.Index(s, "  "); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// View renders the picker in the "log-framed" style: a subdued header and a
// body (filter line, rows, help) hung off a single left bar `│` colored by
// the target's stage (m.accent) — green dev / yellow staging / red prod, or
// the default accent when the stage isn't known. No boxy title or `────`
// divider, so it blends into the surrounding Odoo-style log stream while the
// bar makes the environment legible at a glance.
func (m fuzzyPicker) View() string {
	p := m.palette
	helpText := "↑↓/pgup·pgdn move · tab toggle · enter confirm · esc cancel · ctrl+x quit"
	if m.single {
		helpText = "↑↓/pgup·pgdn move · enter select · esc cancel · ctrl+x quit"
	}
	if m.hasRecent() {
		helpText += " · highlighted = last update"
	}

	bar := lipgloss.NewStyle().Foreground(m.accent).Render("│ ")
	dim := lipgloss.NewStyle().Foreground(p.Dim)

	var b strings.Builder
	b.WriteString(bar)
	b.WriteString(lipgloss.NewStyle().Foreground(p.Dim).Render(m.title))
	b.WriteString(lipgloss.NewStyle().Foreground(p.Faint).Render(
		fmt.Sprintf("  (%d/%d)", len(m.visible), len(m.items))))
	b.WriteString("\n" + bar + m.filter.View() + "\n")

	if len(m.visible) == 0 {
		b.WriteString(bar + dim.Render("(no matches)") + "\n")
	} else {
		rows := m.maxRows()
		start := m.offset
		end := start + rows
		if end > len(m.visible) {
			end = len(m.visible)
		}
		if start > 0 {
			b.WriteString(bar + dim.Render(fmt.Sprintf("↑ %d more", start)) + "\n")
		}
		for i := start; i < end; i++ {
			idx := m.visible[i]
			it := m.items[idx]
			cursorStr := "  "
			if i == m.cursor {
				cursorStr = lipgloss.NewStyle().Foreground(m.accent).Render("❯ ")
			}
			head, tail := splitLabel(it.name)
			headStyle := lipgloss.NewStyle().Foreground(p.Fg)
			switch {
			case i == m.cursor:
				headStyle = lipgloss.NewStyle().Foreground(m.accent).Bold(true)
			case it.recent:
				headStyle = lipgloss.NewStyle().Foreground(p.Info)
			}
			row := bar + cursorStr
			if !m.single {
				checkbox := lipgloss.NewStyle().Foreground(p.Faint).Render("[ ]")
				if it.selected {
					checkbox = lipgloss.NewStyle().Foreground(p.Success).Render("[×]")
				}
				row += checkbox + " "
			}
			row += headStyle.Render(head)
			if tail != "" {
				row += dim.Render(tail)
			}
			b.WriteString(row + "\n")
		}
		if end < len(m.visible) {
			b.WriteString(bar + dim.Render(fmt.Sprintf("↓ %d more", len(m.visible)-end)) + "\n")
		}
	}

	b.WriteString(bar + lipgloss.NewStyle().Foreground(p.Faint).Render(helpText))
	return b.String()
}

// hasRecent reports whether any item is marked as part of the previous
// run, so the picker only shows the "last update" legend when relevant.
func (m fuzzyPicker) hasRecent() bool {
	for _, it := range m.items {
		if it.recent {
			return true
		}
	}
	return false
}

func (m fuzzyPicker) selectedNames() []string {
	var out []string
	for _, it := range m.items {
		if it.selected {
			out = append(out, it.name)
		}
	}
	return out
}

// runFuzzyPickerCore shows the multi-select and returns the selected
// names, whether the user canceled (Esc / ctrl+c), and any run error. An
// empty `picked` with `canceled == false` means Enter on an empty
// selection — the caller decides what that means. `recent` tints the
// previous run's items.
func runFuzzyPickerCore(title string, available, recent []string, palette theme.Palette, stage string) (picked []string, canceled bool, err error) {
	if err := requireTTY("pass the selection as command arguments"); err != nil {
		return nil, false, err
	}
	m := newFuzzyPicker(title, available, recent, palette)
	if stage != "" {
		m.setAccent(palette.PromptColor(theme.StageFromString(stage)))
	}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return nil, false, err
	}
	fm := final.(fuzzyPicker)
	if fm.quit {
		return nil, false, ErrQuit
	}
	if fm.canceled {
		return nil, true, nil
	}
	return fm.selectedNames(), false, nil
}

// runFuzzyPicker shows the picker and returns the selected items. Empty
// selection or user cancel returns ErrCancelled.
func runFuzzyPicker(title string, available []string, palette theme.Palette) ([]string, error) {
	picked, canceled, err := runFuzzyPickerCore(title, available, nil, palette, "")
	if err != nil {
		return nil, err
	}
	if canceled || len(picked) == 0 {
		return nil, ErrCancelled
	}
	return picked, nil
}

// runSingleFuzzyPicker is the single-select variant: Enter commits the
// highlighted row. Returns ErrCancelled on Esc / empty list.
func runSingleFuzzyPicker(title string, available []string, palette theme.Palette) (string, error) {
	return runSingleFuzzyPickerStaged(title, available, palette, "")
}

// runSingleFuzzyPickerStaged is runSingleFuzzyPicker with the highlight
// tinted by the target's stage (dev/staging/prod). An empty stage keeps the
// default accent — used by pickers whose stage isn't yet known (e.g. the
// connect/i18n-pull target picker).
func runSingleFuzzyPickerStaged(title string, available []string, palette theme.Palette, stage string) (string, error) {
	if err := requireTTY("pass the selection as a command argument"); err != nil {
		return "", err
	}
	m := newFuzzyPicker(title, available, nil, palette)
	m.single = true
	if stage != "" {
		m.setAccent(palette.PromptColor(theme.StageFromString(stage)))
	}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	fm := final.(fuzzyPicker)
	if fm.quit {
		return "", ErrQuit
	}
	if fm.canceled || len(fm.visible) == 0 {
		return "", ErrCancelled
	}
	return fm.items[fm.visible[fm.cursor]].name, nil
}

// PickOne opens a single-select fuzzy picker over options and returns the
// chosen value. Esc / empty list → ErrCancelled; a non-TTY caller →
// ErrNonInteractive. Exported for callers outside the cmd package (the
// recipe runner's `--pick` selector).
func PickOne(title string, options []string, palette theme.Palette) (string, error) {
	return runSingleFuzzyPicker(title, options, palette)
}
