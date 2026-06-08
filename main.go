package main

import (
	"context"
	"os"
	"os/user"

	"github.com/charmbracelet/log"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/project"
	"github.com/pascualchavez/echo/internal/repl"
	"github.com/pascualchavez/echo/internal/theme"
)

func main() {
	// `echo connect …` is a projectless direct mode: it talks to a named
	// remote target from the global config and never needs a local
	// docker-compose.yml, so it runs before the project-root check.
	if len(os.Args) > 1 && os.Args[1] == "connect" {
		if err := cmd.RunDirectConnect(context.Background(), os.Args[2:]); err != nil {
			log.Fatal("connect", "err", err)
		}
		return
	}

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

	// Backfill project_path into a pre-existing profile so this project
	// becomes discoverable as a remote connect target from a laptop.
	if _, err := config.BackfillProjectPath(cfg); err != nil {
		log.Warn("could not backfill project_path", "err", err)
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
