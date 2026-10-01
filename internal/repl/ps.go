package repl

import (
	"context"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/docker"
)

// runPSTable renders `ps` as an Echo-styled table (service / image / status /
// ports) instead of docker's raw output; cmd.RunPS falls back to streaming
// the raw `docker compose ps` when the structured read fails, locally or on
// the remote named by `--from` / `--remote`.
func (sess *session) runPSTable(ctx context.Context, opts cmd.DockerOpts) {
	rendered := false
	err := cmd.RunPS(ctx, opts, func(rows []docker.PSContainer, db string) {
		rendered = true
		sess.emitPSTableAs(rows, "echo.ps", db)
	})
	if !rendered {
		sess.readonlyFinalize("ps", err)
	}
}

// emitPSTableAs prints the aligned, theme-styled container table and closes
// with an Odoo-style count line under logger/db, mirroring `modstate`, so
// `link --show` reuses it for a remote `ps` under its own namespace. The
// status cell is colored by container state/health; ports are dimmed.
func (sess *session) emitPSTableAs(rows []docker.PSContainer, logger, db string) {
	if len(rows) == 0 {
		emitOdooLog("INFO", logger, "no containers running",
			nil, sess.styles, sess.palette, db)
		sess.exitCode = exitOK
		return
	}

	const (
		svcHdr = "service"
		imgHdr = "image"
		stHdr  = "status"
	)
	svcW, imgW, stW := len(svcHdr), len(imgHdr), len(stHdr)
	for _, r := range rows {
		if len(r.Service) > svcW {
			svcW = len(r.Service)
		}
		if len(r.Image) > imgW {
			imgW = len(r.Image)
		}
		if len(r.Status) > stW {
			stW = len(r.Status)
		}
	}

	accent := lipgloss.NewStyle().Bold(true).Foreground(sess.palette.Accent)
	header := accent.Render(pad(svcHdr, svcW)) + "  " +
		accent.Render(pad(imgHdr, imgW)) + "  " +
		accent.Render(pad(stHdr, stW)) + "  " + accent.Render("ports")
	sess.print(Line{Kind: "table", Text: header})

	for _, r := range rows {
		ports := r.Ports()
		if ports == "" {
			ports = "-"
		}
		line := sess.styles.Out.Render(pad(r.Service, svcW)) + "  " +
			sess.styles.Out.Render(pad(r.Image, imgW)) + "  " +
			sess.psStatusStyle(r.State, r.Health).Render(pad(r.Status, stW)) + "  " +
			sess.styles.Dim.Render(ports)
		sess.print(Line{Kind: "table", Text: line})
	}

	emitOdooLog("INFO", logger, "containers listed",
		[]logField{{"count", strconv.Itoa(len(rows))}},
		sess.styles, sess.palette, db)
	sess.exitCode = exitOK
}

// psStatusStyle colors a container's status cell. Health wins when present
// (healthy→ok, unhealthy→err, starting→warn); otherwise the lifecycle state
// decides: running→ok, restarting→warn, exited/dead→err, paused/created→dim.
func (sess *session) psStatusStyle(state, health string) lipgloss.Style {
	switch health {
	case "healthy":
		return sess.styles.Ok
	case "unhealthy":
		return sess.styles.Err
	case "starting":
		return sess.styles.Warn
	}
	switch state {
	case "running":
		return sess.styles.Ok
	case "restarting":
		return sess.styles.Warn
	case "exited", "dead", "removing":
		return sess.styles.Err
	case "paused", "created":
		return sess.styles.Dim
	default:
		return sess.styles.Out
	}
}
