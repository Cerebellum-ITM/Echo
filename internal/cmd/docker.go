package cmd

import (
	"bytes"
	"context"
	"fmt"

	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
)

type DockerOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	StreamOut func(string)
}

func RunUp(ctx context.Context, opts DockerOpts) error {
	return docker.Up(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Args, opts.StreamOut)
}

func RunDown(ctx context.Context, opts DockerOpts) error {
	return docker.Down(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Args, opts.StreamOut)
}

func RunRestart(ctx context.Context, opts DockerOpts) error {
	return docker.Restart(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Args, opts.StreamOut)
}

func RunPS(ctx context.Context, opts DockerOpts) error {
	return docker.PS(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.StreamOut)
}

// RunLogs follows logs by default. With no service argument, targets the
// Odoo container from config. Flags:
//
//	-t, --tail N      show last N lines on start (passes --tail to compose)
//	--no-follow       disable follow mode (bounded output)
//	-c, --copy        bounded + copy output to clipboard
//	--all             include every compose service (overrides default)
func RunLogs(ctx context.Context, opts DockerOpts) error {
	follow := true
	copyMode := false
	all := false
	tail := "100" // default; -t/--tail overrides
	services := make([]string, 0, len(opts.Args))

	for i := 0; i < len(opts.Args); i++ {
		a := opts.Args[i]
		switch a {
		case "-f", "--follow":
			follow = true
		case "--no-follow":
			follow = false
		case "-c", "--copy":
			copyMode = true
			follow = false
		case "--all":
			all = true
		case "-t", "--tail":
			if i+1 < len(opts.Args) {
				tail = opts.Args[i+1]
				i++
			}
		default:
			services = append(services, a)
		}
	}

	if !all && len(services) == 0 && opts.Cfg.OdooContainer != "" {
		services = []string{opts.Cfg.OdooContainer}
	}

	if copyMode {
		return runLogsAndCopy(ctx, opts, tail, services)
	}
	if follow {
		return docker.LogsFollow(ctx, opts.Cfg.ComposeCmd, opts.Root, tail, services)
	}
	return docker.Logs(ctx, opts.Cfg.ComposeCmd, opts.Root, tail, services, opts.StreamOut)
}

// runLogsAndCopy captures bounded log output, prints each line via the
// stream callback, and copies the full captured text to the system clipboard.
func runLogsAndCopy(ctx context.Context, opts DockerOpts, tail string, services []string) error {
	var buf bytes.Buffer
	stream := func(line string) {
		buf.WriteString(line)
		buf.WriteByte('\n')
		if opts.StreamOut != nil {
			opts.StreamOut(line)
		}
	}
	if err := docker.Logs(ctx, opts.Cfg.ComposeCmd, opts.Root, tail, services, stream); err != nil {
		return err
	}
	if err := clipboard.WriteAll(buf.String()); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}
	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("✓ copied %d lines to clipboard", lineCount(buf.String())))
	}
	return nil
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}
