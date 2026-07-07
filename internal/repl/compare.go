package repl

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runCompare implements `compare [<mod>] [--from <t>|--remote] [--copy]`:
// pick a local module file and diff it against its copy inside Docker (the
// local container, or a remote target's). Identical contents print a single
// line; a real diff is shown through bat (--language=diff) with an internal
// plain fallback. With --copy the raw unified diff goes to the clipboard.
func (sess *session) runCompare(ctx context.Context, args []string) {
	opts := cmd.CompareOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
	}

	res, err := cmd.RunCompare(ctx, opts)
	if err != nil {
		switch {
		case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
			errors.Is(err, cmd.ErrNonInteractive):
			sess.finalize("compare", 0, 0, err)
		default:
			sess.finalize("compare", 1, 0, err)
		}
		return
	}

	base := []logField{{"module", res.Module}, {"file", res.RelPath}}
	if res.From != "" {
		base = append(base, logField{"from", res.From})
	}

	if res.MissingInContainer {
		emitOdooLog("WARNING", "echo.compare", "file missing in container — showing full file as added",
			base, sess.styles, sess.palette, sess.cfg.DBName)
	}

	if res.Copy {
		if err := clipboard.WriteAll(res.Diff); err != nil {
			emitOdooLog("ERROR", "echo.compare", "copy failed: "+err.Error(),
				base, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitError
			return
		}
		emitOdooLog("INFO", "echo.compare", "copied diff to clipboard",
			append(base, logField{"result", diffResult(res.Identical)}),
			sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}

	if res.Identical {
		emitOdooLog("INFO", "echo.compare", "identical — no differences",
			append(base, logField{"result", "identical"}),
			sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}

	shown, err := cmd.ShowWithBat(res.RelPath+".diff", res.Diff)
	if err != nil {
		emitOdooLog("ERROR", "echo.compare", "display failed: "+err.Error(),
			base, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	via := "bat"
	if !shown {
		for _, line := range strings.Split(strings.TrimRight(res.Diff, "\n"), "\n") {
			sess.print(Line{Kind: "out", Text: line})
		}
		via = "internal"
	}
	emitOdooLog("INFO", "echo.compare", "diff shown",
		append(base, logField{"result", "different"}, logField{"via", via}),
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}

// diffResult labels a copy's payload for the log frame.
func diffResult(identical bool) string {
	if identical {
		return "identical"
	}
	return "different"
}
