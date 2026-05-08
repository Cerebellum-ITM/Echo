package main

import (
	"fmt"
	"os"
	"os/user"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/repl"
	"github.com/pascualchavez/echo/internal/theme"
)

func main() {
	cwd, _ := os.Getwd()

	cfg, err := config.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load config: %v\n", err)
		defaults := config.Defaults
		cfg = &defaults
	}

	palette := theme.PaletteByName(cfg.Theme)
	stage := theme.StageFromString(cfg.Stage)
	styles := theme.New(palette, stage)

	u, _ := user.Current()
	username := u.Name
	if username == "" {
		username = u.Username
	}

	repl.Start(styles, palette, cfg.Logo, "01", stage, cfg.OdooVersion, cfg.Theme, username, cwd, cfg)
}
