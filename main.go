package main

import (
	"context"
	"errors"
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

	// Global flags that never need a project, handled before any project
	// detection: `--version`/`-v` prints the CLI version and exits;
	// `--help`/`-h` are normalized to the `help` command (which runs
	// projectless). These must work from anywhere, including outside a
	// compose project.
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-v":
			fmt.Println("echo " + repl.FullVersion())
			return
		case "--help", "-h":
			args[0] = "help"
		}
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
			exitOnConfigError(err)
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
		// the repository root instead of failing, so that working in a
		// subfolder keeps the same project config.
		switch {
		case oneShot && projectlessOneShot(args[0], args[1:]):
			root = repoRoot(cwd)
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
		exitOnConfigError(err)
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

// exitOnConfigError stops Echo with exit 2 when err is a config file that
// does not parse: running on defaults would let the next save overwrite it.
func exitOnConfigError(err error) {
	var perr *config.ParseError
	if errors.As(err, &perr) {
		repl.PrintConfigError(perr)
		os.Exit(exitUsage)
	}
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
// `help` is purely informational and needs nothing; `lint` only reads local
// XML files and shells out to xmllint, so it must work from anywhere — an
// editor or git hook runs it wherever the file was saved, and requiring a
// compose project would disable exactly the automatic check the command
// exists for; `logview`/`report`
// only read the local command-history / run-report store keyed by cwd
// (browsing history must not require a live project — the deploy that wrote
// it already ran projectless); `db-pull` dumps a remote DB over SSH into
// the local repo's ./backups/ (download-only by default) — a local Docker
// stack is only needed with `--restore`, and that step self-guards via
// requireDBContainer; the remote-mode group
// (`shell`/`shell-run`/`update`/`sequence`/`db-admin`/…) qualifies only with
// `--from`/`--remote` — locally they need the compose project as always.
// repoRoot returns the git top level for cwd, falling back to cwd itself
// outside a repository. Per-project state (the `link` binding, addons
// paths, deploy history) is keyed by the root's path, so resolving to the
// repository is what keeps a `cd` into a subfolder from looking like a
// different, unconfigured project. State left behind by a binding made in
// a subfolder is moved once, and only into an empty destination.
func repoRoot(cwd string) string {
	git := project.GitRoot(cwd)
	if git == "" || project.SameDir(git, cwd) {
		return cwd
	}
	if from := config.FindProjectState(cwd, git); from != "" && !project.SameDir(from, git) {
		moved, err := config.MigrateProjectKey(from, git)
		switch {
		case err != nil:
			log.Warn("could not migrate project config", "from", from, "to", git, "err", err)
		case moved:
			log.Info("project config migrated", "from", from, "to", git)
		}
	}
	return git
}

func projectlessOneShot(name string, args []string) bool {
	switch name {
	case "help", "lint", "i18n-pull", "link", "doctor", "deploy", "push", "watch", "checkpoint", "actions", "promote", "logview", "report", "db-pull", "modules":
		return true
	case "shell", "shell-run", "up", "down", "stop", "restart", "ps", "logs", "sequence", "update", "test", "view", "compare", "db-admin":
		return hasRemoteFlag(args)
	}
	return false
}

// hasRemoteFlag reports whether args select the remote mode — a named or
// linked connect target (`--from`/`--remote`) or a Reverb environment
// (`-E`/`--env`, Unit 107). All of them reach an instance over SSH, so the
// command needs no local compose project.
func hasRemoteFlag(args []string) bool {
	for _, a := range args {
		switch {
		case a == "--remote", a == "--from", strings.HasPrefix(a, "--from="):
			return true
		case a == "-E", a == "--env",
			strings.HasPrefix(a, "-E="), strings.HasPrefix(a, "--env="):
			return true
		}
	}
	return false
}
