package cmd

import (
	"context"

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
	Palette    theme.Palette
	StreamOut  func(string)
}

// RunShellScript runs `odoo shell -d <db> --no-http < ScriptPath` inside the
// Odoo container (non-interactive, `exec -T`), streaming the output through
// opts.StreamOut. It is the headless counterpart of RunOdooShell.
func RunShellScript(ctx context.Context, opts ShellScriptOpts) error {
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
