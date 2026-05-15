package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/theme"
)

const (
	IconOdoo       = "\U000f01a6" // md-cube
	IconContainers = ""           // oct-container
	IconDatabase   = "\U000f01bc" // md-database
)

type InitOpts struct {
	Cfg       *config.Config
	Root      string
	Palette   theme.Palette
	StreamOut func(string)
}

var ErrCancelled = errors.New("cancelled by user")

func RunInit(ctx context.Context, opts InitOpts) (*config.Config, error) {
	cfg := opts.Cfg
	huhTheme := BuildHuhTheme(opts.Palette)

	version := firstNonEmpty(cfg.OdooVersion, config.Defaults.OdooVersion)
	stage := firstNonEmpty(cfg.Stage, config.Defaults.Stage)

	containers, err := docker.ListContainers(ctx, cfg.ComposeCmd, opts.Root)
	if err != nil || len(containers) == 0 {
		var bringUp bool
		confirm := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title(IconContainers + "  No running containers").
				Description("Echo needs the project's containers running\nto list services and databases.\n\nStart them now with `compose up -d`?").
				Affirmative("Yes, start them").
				Negative("Cancel").
				Value(&bringUp),
		)).WithTheme(huhTheme).WithInput(os.Stdin).WithOutput(os.Stdout)

		if err := confirm.Run(); err != nil {
			return nil, err
		}
		if !bringUp {
			return nil, ErrCancelled
		}
		if opts.StreamOut != nil {
			opts.StreamOut("starting containers…")
		}
		if err := docker.Up(ctx, cfg.ComposeCmd, opts.Root, nil, opts.StreamOut); err != nil {
			return nil, fmt.Errorf("compose up failed: %w", err)
		}
		containers, err = docker.ListContainers(ctx, cfg.ComposeCmd, opts.Root)
		if err != nil || len(containers) == 0 {
			return nil, fmt.Errorf("containers still not running after up")
		}
	}

	odooSvc := firstNonEmpty(cfg.OdooContainer, matchService(containers, "odoo"), containers[0].Service)
	dbSvc := firstNonEmpty(cfg.DBContainer, matchService(containers, "db", "postgres"), containers[0].Service)

	envVars := env.Load(opts.Root)
	pgUser := envVars["POSTGRES_USER"]
	pgDB := envVars["POSTGRES_DB"]

	dbs, _ := docker.ListDatabases(ctx, cfg.ComposeCmd, opts.Root, dbSvc, pgUser)
	dbName := firstNonEmpty(cfg.DBName, firstStr(dbs), pgDB, config.Defaults.DBName)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Odoo version").
				Description("Major release of Odoo running in the container").
				Options(opt("17"), opt("18"), opt("19")).
				Value(&version),
			huh.NewSelect[string]().
				Title("Stage").
				Description("Environment marker — colors the prompt").
				Options(opt("dev"), opt("staging"), opt("prod")).
				Value(&stage),
		).
			Title(IconOdoo + "  Odoo").
			Description("Version and environment for this project"),

		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Odoo container").
				Description("compose service running the Odoo process").
				Options(containerOptions(containers)...).
				Value(&odooSvc),
			huh.NewSelect[string]().
				Title("DB container").
				Description("compose service running PostgreSQL").
				Options(containerOptions(containers)...).
				Value(&dbSvc),
		).
			Title(IconContainers + "  Containers").
			Description("Pick which compose service maps to each role"),

		huh.NewGroup(
			dbField(dbs, &dbName),
		).
			Title(IconDatabase + "  Database").
			Description("PostgreSQL database used by Odoo"),
	).
		WithTheme(huh.ThemeCharm()).
		WithInput(os.Stdin).
		WithOutput(os.Stdout).
		WithShowHelp(true)

	if err := form.Run(); err != nil {
		return nil, err
	}

	cfg.OdooVersion = version
	cfg.Stage = stage
	cfg.OdooContainer = odooSvc
	cfg.DBContainer = dbSvc
	cfg.DBName = dbName

	if err := config.SaveProject(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func dbField(dbs []string, val *string) huh.Field {
	if len(dbs) == 0 {
		return huh.NewInput().
			Title("DB name").
			Description("No databases detected — type one manually").
			Value(val)
	}
	return huh.NewSelect[string]().
		Title("DB name").
		Description("Database to target with module/test commands").
		Options(toOptions(dbs)...).
		Value(val)
}

func opt(v string) huh.Option[string] {
	return huh.NewOption(v, v)
}

func toOptions(values []string) []huh.Option[string] {
	out := make([]huh.Option[string], len(values))
	for i, v := range values {
		out[i] = opt(v)
	}
	return out
}

// containerOptions builds select options that show "container_name - service"
// while storing the service name as the value.
func containerOptions(containers []docker.Container) []huh.Option[string] {
	out := make([]huh.Option[string], len(containers))
	for i, c := range containers {
		out[i] = huh.NewOption(c.Label(), c.Service)
	}
	return out
}

// matchService returns the first service whose name contains any of the
// given substrings (case-insensitive). Empty string if none match.
func matchService(containers []docker.Container, substrs ...string) string {
	for _, c := range containers {
		low := strings.ToLower(c.Service)
		for _, sub := range substrs {
			if strings.Contains(low, sub) {
				return c.Service
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstStr(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
