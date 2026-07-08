package repl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runCompare implements `compare [<mod>] [--from <t>|--remote] [--copy]`:
// pick a local module file and diff it against its copy inside Docker (the
// local container, or a remote target's). Identical contents print a single
// line; a real diff is shown through bat (--language=diff) with an internal
// plain fallback. With --copy the raw unified diff goes to the clipboard.
func (sess *session) runCompare(ctx context.Context, args []string) {
	if compareWantsAll(args) {
		sess.runCompareAll(ctx, args)
		return
	}

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

// compareWantsAll reports whether the `--all` module-mode flag is present.
func compareWantsAll(args []string) bool {
	for _, a := range args {
		if a == "--all" {
			return true
		}
	}
	return false
}

// runCompareAll implements `compare <mod> --all`: a whole-module sync-status
// table (changed/added/missing/equal) closed by a verdict frame, then — on a
// TTY without --copy — an interactive drill-down into each differing file's
// diff (the Unit 80 renderer).
func (sess *session) runCompareAll(ctx context.Context, args []string) {
	opts := cmd.CompareOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
	}

	res, err := cmd.RunCompareAll(ctx, opts)
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

	base := []logField{{"module", res.Module}}
	if res.From != "" {
		base = append(base, logField{"from", res.From})
	}

	// All in sync: single line, skip the table and drill-down.
	if len(res.Rows) == 0 {
		emitOdooLog("INFO", "echo.compare", "in sync",
			append(base, logField{"equal", strconv.Itoa(res.Equal)}),
			sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}

	changed, added, missing := compareCounts(res.Rows)

	// Render the aligned status table (modstate style) + collect a plain
	// form for --copy.
	relW := len("file")
	for _, r := range res.Rows {
		if len(r.Rel) > relW {
			relW = len(r.Rel)
		}
	}
	accent := lipgloss.NewStyle().Bold(true).Foreground(sess.palette.Accent)
	header := "  " + accent.Render(pad("file", relW)) + "  " + accent.Render("status")
	var plain strings.Builder
	plain.WriteString(pad("file", relW) + "  status\n")

	if !res.Copy {
		sess.print(Line{Kind: "table", Text: header})
	}
	for _, r := range res.Rows {
		line := "  " + sess.styles.Out.Render(pad(r.Rel, relW)) + "  " + sess.compareStatusStyled(r.Status)
		if !res.Copy {
			sess.print(Line{Kind: "table", Text: line})
		}
		plain.WriteString(pad(r.Rel, relW) + "  " + r.Status + "\n")
	}

	verdict := append(base,
		logField{"changed", strconv.Itoa(changed)},
		logField{"added", strconv.Itoa(added)},
		logField{"missing", strconv.Itoa(missing)},
		logField{"equal", strconv.Itoa(res.Equal)},
	)

	if res.Copy {
		plain.WriteString(fmt.Sprintf("\nmodule compared: changed=%d added=%d missing=%d equal=%d\n",
			changed, added, missing, res.Equal))
		if err := clipboard.WriteAll(plain.String()); err != nil {
			emitOdooLog("ERROR", "echo.compare", "copy failed: "+err.Error(),
				base, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitError
			return
		}
		emitOdooLog("INFO", "echo.compare", "copied table to clipboard",
			verdict, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}

	emitOdooLog("INFO", "echo.compare", "module compared",
		verdict, sess.styles, sess.palette, sess.cfg.DBName)

	// Interactive drill-down over the differing files (TTY only).
	if stdinIsTTY() && stdoutIsTTY() {
		sess.compareDrillDown(ctx, opts, res)
	}
	sess.exitCode = exitOK
}

// compareDrillDown loops: pick a differing file → show its diff → back to
// the picker, until esc (ErrCancelled). Ctrl+X closes Echo.
func (sess *session) compareDrillDown(ctx context.Context, opts cmd.CompareOpts, res cmd.CompareAllResult) {
	labels := make([]string, len(res.Rows))
	for i, r := range res.Rows {
		labels[i] = r.Rel
	}
	for {
		pick, err := cmd.PickOne("Changed files in "+res.Module, labels, sess.palette)
		if err != nil {
			if sess.handleQuit(err) {
				return
			}
			return // ErrCancelled/esc: normal end of the loop
		}
		cr, err := cmd.CompareModuleFile(ctx, opts, res.Module, pick)
		if err != nil {
			emitOdooLog("WARNING", "echo.compare", "diff failed: "+err.Error(),
				[]logField{{"file", pick}}, sess.styles, sess.palette, sess.cfg.DBName)
			continue
		}
		if cr.Identical {
			emitOdooLog("INFO", "echo.compare", "no differences",
				[]logField{{"file", pick}}, sess.styles, sess.palette, sess.cfg.DBName)
			continue
		}
		shown, serr := cmd.ShowWithBat(cr.RelPath+".diff", cr.Diff)
		if serr != nil {
			emitOdooLog("ERROR", "echo.compare", "display failed: "+serr.Error(),
				[]logField{{"file", pick}}, sess.styles, sess.palette, sess.cfg.DBName)
			continue
		}
		if !shown {
			for _, line := range strings.Split(strings.TrimRight(cr.Diff, "\n"), "\n") {
				sess.print(Line{Kind: "out", Text: line})
			}
		}
	}
}

// compareStatusStyled colors a status token: changed=Warn, added=Info,
// missing=Err.
func (sess *session) compareStatusStyled(status string) string {
	switch status {
	case "changed":
		return sess.styles.Warn.Render(status)
	case "added":
		return sess.styles.Info.Render(status)
	case "missing":
		return sess.styles.Err.Render(status)
	default:
		return sess.styles.Out.Render(status)
	}
}

// compareCounts tallies the rows by status for the verdict frame.
func compareCounts(rows []cmd.FileStatus) (changed, added, missing int) {
	for _, r := range rows {
		switch r.Status {
		case "changed":
			changed++
		case "added":
			added++
		case "missing":
			missing++
		}
	}
	return
}
