package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// ShellScriptOpts carries what RunShellScript needs to pipe a local .py
// file through the Odoo shell inside the container.
type ShellScriptOpts struct {
	Cfg        *config.Config
	Root       string
	ScriptPath string // host path of the .py to feed to the shell's stdin
	Args       []string
	// From / Remote select the remote mode: a named connect target, or
	// the directory's [connect] binding. Both empty/false → local run.
	From    string
	Remote  bool
	Palette theme.Palette
	// Log emits Odoo-style progress lines for the remote path (target
	// resolved, system status). Nil is a no-op.
	Log       func(level, sub, msg, db string, fields ...[2]string)
	StreamOut func(string)
}

// RunShellScript runs `odoo shell -d <db> --no-http < ScriptPath` inside the
// Odoo container (non-interactive, `exec -T`), streaming the output through
// opts.StreamOut. It is the headless counterpart of RunOdooShell. With
// From/Remote set, the shell runs in the REMOTE instance's container over
// SSH and the local script is piped through ssh's stdin.
func RunShellScript(ctx context.Context, opts ShellScriptOpts) error {
	if opts.From != "" || opts.Remote {
		return runShellScriptRemote(ctx, opts)
	}
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	if err := maybeConfirmProd(ShellOpts{
		Cfg:     opts.Cfg,
		Args:    opts.Args,
		Palette: opts.Palette,
	}, "shell-run"); err != nil {
		return err
	}
	envVars := env.Load(opts.Root)
	conn := odoo.Conn{
		DB:       opts.Cfg.DBName,
		Host:     opts.Cfg.DBContainer,
		Port:     envVars["POSTGRES_PORT"],
		User:     envVars["POSTGRES_USER"],
		Password: envVars["POSTGRES_PASSWORD"],
	}
	if conn.Port == "" {
		conn.Port = "5432"
	}
	return docker.ExecWithStdin(ctx, opts.Cfg.ComposeCmd, opts.Root,
		opts.Cfg.OdooContainer, odoo.Shell(conn), opts.ScriptPath, opts.StreamOut)
}

// runShellScriptRemote pipes the local script through the REMOTE Odoo
// shell: `ssh <host> 'cd <path> && <compose> exec -T <odoo> odoo shell …'`
// with the script bytes on ssh's stdin — the remote analog of
// docker.ExecWithStdin, streamed live through runSSHStream.
func runShellScriptRemote(ctx context.Context, opts ShellScriptOpts) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, opts.From, opts.Log)
	if err != nil {
		return err
	}
	if err := confirmRemoteProd(opts.Palette, "shell-run", rsc, opts.Args); err != nil {
		return err
	}
	script, err := os.ReadFile(opts.ScriptPath)
	if err != nil {
		return fmt.Errorf("read script: %w", err)
	}
	remoteCmd := remoteContainerCmd(rsc.remotePath, rsc.target, odoo.Shell(rsc.conn))
	return runSSHStream(ctx, rsc.sshHost, remoteCmd, script, opts.StreamOut)
}
