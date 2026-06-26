package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// seqBuildGlyph marks a command queued for builder mode in the sequence
// picker: Nerd Font `cod-tools` (U+EB6D), the same Codicons family as the
// `cod-package` glyph used by `modules`.
const seqBuildGlyph = ""

// SeqItem is one selectable command in the sequence picker: Name is the
// command, Desc the dim secondary column shown after it.
type SeqItem struct {
	Name string
	Desc string
}

// SeqPick is one chosen command, in execution order. Build is true when the
// user cycled it to builder mode (it should go through RunBuild).
type SeqPick struct {
	Command string
	Build   bool
}

// seqState is the tri-state of an item: off → run → build.
const (
	seqOff = iota
	seqRun
	seqBuild
)

type seqPickItem struct {
	name  string
	desc  string
	state int
}

// sequencePicker is a fzf-style tri-state, ordered multi-select. Tab cycles
// the item under the cursor off → run → build → off; the selection order
// (the order items first leave the off state) is the execution order, shown
// as a numeric badge. Mirrors the fuzzyPicker chrome (left bar, filter line).
type sequencePicker struct {
	filter   textinput.Model
	items    []seqPickItem
	visible  []int
	order    []int // item indices in selection order (state > off)
	cursor   int
	offset   int
	height   int
	title    string
	palette  theme.Palette
	accent   lipgloss.Color
	canceled bool
	quit     bool
}

func newSequencePicker(title string, items []SeqItem, palette theme.Palette) sequencePicker {
	ti := textinput.New()
	ti.Placeholder = "type to filter…"
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(palette.Faint)
	ti.Width = filterWidth
	ti.Focus()

	pis := make([]seqPickItem, len(items))
	for i, it := range items {
		pis[i] = seqPickItem{name: it.Name, desc: it.Desc}
	}
	m := sequencePicker{filter: ti, items: pis, title: title, palette: palette}
	m.setAccent(palette.Accent)
	m.recompute()
	return m
}

func (m *sequencePicker) setAccent(c lipgloss.Color) {
	m.accent = c
	m.filter.Prompt = lipgloss.NewStyle().Foreground(c).Render("filter › ")
}

func (m *sequencePicker) maxRows() int {
	if m.height <= 0 {
		return defaultListRows
	}
	r := m.height - chromeLines
	if r < 3 {
		return 3
	}
	return r
}

func (m *sequencePicker) recompute() {
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

func (m *sequencePicker) clampScroll() {
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

// cycle advances the item's tri-state and keeps the order slice in sync.
func (m *sequencePicker) cycle(idx int) {
	switch m.items[idx].state {
	case seqOff:
		m.items[idx].state = seqRun
		m.order = append(m.order, idx)
	case seqRun:
		m.items[idx].state = seqBuild // keep order position
	default: // seqBuild → off
		m.items[idx].state = seqOff
		for i, o := range m.order {
			if o == idx {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
}

// orderOf returns the 1-based selection position of idx, or 0 if unselected.
func (m *sequencePicker) orderOf(idx int) int {
	for i, o := range m.order {
		if o == idx {
			return i + 1
		}
	}
	return 0
}

func (m sequencePicker) Init() tea.Cmd { return textinput.Blink }

func (m sequencePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.quit = true
			return m, tea.Quit
		case "enter":
			return m, tea.Quit
		case "tab":
			if len(m.visible) > 0 {
				m.cycle(m.visible[m.cursor])
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

func (m sequencePicker) View() string {
	p := m.palette
	bar := lipgloss.NewStyle().Foreground(m.accent).Render("│ ")
	dim := lipgloss.NewStyle().Foreground(p.Dim)
	faint := lipgloss.NewStyle().Foreground(p.Faint)

	var b strings.Builder
	b.WriteString(bar)
	b.WriteString(dim.Render(m.title))
	b.WriteString(faint.Render(fmt.Sprintf("  (%d/%d)", len(m.visible), len(m.items))))
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
			// Badge: ⟦ ⟧ off (faint) / ⟦n⟧ selected (success).
			badge := faint.Render("⟦ ⟧")
			if it.state != seqOff {
				badge = lipgloss.NewStyle().Foreground(p.Success).Render(fmt.Sprintf("⟦%d⟧", m.orderOf(idx)))
			}
			// Builder glyph, only in build state.
			glyph := "  "
			if it.state == seqBuild {
				glyph = lipgloss.NewStyle().Foreground(m.accent).Render(seqBuildGlyph) + " "
			}
			nameStyle := lipgloss.NewStyle().Foreground(p.Fg)
			if i == m.cursor {
				nameStyle = lipgloss.NewStyle().Foreground(m.accent).Bold(true)
			}
			row := bar + cursorStr + badge + " " + glyph + nameStyle.Render(it.name)
			if it.desc != "" {
				row += dim.Render("  " + it.desc)
			}
			b.WriteString(row + "\n")
		}
		if end < len(m.visible) {
			b.WriteString(bar + dim.Render(fmt.Sprintf("↓ %d more", len(m.visible)-end)) + "\n")
		}
	}

	help := "↑↓ move · tab cycle (off→run→build) · enter continue · esc cancel · ctrl+x quit"
	b.WriteString(bar + faint.Render(help))
	return b.String()
}

// picks returns the selections in execution (selection) order.
func (m sequencePicker) picks() []SeqPick {
	out := make([]SeqPick, 0, len(m.order))
	for _, idx := range m.order {
		out = append(out, SeqPick{Command: m.items[idx].name, Build: m.items[idx].state == seqBuild})
	}
	return out
}

// RunSequencePicker shows the tri-state ordered picker and returns the
// chosen commands in order. Esc / empty selection → ErrCancelled; Ctrl+X →
// ErrQuit; a non-TTY caller → ErrNonInteractive. The accent (cursor, badge,
// left bar, filter prompt) is tinted by stage when known.
func RunSequencePicker(title string, items []SeqItem, palette theme.Palette, stage string) ([]SeqPick, error) {
	if err := requireTTY("sequence is interactive; run it from a terminal"); err != nil {
		return nil, err
	}
	m := newSequencePicker(title, items, palette)
	if stage != "" {
		m.setAccent(palette.PromptColor(theme.StageFromString(stage)))
	}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return nil, err
	}
	fm := final.(sequencePicker)
	if fm.quit {
		return nil, ErrQuit
	}
	if fm.canceled {
		return nil, ErrCancelled
	}
	picks := fm.picks()
	if len(picks) == 0 {
		return nil, ErrCancelled
	}
	return picks, nil
}
