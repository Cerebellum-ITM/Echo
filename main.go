package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/project"
	"github.com/pascualchavez/echo/internal/repl"
	"github.com/pascualchavez/echo/internal/theme"
)

// One-shot (script mode) exit codes, mirroring the unexported constants
// in internal/repl. `echo <cmd>` resolves the project, runs the single
// command, and exits with repl.RunOnce's code; the usage failures below
// (no project / unknown command) are the main-side equivalents.
const (
	exitUsage = 2
)

func main() {
	args := os.Args[1:]

	// Expose the Echo CLI version (with build metadata / dirty marker) to the
	// cmd layer, which can't import internal/repl. Used by the system-status
	// line that connect / i18n-pull emit at start.
	cmd.EchoVersion = repl.FullVersion()

	projectDir, args, err := extractProjectDir(args)
	if err != nil {
		log.Error(err.Error())
		os.Exit(exitUsage)
	}

	// A `-C` value that isn't an existing directory may be a project alias
	// (or a connect target whose remote_path is local). A real directory
	// always wins, so this never changes the meaning of an existing path.
	if projectDir != "" && !isDir(projectDir) {
		if p, _, ok := config.ResolveProjectAlias(projectDir); ok {
			projectDir = p
		} else {
			log.Error("unknown project alias or directory", "value", projectDir,
				"hint", "pass a directory, or register one with `alias <name>`")
			os.Exit(exitUsage)
		}
	}

	// `echo connect …` is a projectless direct mode: it talks to a named
	// remote target from the global config and never needs a local
	// docker-compose.yml, so it runs before the project-root check.
	if len(args) > 0 && args[0] == "connect" {
		if err := cmd.RunDirectConnect(context.Background(), args[1:]); err != nil {
			log.Fatal("connect", "err", err)
		}
		return
	}

	// Any leading argument means a one-shot, non-interactive invocation
	// (`echo <cmd> [args]`). No arguments → the interactive REPL.
	oneShot := len(args) > 0

	cwd := projectDir
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			log.Fatal("could not determine current directory", "err", err)
		}
	}

	root, err := project.FindRoot(cwd)
	if err != nil {
		// Some one-shot commands (e.g. `i18n-pull`) talk only to a remote
		// instance and write into the local repo — they never touch a local
		// docker stack, so they don't need a compose project. Fall back to
		// cwd as the working directory instead of failing.
		switch {
		case oneShot && projectlessOneShot(args[0], args[1:]):
			root = cwd
		case oneShot:
			log.Error("not inside a project", "cwd", cwd,
				"hint", "run echo from a directory containing docker-compose.yml, or pass -C <dir>")
			os.Exit(exitUsage)
		default:
			log.Fatal("not inside a project", "cwd", cwd,
				"hint", "run echo from a directory containing docker-compose.yml")
		}
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

	if oneShot {
		name := args[0]
		// `run` is a one-shot-only orchestrator (not a REPL command, not in
		// Registry): it executes a recipe of commands. Handle it before the
		// generic single-command dispatch.
		if name == "run" {
			code := repl.RunRecipe(styles, palette, cfg.Logo, "01", stage,
				cfg.OdooVersion, cfg.Theme, username, root, cfg, args[1:])
			os.Exit(code)
		}
		if !repl.IsScriptCommand(name) {
			log.Error("unknown command", "cmd", name,
				"hint", "start `echo` and type `help` for the command list")
			os.Exit(exitUsage)
		}
		code := repl.RunOnce(styles, palette, cfg.Logo, "01", stage,
			cfg.OdooVersion, cfg.Theme, username, root, cfg, name, args[1:])
		os.Exit(code)
	}

	repl.Start(styles, palette, cfg.Logo, "01", stage, cfg.OdooVersion, cfg.Theme, username, root, cfg)
}

// extractProjectDir pulls a leading `-C <dir>` / `--project-dir <dir>`
// (or the `=`-joined forms) out of the argument list so a one-shot command
// can run from outside the project directory. It only looks at the first
// token: the flag must come before the command, mirroring `git -C`.
func extractProjectDir(args []string) (dir string, rest []string, err error) {
	if len(args) == 0 {
		return "", args, nil
	}
	switch a := args[0]; {
	case a == "-C" || a == "--project-dir":
		if len(args) < 2 {
			return "", nil, fmt.Errorf("%s requires a directory", a)
		}
		return args[1], args[2:], nil
	case strings.HasPrefix(a, "-C="):
		return strings.TrimPrefix(a, "-C="), args[1:], nil
	case strings.HasPrefix(a, "--project-dir="):
		return strings.TrimPrefix(a, "--project-dir="), args[1:], nil
	}
	return "", args, nil
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// projectlessOneShot reports whether a one-shot command can run outside a
// compose project (using cwd as the working directory). These commands
// reach a remote instance and only read/write local files — they never
// drive a local docker stack, so a missing docker-compose.yml is fine.
// `shell`/`shell-run`/`sequence` qualify only in their remote mode
// (`--from` / `--remote`); locally they need the compose project as always.
func projectlessOneShot(name string, args []string) bool {
	switch name {
	case "i18n-pull", "link", "deploy":
		return true
	case "shell", "shell-run", "up", "stop", "restart", "logs", "sequence":
		return hasRemoteFlag(args)
	}
	return false
}

// hasRemoteFlag reports whether args select the remote mode.
func hasRemoteFlag(args []string) bool {
	for _, a := range args {
		if a == "--remote" || a == "--from" || strings.HasPrefix(a, "--from=") {
			return true
		}
	}
	return false
}
