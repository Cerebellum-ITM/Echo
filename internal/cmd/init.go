package cmd

import (
	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/detect"
)

// RunInit opens the huh form pre-filled with existing/detected values,
// saves config on confirm, and returns the updated Config.
// Returns an error (including huh.ErrUserAborted) if the user cancels.
func RunInit(cfg *config.Config, projectDir string) (*config.Config, error) {
	detected := detect.FromCompose(projectDir)

	version := firstNonEmpty(cfg.OdooVersion, detected.OdooVersion, config.Defaults.OdooVersion)
	odooSvc := firstNonEmpty(cfg.OdooContainer, detected.OdooContainer, config.Defaults.OdooContainer)
	dbSvc := firstNonEmpty(cfg.DBContainer, detected.DBContainer, config.Defaults.DBContainer)
	dbName := firstNonEmpty(cfg.DBName, detected.DBName, config.Defaults.DBName)
	stage := firstNonEmpty(cfg.Stage, config.Defaults.Stage)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Odoo version").
				Options(
					huh.NewOption("17", "17"),
					huh.NewOption("18", "18"),
					huh.NewOption("19", "19"),
				).
				Value(&version),

			huh.NewInput().
				Title("Odoo container").
				Value(&odooSvc),

			huh.NewInput().
				Title("DB container").
				Value(&dbSvc),

			huh.NewInput().
				Title("DB name").
				Value(&dbName),

			huh.NewSelect[string]().
				Title("Stage").
				Options(
					huh.NewOption("dev", "dev"),
					huh.NewOption("staging", "staging"),
					huh.NewOption("prod", "prod"),
				).
				Value(&stage),
		),
	).WithTheme(huh.ThemeBase())

	if err := form.Run(); err != nil {
		return nil, err
	}

	cfg.OdooVersion = version
	cfg.OdooContainer = odooSvc
	cfg.DBContainer = dbSvc
	cfg.DBName = dbName
	cfg.Stage = stage

	if err := config.SaveProject(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
