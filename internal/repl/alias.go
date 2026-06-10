package repl

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/cmd"
)

// runAlias implements `alias [<name>] [--list] [--rm <name>] [--migrate]`:
// manage the global project-alias registry that `-C <alias>` resolves
// against. Bare `<name>` registers the current project; `--list` shows all;
// `--rm` removes; `--migrate` backfills from connect targets. Output is
// `echo.alias` log lines, consistent with the rest of Echo.
func (sess *session) runAlias(_ context.Context, args []string) {
	res, err := cmd.RunAlias(cmd.AliasOpts{
		Cfg:  sess.cfg,
		Root: sess.projectDir,
		Args: args,
	})
	if err != nil {
		level := exitError
		if errors.Is(err, cmd.ErrAliasUsage) {
			level = exitUsage
		}
		emitOdooLog("ERROR", "echo.alias", "alias failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = level
		return
	}

	switch res.Action {
	case "set":
		emitOdooLog("INFO", "echo.alias", "alias set",
			[]logField{{"name", res.Name}, {"path", res.Path}},
			sess.styles, sess.palette, sess.cfg.DBName)
	case "remove":
		if res.Removed {
			emitOdooLog("INFO", "echo.alias", "alias removed",
				[]logField{{"name", res.Name}}, sess.styles, sess.palette, sess.cfg.DBName)
		} else {
			emitOdooLog("WARNING", "echo.alias", "alias not found",
				[]logField{{"name", res.Name}}, sess.styles, sess.palette, sess.cfg.DBName)
		}
	case "migrate":
		fields := []logField{
			{"added", strconv.Itoa(len(res.Added))},
			{"skipped", strconv.Itoa(len(res.Skipped))},
		}
		if len(res.Added) > 0 {
			fields = append(fields, logField{"names", strings.Join(res.Added, ",")})
		}
		emitOdooLog("INFO", "echo.alias", "aliases migrated", fields,
			sess.styles, sess.palette, sess.cfg.DBName)
	default: // list
		if len(res.Aliases) == 0 {
			emitOdooLog("INFO", "echo.alias", "no aliases", nil,
				sess.styles, sess.palette, sess.cfg.DBName)
			break
		}
		for _, a := range res.Aliases {
			emitOdooLog("INFO", "echo.alias", "alias",
				[]logField{{"name", a.Name}, {"path", a.Path}},
				sess.styles, sess.palette, sess.cfg.DBName)
		}
		emitOdooLog("INFO", "echo.alias", "aliases listed",
			[]logField{{"count", strconv.Itoa(len(res.Aliases))}},
			sess.styles, sess.palette, sess.cfg.DBName)
	}
	sess.exitCode = exitOK
}
