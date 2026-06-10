package repl

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// shellGlobalLine matches the namespace globals Odoo's `shell` injects and
// prints at startup (env / odoo / openerp / self), e.g.:
//
//	self: res.users(1,)
//	env: <odoo.api.Environment object at 0x…>
//
// They are not Odoo log lines, so they'd otherwise pass through raw. We
// restyle them like Echo's structured key=value fields: key in accent, the
// value dimmed.
var shellGlobalLine = regexp.MustCompile(`^(env|odoo|openerp|self): (.*)$`)

// styleShellBanner restyles the non-log lines of the Odoo `shell` startup so
// they sit coherently with the rest of Echo: the injected namespace globals
// render as accent-key + dim-value, and the Python/IPython banner lines are
// faded so they recede and the prompt stands out. Returns ("", false) for
// anything else (the caller passes it through verbatim).
func styleShellBanner(line string, s theme.Styles, p theme.Palette) (string, bool) {
	if m := shellGlobalLine.FindStringSubmatch(line); m != nil {
		keyStyle := lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
		return keyStyle.Render(m[1]) + s.Dim.Render(": "+m[2]), true
	}
	if isIPythonBanner(line) {
		return s.Faint.Render(line), true
	}
	return "", false
}

// isIPythonBanner reports whether a line is part of the stock Python/IPython
// startup banner (version line, the "Type 'help'…" hint, and the random
// "Tip: …"). These are noise next to Echo's framing, so they're faded.
func isIPythonBanner(line string) bool {
	return strings.HasPrefix(line, "Python ") ||
		strings.HasPrefix(line, "IPython ") ||
		strings.HasPrefix(line, "Type '") ||
		strings.HasPrefix(line, "Tip: ")
}
