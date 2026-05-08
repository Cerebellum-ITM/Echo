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
)

// Step group titles use Nerd Font glyphs.
const (
	IconOdoo       = "\U000f01a6" // md-cube
	IconContainers = ""           // oct-container
	IconDatabase   = "\U000f01bc" // md-database
)

type InitOpts struct {
	Cfg       *config.Config
	Root      string
	StreamOut func(string)
}

// ErrCancelled is returned when the user declines to start containers.
var ErrCancelled = errors.New("init cancelled")

func RunInit(ctx context.Context, opts InitOpts) (*config.Config, error) {
	cfg := opts.Cfg

	version := firstNonEmpty(cfg.OdooVersion, config.Defaults.OdooVersion)
	stage := firstNonEmpty(cfg.Stage, config.Defaults.Stage)

	services, err := docker.ListContainers(ctx, cfg.ComposeCmd, opts.Root)
	if err != nil || len(services) == 0 {
		var bringUp bool
		confirm := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("No running containers").
				Description("Start them now with `compose up -d`?").
				Affirmative("Yes").
				Negative("No").
				Value(&bringUp),
		)).WithTheme(huh.ThemeBase()).WithInput(os.Stdin).WithOutput(os.Stdout)

		if err := confirm.Run(); err != nil {
			return nil, err
		}
		if !bringUp {
			return nil, ErrCancelled
		}
		if opts.StreamOut != nil {
			opts.StreamOut("starting containers…")
		}
		if err := docker.Up(ctx, cfg.ComposeCmd, opts.Root, opts.StreamOut); err != nil {
			return nil, fmt.Errorf("compose up failed: %w", err)
		}
		services, err = docker.ListContainers(ctx, cfg.ComposeCmd, opts.Root)
		if err != nil || len(services) == 0 {
			return nil, fmt.Errorf("containers still not running after up")
		}
	}

	odooSvc := firstNonEmpty(cfg.OdooContainer, defaultMatch(services, "odoo"), services[0])
	dbSvc := firstNonEmpty(cfg.DBContainer, defaultMatch(services, "db", "postgres"), services[0])

	envVars := env.Load(opts.Root)
	pgUser := envVars["POSTGRES_USER"]
	pgDB := envVars["POSTGRES_DB"]

	dbs, _ := docker.ListDatabases(ctx, cfg.ComposeCmd, opts.Root, dbSvc, pgUser)
	dbName := firstNonEmpty(cfg.DBName, firstStr(dbs), pgDB, config.Defaults.DBName)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Odoo version").
				Options(opt("17"), opt("18"), opt("19")).
				Value(&version),
			huh.NewSelect[string]().Title("Stage").
				Options(opt("dev"), opt("staging"), opt("prod")).
				Value(&stage),
		).Title(IconOdoo + "  Odoo"),

		huh.NewGroup(
			huh.NewSelect[string]().Title("Odoo container").
				Options(toOptions(services)...).
				Value(&odooSvc),
			huh.NewSelect[string]().Title("DB container").
				Options(toOptions(services)...).
				Value(&dbSvc),
		).Title(IconContainers + "  Containers"),

		huh.NewGroup(
			dbField(dbs, &dbName),
		).Title(IconDatabase + "  Database"),
	).WithTheme(huh.ThemeBase()).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)

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
		return huh.NewInput().Title("DB name").Value(val)
	}
	return huh.NewSelect[string]().
		Title("DB name").
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

// defaultMatch returns the first service whose name contains any of the
// given substrings (case-insensitive). Empty string if none match.
func defaultMatch(services []string, substrs ...string) string {
	for _, s := range services {
		low := strings.ToLower(s)
		for _, sub := range substrs {
			if strings.Contains(low, sub) {
				return s
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
