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
	canceled bool
	single   bool
}

// chromeLines is the number of non-list lines the picker always renders
// (title, filter, separator, blank, help) — subtracted from the terminal
// height to size the scrollable window.
const chromeLines = 6

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
}

func newFuzzyPicker(title string, available []string, palette theme.Palette) fuzzyPicker {
	ti := textinput.New()
	ti.Placeholder = "type to filter…"
	ti.Prompt = lipgloss.NewStyle().Foreground(palette.Accent).Render("❯ ")
	ti.Focus()

	items := make([]pickerItem, len(available))
	for i, n := range available {
		items[i] = pickerItem{name: n}
	}

	m := fuzzyPicker{
		filter:  ti,
		items:   items,
		title:   title,
		palette: palette,
	}
	m.recompute()
	return m
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

func (m fuzzyPicker) View() string {
	p := m.palette
	title := lipgloss.NewStyle().Foreground(p.Accent).Bold(true).Render(m.title)
	helpText := "type to filter · tab toggle · ↑↓/pgup·pgdn navigate · enter confirm · esc cancel"
	if m.single {
		helpText = "type to filter · ↑↓/pgup·pgdn navigate · enter select · esc cancel"
	}
	help := lipgloss.NewStyle().Foreground(p.Faint).Render(helpText)
	counter := lipgloss.NewStyle().Foreground(p.Dim).Render(
		fmt.Sprintf(" (%d/%d)", len(m.visible), len(m.items)),
	)

	var b strings.Builder
	b.WriteString(title + counter + "\n")
	b.WriteString(m.filter.View() + "\n")
	b.WriteString(strings.Repeat("─", 40) + "\n")

	if len(m.visible) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(p.Dim).Render("  (no matches)") + "\n")
	} else {
		rows := m.maxRows()
		start := m.offset
		end := start + rows
		if end > len(m.visible) {
			end = len(m.visible)
		}
		moreStyle := lipgloss.NewStyle().Foreground(p.Dim)
		if start > 0 {
			b.WriteString(moreStyle.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
		}
		for i := start; i < end; i++ {
			idx := m.visible[i]
			it := m.items[idx]
			cursorStr := "  "
			if i == m.cursor {
				cursorStr = lipgloss.NewStyle().Foreground(p.Accent2).Render("❯ ")
			}
			name := it.name
			if i == m.cursor {
				name = lipgloss.NewStyle().Foreground(p.Accent).Bold(true).Render(name)
			}
			if m.single {
				b.WriteString(cursorStr + name + "\n")
				continue
			}
			checkbox := lipgloss.NewStyle().Foreground(p.Faint).Render("[ ]")
			if it.selected {
				checkbox = lipgloss.NewStyle().Foreground(p.Success).Render("[×]")
			}
			b.WriteString(cursorStr + checkbox + " " + name + "\n")
		}
		if end < len(m.visible) {
			b.WriteString(moreStyle.Render(fmt.Sprintf("  ↓ %d more", len(m.visible)-end)) + "\n")
		}
	}

	b.WriteString("\n" + help)
	return b.String()
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

// runFuzzyPicker shows the picker and returns the selected items. Empty
// selection or user cancel returns ErrCancelled.
func runFuzzyPicker(title string, available []string, palette theme.Palette) ([]string, error) {
	m := newFuzzyPicker(title, available, palette)
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return nil, err
	}
	fm := final.(fuzzyPicker)
	if fm.canceled {
		return nil, ErrCancelled
	}
	picked := fm.selectedNames()
	if len(picked) == 0 {
		return nil, ErrCancelled
	}
	return picked, nil
}

// runSingleFuzzyPicker is the single-select variant: Enter commits the
// highlighted row. Returns ErrCancelled on Esc / empty list.
func runSingleFuzzyPicker(title string, available []string, palette theme.Palette) (string, error) {
	m := newFuzzyPicker(title, available, palette)
	m.single = true
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	fm := final.(fuzzyPicker)
	if fm.canceled || len(fm.visible) == 0 {
		return "", ErrCancelled
	}
	return fm.items[fm.visible[fm.cursor]].name, nil
}
