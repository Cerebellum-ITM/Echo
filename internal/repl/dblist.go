package repl

import (
	"context"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/docker"
)

// runDBListTable renders `db-list` as an Echo-styled table (name / size /
// created) instead of plain printf lines, marking the active database.
func (sess *session) runDBListTable(ctx context.Context, opts cmd.DBOpts) {
	infos, err := cmd.DBList(ctx, opts)
	if err != nil {
		sess.readonlyFinalize("db-list", err)
		return
	}
	sess.emitDBListTable(infos, sess.cfg.DBName)
}

// emitDBListTable prints the aligned, theme-styled database table and closes
// with an Odoo-style count line, mirroring `modstate`/`ps`. The active
// database (cfg.DBName) gets a green ● and its name in the ok style.
func (sess *session) emitDBListTable(infos []docker.DatabaseInfo, active string) {
	if len(infos) == 0 {
		emitOdooLog("INFO", "echo.db-list", "no databases",
			nil, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}

	const (
		nameHdr = "name"
		sizeHdr = "size"
	)
	nameW, sizeW := len(nameHdr), len(sizeHdr)
	for _, i := range infos {
		if len(i.Name) > nameW {
			nameW = len(i.Name)
		}
		if len(i.SizeHuman) > sizeW {
			sizeW = len(i.SizeHuman)
		}
	}

	accent := lipgloss.NewStyle().Bold(true).Foreground(sess.palette.Accent)
	header := "  " + accent.Render(pad(nameHdr, nameW)) + "  " +
		accent.Render(pad(sizeHdr, sizeW)) + "  " + accent.Render("created")
	sess.print(Line{Kind: "table", Text: header})

	bullet := lipgloss.NewStyle().Foreground(sess.palette.Success).Render("●")
	for _, i := range infos {
		mark := "  "
		nameStyle := sess.styles.Out
		if i.Name == active {
			mark = bullet + " "
			nameStyle = sess.styles.Ok
		}
		line := mark + nameStyle.Render(pad(i.Name, nameW)) + "  " +
			sess.styles.Dim.Render(pad(i.SizeHuman, sizeW)) + "  " +
			sess.styles.Dim.Render(i.CreatedAt)
		sess.print(Line{Kind: "table", Text: line})
	}

	emitOdooLog("INFO", "echo.db-list", "databases listed",
		[]logField{{"count", strconv.Itoa(len(infos))}},
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}
