package repl

import (
	"context"
	"errors"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/cmd"
)

// runPush implements `push [<mod>...] [--from <t>|--remote] [--dirty]
// [--dry-run] [--delete] [--force]`: rsync the selected local modules to the
// remote target's addons dir over SSH. Progress renders as `echo.push` log
// lines; each module's file changes render as a colored change tree between
// its syncing/synced frame.
func (sess *session) runPush(ctx context.Context, args []string) {
	err := cmd.RunPush(ctx, cmd.PushOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("push"),
		OnSync:  sess.renderSyncTree,
	})

	switch {
	case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted),
		errors.Is(err, cmd.ErrNonInteractive), errors.Is(err, cmd.ErrUsage):
		sess.finalize("push", 0, 0, err)
		if errors.Is(err, cmd.ErrUsage) {
			sess.exitCode = exitUsage
		}
	case err != nil:
		sess.commandFailureLog("push", err, 0, 0)
	default:
		sess.finalize("push", 0, 0, nil)
	}
}

// renderSyncTree prints a module's file changes as a colored tree: dim tree
// connectors, an operation glyph tinted by kind (+ new = success, ~ changed =
// warning, − deleted = error), directory nodes dimmed. When icons are enabled
// a nerd-font file-type glyph precedes each name (folder glyph for dir nodes);
// the ANSI-free `plain` form kept for copy-last/--log never carries icons.
// Backs push's OnSync.
func (sess *session) renderSyncTree(changes []cmd.FileChange) {
	p := sess.palette
	icons := sess.iconsEnabled()
	dim := lipgloss.NewStyle().Foreground(p.Dim)
	glyphStyle := func(kind string) lipgloss.Style {
		switch kind {
		case "new":
			return lipgloss.NewStyle().Foreground(p.Success)
		case "changed":
			return lipgloss.NewStyle().Foreground(p.Warning)
		case "deleted":
			return lipgloss.NewStyle().Foreground(p.Error)
		}
		return dim
	}
	for _, r := range cmd.BuildSyncTree(changes) {
		if r.Kind == "dir" {
			name := r.Name
			if icons {
				name = folderIcon + " " + name
			}
			sess.printStyled(dim.Render(r.Prefix+name), r.Prefix+r.Name, "dim")
			continue
		}
		nameSeg := sess.styles.Out.Render(r.Name)
		if icons {
			nameSeg = dim.Render(fileIcon(r.Name)) + " " + nameSeg
		}
		rendered := dim.Render(r.Prefix) + glyphStyle(r.Kind).Render(r.Glyph) + " " + nameSeg
		plain := r.Prefix + r.Glyph + " " + r.Name
		sess.printStyled(rendered, plain, "out")
	}
}
