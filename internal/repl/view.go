package repl

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runView implements `view [<mod>] [--copy]`: pick a module file and
// display it through bat (syntax highlight + paging) when available, else
// print it internally through the themed Line channel (captured for
// copy-last). With --copy the file's contents go to the clipboard instead.
func (sess *session) runView(ctx context.Context, args []string) {
	opts := cmd.ViewOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
	}

	args, last := stripFlag(args, "--last")
	var res cmd.ViewResult
	var err error
	if last {
		if sess.lastViewFile == "" {
			emitOdooLog("WARNING", "echo.view", "no previous view this session",
				nil, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitUsage
			return
		}
		_, copyFlag := stripFlag(args, "--copy")
		// Replay against the same source: the current args' remote flags win,
		// else fall back to the stored ones from the original view.
		from, remote := remoteRunFlags(args)
		if from == "" && !remote {
			from, remote = sess.lastViewFrom, sess.lastViewRemote
		}
		res, err = cmd.RunViewLast(ctx, opts, sess.lastViewModule, sess.lastViewFile, copyFlag, from, remote)
	} else {
		res, err = cmd.RunView(ctx, opts)
	}
	if err != nil {
		switch {
		case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
			errors.Is(err, cmd.ErrNonInteractive):
			sess.finalize("view", 0, 0, err)
		default:
			sess.finalize("view", 1, 0, err)
		}
		return
	}
	sess.lastViewModule, sess.lastViewFile = res.Module, res.RelPath
	if !last {
		sess.lastViewFrom, sess.lastViewRemote = remoteRunFlags(args)
	}

	base := []logField{{"module", res.Module}, {"file", res.RelPath}}
	if res.From != "" {
		base = append(base, logField{"from", res.From})
	}

	if res.Copy {
		if err := clipboard.WriteAll(res.Content); err != nil {
			emitOdooLog("ERROR", "echo.view", "copy failed: "+err.Error(),
				nil, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitError
			return
		}
		emitOdooLog("INFO", "echo.view", "copied to clipboard",
			base, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}

	shown, err := cmd.ShowWithBat(res.RelPath, res.Content)
	if err != nil {
		emitOdooLog("ERROR", "echo.view", "display failed: "+err.Error(),
			base, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	via := "bat"
	if !shown {
		for _, line := range strings.Split(strings.TrimRight(res.Content, "\n"), "\n") {
			sess.print(Line{Kind: "out", Text: line})
		}
		via = "internal"
	}
	emitOdooLog("INFO", "echo.view", "displayed",
		append(base, logField{"via", via}), sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}
