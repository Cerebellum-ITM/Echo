package repl

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// lineResult is the outcome of one line read.
type lineResult struct {
	value   string
	aborted bool // ctrl+c
	eof     bool // ctrl+d on empty line
}

type lineModel struct {
	input      textinput.Model
	history    []string
	pos        int    // 0 = current, N = N-th most recent historic entry
	saved      string // current input saved when first navigating into history
	result     lineResult
	done       bool
	lastWasTab bool
	infoStyle  lipgloss.Style
}

func newLineModel(prompt string, history []string, info lipgloss.Style) lineModel {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Focus()
	ti.CharLimit = 0
	return lineModel{input: ti, history: history, infoStyle: info}
}

func (m lineModel) Init() tea.Cmd { return textinput.Blink }

func (m lineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		key := k.String()
		if key != "tab" {
			m.lastWasTab = false
		}
		switch key {
		case "enter":
			m.result = lineResult{value: m.input.Value()}
			m.done = true
			return m, tea.Quit
		case "ctrl+d":
			if m.input.Value() == "" {
				m.result = lineResult{eof: true}
				m.done = true
				return m, tea.Quit
			}
		case "ctrl+c":
			m.result = lineResult{aborted: true}
			m.done = true
			return m, tea.Quit
		case "up":
			if m.pos == 0 {
				m.saved = m.input.Value()
			}
			if m.pos < len(m.history) {
				m.pos++
				v := m.history[len(m.history)-m.pos]
				m.input.SetValue(v)
				m.input.SetCursor(len(v))
			}
			return m, nil
		case "down":
			if m.pos > 0 {
				m.pos--
				var v string
				if m.pos == 0 {
					v = m.saved
				} else {
					v = m.history[len(m.history)-m.pos]
				}
				m.input.SetValue(v)
				m.input.SetCursor(len(v))
			}
			return m, nil
		case "tab":
			return m.handleTab()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m lineModel) View() string { return m.input.View() }

// handleTab implements bash-style prefix completion against Registry,
// limited to the first token of the buffer.
func (m lineModel) handleTab() (tea.Model, tea.Cmd) {
	buf := m.input.Value()
	if buf == "" || strings.Contains(buf, " ") {
		m.lastWasTab = false
		return m, nil
	}
	matches := matchPrefix(buf)
	switch len(matches) {
	case 0:
		m.lastWasTab = false
		return m, nil
	case 1:
		full := matches[0] + " "
		m.input.SetValue(full)
		m.input.SetCursor(len(full))
		m.lastWasTab = false
		return m, nil
	default:
		lcp := longestCommonPrefix(matches)
		if lcp != buf {
			m.input.SetValue(lcp)
			m.input.SetCursor(len(lcp))
			m.lastWasTab = true
			return m, nil
		}
		if m.lastWasTab {
			m.lastWasTab = false
			return m, tea.Println(renderMatchList(matches, m.infoStyle))
		}
		m.lastWasTab = true
		return m, nil
	}
}

// renderMatchList formats matches as a space-separated list wrapped to
// the terminal width, styled with the active palette's Info style.
func renderMatchList(matches []string, info lipgloss.Style) string {
	width := terminalWidth()
	const sep = "  "
	var lines []string
	var cur strings.Builder
	curWidth := 0
	for _, name := range matches {
		w := lipgloss.Width(name)
		add := w
		if cur.Len() > 0 {
			add += len(sep)
		}
		if curWidth+add > width && cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curWidth = 0
			add = w
		}
		if cur.Len() > 0 {
			cur.WriteString(sep)
		}
		cur.WriteString(name)
		curWidth += add
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return info.Render(strings.Join(lines, "\n"))
}

func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// readLine reads a single line, supporting up/down history navigation
// and Tab completion against Registry.
func readLine(prompt string, history []string, info lipgloss.Style) (lineResult, error) {
	m := newLineModel(prompt, history, info)
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return lineResult{}, err
	}
	return final.(lineModel).result, nil
}
