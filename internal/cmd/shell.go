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
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

type ShellOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// LineTransform restyles complete output lines of an interactive
	// session (used by `shell` to colorize Odoo's startup logs Echo-style).
	// nil keeps the raw byte-for-byte passthrough.
	LineTransform docker.LineTransform
}

// RunBash opens an interactive bash session inside the Odoo container.
// Returns the captured output, an `interrupted` flag (true when the user
// sent SIGINT during the session), and the run error.
func RunBash(ctx context.Context, opts ShellOpts) (string, bool, error) {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return "", false, err
	}
	if err := maybeConfirmProd(opts, "bash"); err != nil {
		return "", false, err
	}
	return docker.ExecInteractive(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, []string{"bash"}, nil)
}

// RunPsql opens an interactive psql session against the configured DB.
func RunPsql(ctx context.Context, opts ShellOpts) (string, bool, error) {
	if opts.Cfg.DBContainer == "" {
		return "", false, ErrNoDBContainer
	}
	if opts.Cfg.DBName == "" {
		return "", false, ErrNoDB
	}
	if err := maybeConfirmProd(opts, "psql"); err != nil {
		return "", false, err
	}
	envVars := env.Load(opts.Root)
	user := envVars["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	argv := []string{"psql", "-U", user, opts.Cfg.DBName}
	return docker.ExecInteractive(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, argv, nil)
}

// RunOdooShell opens the Odoo Python shell loaded against the
// configured DB. With `--from <target>` / `--remote` in Args, the shell
// opens on the REMOTE instance over `ssh -tt`, through the same PTY
// capture/colorize path as the local one.
func RunOdooShell(ctx context.Context, opts ShellOpts) (string, bool, error) {
	if from, remote := remoteFlagsIn(opts.Args); from != "" || remote {
		return runOdooShellRemote(ctx, opts, from)
	}
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return "", false, err
	}
	if err := maybeConfirmProd(opts, "shell"); err != nil {
		return "", false, err
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
	return docker.ExecInteractive(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, odoo.Shell(conn), opts.LineTransform)
}

// runOdooShellRemote opens the Odoo shell inside the REMOTE instance's
// container: `ssh -tt <host> 'cd <path> && <compose> exec <odoo> odoo
// shell …'`, run through docker.RunInteractive so the session gets the
// same PTY passthrough, capture, and startup-log colorizing as a local
// shell. An interactive shell needs a remote TTY, so a non-TTY caller
// fails closed before dialing.
func runOdooShellRemote(ctx context.Context, opts ShellOpts, from string) (string, bool, error) {
	if err := requireTTY("the remote shell is interactive"); err != nil {
		return "", false, err
	}
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)
	if err != nil {
		return "", false, err
	}
	if err := confirmRemoteProd(opts.Palette, "shell", rsc, opts.Args); err != nil {
		return "", false, err
	}
	remoteCmd := remoteExecInteractive(rsc.remotePath, rsc.target.composeCmd,
		rsc.target.odooContainer, odoo.Shell(rsc.conn))
	return docker.RunInteractive(ctx,
		[]string{"ssh", "-tt", "-o", "BatchMode=yes", rsc.sshHost, remoteCmd},
		"", opts.LineTransform)
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
	if err := requireTTY("pass --force to proceed against prod"); err != nil {
		return err
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Opening " + action + " against prod database " + red).
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
