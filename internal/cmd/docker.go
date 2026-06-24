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
	// Log emits one Odoo-style progress line (rendered by the REPL under
	// `echo.<cmd>[.sub]`), used by the remote `restart`/`logs` branches to
	// surface `target resolved` / `system` lines. nil-safe: the local path
	// leaves it unset and stays silent.
	Log func(level, sub, msg, db string, fields ...[2]string)
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

// RunRestart restarts compose services. With `--from <target>` / `--remote`
// it restarts on a remote host over SSH (reusing the deploy/shell
// transport); otherwise it restarts the local stack. A remote run with no
// service argument targets the remote profile's Odoo container and gates
// on the remote stage when it is `prod` (`--force` bypass).
func RunRestart(ctx context.Context, opts DockerOpts) error {
	if from, remote := remoteFlagsIn(opts.Args); from != "" || remote {
		return runRemoteRestart(ctx, opts, from)
	}
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
	follow, copyMode, all, tail, services := parseLogsArgs(opts.Args)

	if from, remote := remoteFlagsIn(opts.Args); from != "" || remote {
		return runRemoteLogs(ctx, opts, from, follow, copyMode, all, tail, services)
	}

	if !all && len(services) == 0 && opts.Cfg.OdooContainer != "" {
		services = []string{opts.Cfg.OdooContainer}
	}

	if copyMode {
		return runLogsAndCopy(ctx, opts, tail, services)
	}
	if follow {
		return docker.LogsFollow(ctx, opts.Cfg.ComposeCmd, opts.Root, tail, services, opts.StreamOut)
	}
	return docker.Logs(ctx, opts.Cfg.ComposeCmd, opts.Root, tail, services, opts.StreamOut)
}

// parseLogsArgs extracts the `logs` flags shared by the local and remote
// paths. follow defaults to true; `--no-follow` and `-c/--copy` clear it
// (copy forces bounded output). The remote-mode switches (`--from <v>` /
// `--from=v` / `--remote`) are consumed so they never land in services.
func parseLogsArgs(args []string) (follow, copyMode, all bool, tail string, services []string) {
	follow = true
	tail = "100" // default; -t/--tail overrides
	services = make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" || a == "--follow":
			follow = true
		case a == "--no-follow":
			follow = false
		case a == "-c" || a == "--copy":
			copyMode = true
			follow = false
		case a == "--all":
			all = true
		case a == "-t" || a == "--tail":
			if i+1 < len(args) {
				tail = args[i+1]
				i++
			}
		case a == "--remote":
			// remote-mode switch, not a service
		case a == "--from":
			i++ // skip the target name
		case strings.HasPrefix(a, "--from="):
			// remote-mode switch, not a service
		default:
			services = append(services, a)
		}
	}
	return follow, copyMode, all, tail, services
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
