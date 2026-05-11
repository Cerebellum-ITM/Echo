package repl

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// lineResult is the outcome of one line read.
type lineResult struct {
	value   string
	aborted bool // ctrl+c
	eof     bool // ctrl+d on empty line
}

type lineModel struct {
	input   textinput.Model
	history []string
	pos     int    // 0 = current, N = N-th most recent historic entry
	saved   string // current input saved when first navigating into history
	result  lineResult
	done    bool
}

func newLineModel(prompt string, history []string) lineModel {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Focus()
	ti.CharLimit = 0
	return lineModel{input: ti, history: history}
}

func (m lineModel) Init() tea.Cmd { return textinput.Blink }

func (m lineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
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
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m lineModel) View() string { return m.input.View() }

// readLine reads a single line, supporting up/down history navigation.
func readLine(prompt string, history []string) (lineResult, error) {
	m := newLineModel(prompt, history)
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return lineResult{}, err
	}
	return final.(lineModel).result, nil
}
