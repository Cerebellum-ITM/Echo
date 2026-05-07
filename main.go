package main

import (
	"os"
	"os/user"

	"github.com/pascualchavez/echo/internal/repl"
	"github.com/pascualchavez/echo/internal/theme"
)

func main() {
	palette := theme.Charm
	stage := theme.StageDev
	styles := theme.New(palette, stage)

	u, _ := user.Current()
	username := u.Name
	if username == "" {
		username = u.Username
	}

	cwd, _ := os.Getwd()

	repl.Start(styles, palette, "echo", "01", stage, "17", username, cwd)
}
