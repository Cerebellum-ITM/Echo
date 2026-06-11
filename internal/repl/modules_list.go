package repl

import (
	"context"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/cmd"
)

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

// emitModulesList prints the module names wrapped to the terminal width
// (reusing the picker's match-list layout) and closes with a count line.
func (sess *session) emitModulesList(found []string) {
	if len(found) == 0 {
		emitOdooLog("INFO", "echo.modules", "no modules",
			[]logField{{"hint", "run `modules --config` to set addons paths"}},
			sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}
	for _, line := range strings.Split(renderMatchList(found, sess.styles.Out), "\n") {
		sess.print(Line{Kind: "table", Text: line})
	}
	emitOdooLog("INFO", "echo.modules", "modules listed",
		[]logField{{"count", strconv.Itoa(len(found))}},
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}
