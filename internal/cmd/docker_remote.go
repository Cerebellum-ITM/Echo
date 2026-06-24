package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/clipboard"
)

// remoteServiceArgs returns the positional compose-service arguments from a
// command's args, dropping the remote-mode switches (`--from <v>` / `--from=v`
// / `--remote`) and the prod-confirm bypass (`--force`). What remains is the
// list of services to act on; empty means "use the default container".
func remoteServiceArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			i++ // skip the target name
		case strings.HasPrefix(a, "--from="):
		case a == "--remote", a == "--force":
		default:
			out = append(out, a)
		}
	}
	return out
}

// runRemoteRestart restarts compose services on a remote host over SSH. With
// no service it targets the remote profile's Odoo container, symmetric with
// the local `logs` default. A `prod` remote stage gates on confirmRemoteProd
// (`--force` bypass). Output streams live through opts.StreamOut.
func runRemoteRestart(ctx context.Context, opts DockerOpts, from string) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, opts.Log)
	if err != nil {
		return err
	}
	if err := confirmRemoteProd(opts.Palette, "restart", rsc, opts.Args); err != nil {
		return err
	}
	services := remoteServiceArgs(opts.Args)
	if len(services) == 0 && rsc.target.odooContainer != "" {
		services = []string{rsc.target.odooContainer}
	}
	remoteCmd := remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd,
		append([]string{"restart"}, services...)...)
	return runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut)
}

// runRemoteLogs streams a remote host's compose logs over SSH. Follow is the
// default (a long-lived `compose logs -f` over the SSH stream, ended by
// Ctrl+C / connection close); `--no-follow` and `--copy` bound the output,
// with `--copy` landing the captured text on the local clipboard. With no
// service and without `--all` it defaults to the remote profile's Odoo
// container, mirroring the local default. Read-only: no prod gate.
func runRemoteLogs(ctx context.Context, opts DockerOpts, from string, follow, copyMode, all bool, tail string, services []string) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, opts.Log)
	if err != nil {
		return err
	}
	if !all && len(services) == 0 && rsc.target.odooContainer != "" {
		services = []string{rsc.target.odooContainer}
	}

	args := []string{"logs", "--no-log-prefix"}
	if follow {
		args = append(args, "-f")
	}
	if tail != "" {
		args = append(args, "--tail", tail)
	}
	args = append(args, services...)
	remoteCmd := remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, args...)

	if copyMode {
		return runRemoteLogsAndCopy(ctx, opts, rsc.sshHost, remoteCmd)
	}
	return runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut)
}

// runRemoteLogsAndCopy buffers a bounded remote log run, prints each line via
// the stream callback, and copies the full captured text to the local
// clipboard — the remote analog of runLogsAndCopy.
func runRemoteLogsAndCopy(ctx context.Context, opts DockerOpts, host, remoteCmd string) error {
	var buf bytes.Buffer
	stream := func(line string) {
		buf.WriteString(line)
		buf.WriteByte('\n')
		if opts.StreamOut != nil {
			opts.StreamOut(line)
		}
	}
	if err := runSSHStream(ctx, host, remoteCmd, nil, stream); err != nil {
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
