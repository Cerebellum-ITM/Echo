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

// flagState is the highlight state of a `-`-prefixed token.
type flagState int

const (
	flagUnknown flagState = iota // a flag, but not one this command declares
	flagKnown                    // a declared flag of the current command
)

// universalFlags are accepted by every routed command (build mode, Unit
// 51) and highlight/complete as known regardless of the command's own
// commandFlags entry. Kept out of commandFlags so per-command help and the
// Registry cross-check stay clean.
var universalFlags = []string{"--build", "-b"}

// isUniversalFlag reports whether name is a universal (any-command) flag.
func isUniversalFlag(name string) bool {
	for _, f := range universalFlags {
		if f == name {
			return true
		}
	}
	return false
}

// isKnownFlag reports whether name is a declared flag of command.
func isKnownFlag(command, name string) bool {
	for _, f := range commandFlags[command] {
		if f == name {
			return true
		}
	}
	return false
}

// classifyFlag validates a flag token against the current command. The
// value part of `--flag=value` is ignored for the lookup.
func classifyFlag(command, token string) flagState {
	name := token
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	if isKnownFlag(command, name) || isUniversalFlag(name) {
		return flagKnown
	}
	return flagUnknown
}

// flagsWithPrefix returns the command's flags that start with prefix,
// preserving declaration order. Used by Tab flag completion.
func flagsWithPrefix(command, prefix string) []string {
	var out []string
	for _, f := range commandFlags[command] {
		if strings.HasPrefix(f, prefix) {
			out = append(out, f)
		}
	}
	for _, f := range universalFlags {
		if strings.HasPrefix(f, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// flagStyle colors a flag token: accent+bold when it's a known flag of
// the command, faint otherwise. Never red — forwarded/unknown flags
// should read as "unrecognized", not "wrong".
func flagStyle(state flagState, p theme.Palette) lipgloss.Style {
	if state == flagKnown {
		return lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
	}
	return lipgloss.NewStyle().Faint(true)
}

// lineStyles returns, for each rune of buf, the style to render it with
// (nil = default text style). Token 0 is the command (Unit 21 states);
// later tokens starting with `-` are flags; everything else is default.
func lineStyles(buf string, p theme.Palette) []*lipgloss.Style {
	runes := []rune(buf)
	styles := make([]*lipgloss.Style, len(runes))

	command := ""
	tokenIdx := 0
	i := 0
	for i < len(runes) {
		if runes[i] == ' ' {
			i++
			continue
		}
		start := i
		for i < len(runes) && runes[i] != ' ' {
			i++
		}
		tok := string(runes[start:i])

		var st *lipgloss.Style
		switch {
		case tokenIdx == 0:
			command = tok
			if s, ok := commandStyle(classifyCommand(tok), p); ok {
				st = &s
			}
		case strings.HasPrefix(tok, "-"):
			s := flagStyle(classifyFlag(command, tok), p)
			st = &s
		}
		if st != nil {
			for j := start; j < i; j++ {
				styles[j] = st
			}
		}
		tokenIdx++
	}
	return styles
}
