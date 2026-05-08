package main

import (
	"context"
	"os"
	"os/user"

	"github.com/charmbracelet/log"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/project"
	"github.com/pascualchavez/echo/internal/repl"
	"github.com/pascualchavez/echo/internal/theme"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal("could not determine current directory", "err", err)
	}

	root, err := project.FindRoot(cwd)
	if err != nil {
		log.Fatal("not inside a project", "cwd", cwd, "hint", "run echo from a directory containing docker-compose.yml")
	}

	cfg, err := config.Load(root)
	if err != nil {
		log.Warn("could not load config, using defaults", "err", err)
		defaults := config.Defaults
		cfg = &defaults
	}

	if cfg.ComposeCmd == "" {
		composeCmd, err := docker.DetectCompose(context.Background())
		if err != nil {
			log.Fatal("compose not available", "err", err)
		}
		cfg.ComposeCmd = composeCmd
		if err := config.SaveGlobal(cfg); err != nil {
			log.Warn("could not persist compose command", "err", err)
		}
	}

	palette := theme.PaletteByName(cfg.Theme)
	stage := theme.StageFromString(cfg.Stage)
	styles := theme.New(palette, stage)

	u, _ := user.Current()
	username := u.Name
	if username == "" {
		username = u.Username
	}

	repl.Start(styles, palette, cfg.Logo, "01", stage, cfg.OdooVersion, cfg.Theme, username, root, cfg)
}
