package repl

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/pascualchavez/echo/internal/theme"
)

// cmdState is the highlight state of the first token typed in the REPL.
type cmdState int

const (
	cmdTyping  cmdState = iota // neutral: empty or still a valid prefix
	cmdValid                   // exact command match
	cmdInvalid                 // cannot become any command
)

// commandSet is the exact set of names that count as a valid command:
// the dispatch Registry plus `exit` / `quit` (handled in Start, not in
// Registry). Built once, mirroring the Registry guard in commands.go.
var commandSet = func() map[string]bool {
	m := make(map[string]bool, len(Registry)+2)
	for _, name := range Registry {
		m[name] = true
	}
	m["exit"] = true
	m["quit"] = true
	return m
}()

// isCommandName reports whether name is an exact command.
func isCommandName(name string) bool { return commandSet[name] }

// hasCommandPrefix reports whether some command starts with token. Unlike
// matchPrefix (Registry-only, used by Tab completion), this also covers
// `exit` / `quit` so typing toward them never flashes red.
func hasCommandPrefix(token string) bool {
	for name := range commandSet {
		if strings.HasPrefix(name, token) {
			return true
		}
	}
	return false
}

// classifyCommand inspects the first token of the buffer.
func classifyCommand(token string) cmdState {
	switch {
	case token == "":
		return cmdTyping
	case isCommandName(token):
		return cmdValid
	case hasCommandPrefix(token):
		return cmdTyping
	default:
		return cmdInvalid
	}
}

// firstToken splits buf into its leading non-space run (the command) and
// the remainder (including the separating space).
func firstToken(buf string) (token, rest string) {
	if i := strings.IndexByte(buf, ' '); i >= 0 {
		return buf[:i], buf[i:]
	}
	return buf, ""
}

// commandStyle returns the style for the command token and whether it
// should be recolored at all. The neutral (typing) state returns false so
// the default text style is left untouched.
func commandStyle(state cmdState, p theme.Palette) (lipgloss.Style, bool) {
	switch state {
	case cmdValid:
		return lipgloss.NewStyle().Foreground(p.Success).Bold(true), true
	case cmdInvalid:
		return lipgloss.NewStyle().Foreground(p.Error), true
	default:
		return lipgloss.Style{}, false
	}
}
