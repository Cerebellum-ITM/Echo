package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/docker"
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
		case a == "--from", a == "-E", a == "--env":
			i++ // skip the target name
		case strings.HasPrefix(a, "--from="), strings.HasPrefix(a, "-E="),
			strings.HasPrefix(a, "--env="):
		case a == "--remote", a == "--force":
		default:
			out = append(out, a)
		}
	}
	return out
}

// runRemoteUp starts compose services on a remote host over SSH (`up -d`).
// Starting is non-destructive, so there is no prod gate. With no service it
// brings up the whole remote stack, matching the local default. Output
// streams live through opts.StreamOut.
func runRemoteUp(ctx context.Context, opts DockerOpts, from string) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, opts.Log)
	if err != nil {
		return err
	}
	if rsc.reverb != nil {
		return runReverbEnvAction(ctx, opts, rsc, "up")
	}
	args := append([]string{"up", "-d"}, remoteServiceArgs(opts.Args)...)
	remoteCmd := remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, args...)
	return runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut)
}

// runRemoteStop stops compose services on a remote host over SSH. A `prod`
// remote stage gates on confirmRemoteProd (`--force` bypass) since stopping a
// production stack is disruptive. With no service it stops the whole remote
// stack. Output streams live through opts.StreamOut.
func runRemoteStop(ctx context.Context, opts DockerOpts, from string) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, opts.Log)
	if err != nil {
		return err
	}
	if rsc.reverb != nil {
		return runReverbEnvAction(ctx, opts, rsc, "stop")
	}
	if err := confirmRemoteProd(opts.Palette, "stop", rsc, opts.Args); err != nil {
		return err
	}
	args := append([]string{"stop"}, remoteServiceArgs(opts.Args)...)
	remoteCmd := remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, args...)
	return runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut)
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
	if rsc.reverb != nil {
		return runReverbEnvAction(ctx, opts, rsc, "restart")
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

// runRemotePS lists a remote host's compose containers over SSH. Like
// `logs`, it works the same on classic and Reverb targets: Reverb owns the
// lifecycle verbs, but a read of what is running is still a compose call on
// the host.
func runRemotePS(ctx context.Context, opts DockerOpts, from string, onTable func(rows []docker.PSContainer, db string)) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, opts.Log)
	if err != nil {
		return err
	}
	return remotePSTable(ctx, rsc.sshHost, rsc.remotePath, rsc.target.composeCmd,
		func(rows []docker.PSContainer) { onTable(rows, rsc.prof.DBName) }, opts.StreamOut)
}

// remotePSTable reads `<compose> ps --format json` on host over SSH and hands
// the parsed rows to onTable. When the structured read fails (older compose,
// SSH hiccup) it streams the raw `<compose> ps` through stream instead, so the
// caller always gets something on screen.
func remotePSTable(ctx context.Context, host, remotePath, composeCmd string, onTable func([]docker.PSContainer), stream func(string)) error {
	jsonCmd := remoteComposeCmd(remotePath, composeCmd, "ps", "--format", "json")
	if out, err := runSSH(ctx, host, jsonCmd, nil); err == nil {
		if rows, err := docker.ParsePS(out); err == nil {
			onTable(rows)
			return nil
		}
	}
	return runSSHStream(ctx, host, remoteComposeCmd(remotePath, composeCmd, "ps"), nil, stream)
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
