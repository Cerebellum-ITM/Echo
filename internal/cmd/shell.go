package cmd

import (
	"context"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/theme"
)

type ShellOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
}

// RunBash opens an interactive bash session inside the Odoo container.
func RunBash(ctx context.Context, opts ShellOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	if err := maybeConfirmProd(opts, "bash"); err != nil {
		return err
	}
	return docker.ExecInteractive(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, []string{"bash"})
}

// RunPsql opens an interactive psql session against the configured DB.
func RunPsql(ctx context.Context, opts ShellOpts) error {
	if opts.Cfg.DBContainer == "" {
		return ErrNoDBContainer
	}
	if opts.Cfg.DBName == "" {
		return ErrNoDB
	}
	if err := maybeConfirmProd(opts, "psql"); err != nil {
		return err
	}
	envVars := env.Load(opts.Root)
	user := envVars["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	argv := []string{"psql", "-U", user, opts.Cfg.DBName}
	return docker.ExecInteractive(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, argv)
}

// RunOdooShell opens the Odoo Python shell loaded against the configured DB.
func RunOdooShell(ctx context.Context, opts ShellOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	if err := maybeConfirmProd(opts, "shell"); err != nil {
		return err
	}
	envVars := env.Load(opts.Root)
	argv := []string{
		"odoo", "shell",
		"-d", opts.Cfg.DBName,
		"--db_host=" + opts.Cfg.DBContainer,
		"--db_port=" + envVars["POSTGRES_PORT"],
		"--db_user=" + envVars["POSTGRES_USER"],
		"--db_password=" + envVars["POSTGRES_PASSWORD"],
		"--no-http",
	}
	return docker.ExecInteractive(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, argv)
}

// maybeConfirmProd shows a red huh.Confirm when stage=prod, unless
// --force is in Args. Returns ErrCancelled on No / Esc.
func maybeConfirmProd(opts ShellOpts, action string) error {
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

func confirmProd(palette theme.Palette, action, db string) error {
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Opening "+action+" against prod database "+red).
			Description("This will run against production data.").
			Affirmative("Open").
			Negative("Cancel").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}
