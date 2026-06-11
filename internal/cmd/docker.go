package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/theme"
)

type DockerOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	StreamOut func(string)
}

func RunUp(ctx context.Context, opts DockerOpts) error {
	return docker.Up(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Args, opts.StreamOut)
}

func RunDown(ctx context.Context, opts DockerOpts) error {
	if err := maybeConfirmDockerProd(opts, "down"); err != nil {
		return err
	}
	services := stripFlag(opts.Args, "--force")
	return docker.Down(ctx, opts.Cfg.ComposeCmd, opts.Root, services, opts.StreamOut)
}

// stripFlag removes every occurrence of flag from args, returning a new
// slice with the remaining arguments. Used to filter REPL-only flags
// (e.g. `--force`) before forwarding the rest to docker-compose.
func stripFlag(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == flag {
			continue
		}
		out = append(out, a)
	}
	return out
}

// maybeConfirmDockerProd guards destructive compose actions (currently
// `down`, which removes containers and networks) with a red huh.Confirm
// when stage=prod. `--force` in Args skips the prompt. Returns
// ErrCancelled when the user declines.
func maybeConfirmDockerProd(opts DockerOpts, action string) error {
	if !strings.EqualFold(opts.Cfg.Stage, "prod") {
		return nil
	}
	for _, a := range opts.Args {
		if a == "--force" {
			return nil
		}
	}
	return confirmProd(opts.Palette, action, opts.Cfg.DBName)
}

func RunRestart(ctx context.Context, opts DockerOpts) error {
	return docker.Restart(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Args, opts.StreamOut)
}

func RunStop(ctx context.Context, opts DockerOpts) error {
	return docker.Stop(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Args, opts.StreamOut)
}

// PSList returns the compose services' containers as structured rows for
// Echo's styled `ps` table. The REPL renders it; on any error the caller
// falls back to RunPS (raw streaming) so `ps` never regresses.
func PSList(ctx context.Context, opts DockerOpts) ([]docker.PSContainer, error) {
	return docker.PSList(ctx, opts.Cfg.ComposeCmd, opts.Root)
}

// RunPS streams the raw `<compose> ps` table. Kept as the fallback for the
// styled table when `--format json` can't be parsed.
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
