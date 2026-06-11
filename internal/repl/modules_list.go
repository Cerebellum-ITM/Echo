package repl

import (
	"context"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/cmd"
)

// modIcon is the nerd-font `cod-package` glyph (U+EB29) prefixed to every
// module name in the `modules` list.
const modIcon = ""

// runModulesList renders `modules` as a wrapped, theme-styled list closing
// with an Odoo-style count line, instead of one bare name per line. The
// `--config` addons-path picker keeps its streamed output via RunModules.
func (sess *session) runModulesList(ctx context.Context, opts cmd.ModulesOpts, args []string) {
	sess.startLog("modules", args)
	for _, a := range args {
		if a == "--config" {
			sess.readonlyFinalize("modules", cmd.RunModules(ctx, opts))
			return
		}
	}
	found, err := cmd.ModulesList(ctx, opts)
	if err != nil {
		found = nil // a resolve failure reads as "no modules", as before
	}
	sess.emitModulesList(found)
}

// emitModulesList prints the module names wrapped to the terminal width, each
// prefixed by the package glyph and colored, closing with a count line.
func (sess *session) emitModulesList(found []string) {
	if len(found) == 0 {
		emitOdooLog("INFO", "echo.modules", "no modules",
			[]logField{{"hint", "run `modules --config` to set addons paths"}},
			sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}
	for _, line := range sess.renderModuleList(found) {
		sess.print(Line{Kind: "table", Text: line})
	}
	emitOdooLog("INFO", "echo.modules", "modules listed",
		[]logField{{"count", strconv.Itoa(len(found))}},
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}

// renderModuleList lays the module names out in terminal-width rows, each cell
// an accent-colored package glyph followed by the name in the output style.
// It mirrors renderMatchList's wrapping but styles per item (so the picker's
// shared renderMatchList stays untouched). Width math uses the visible cell
// width, not the ANSI-wrapped string length.
func (sess *session) renderModuleList(found []string) []string {
	width := terminalWidth()
	const sep = "  "
	icon := lipgloss.NewStyle().Foreground(sess.palette.Accent).Render(modIcon)
	iconW := lipgloss.Width(modIcon)

	var lines []string
	var cur strings.Builder
	curWidth := 0
	for _, name := range found {
		cell := icon + " " + sess.styles.Out.Render(name)
		cellW := iconW + 1 + lipgloss.Width(name) // glyph + space + name
		add := cellW
		if cur.Len() > 0 {
			add += len(sep)
		}
		if curWidth+add > width && cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curWidth = 0
			add = cellW
		}
		if cur.Len() > 0 {
			cur.WriteString(sep)
		}
		cur.WriteString(cell)
		curWidth += add
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
