package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// RunRecipe reads a recipe — one Echo command per line — and runs each
// step through the one-shot dispatch, stopping at the first step that
// exits non-zero (fail-fast) unless --continue-on-error is passed. The
// recipe path may be `-` or omitted to read from stdin. Returns the
// process exit code: the failing step's code under fail-fast, exitError
// if any step failed under --continue-on-error, else exitOK.
func RunRecipe(s theme.Styles, p theme.Palette, project, id string, stage theme.Stage, version, themeName, username, cwd string, cfg *config.Config, args []string) int {
	path, continueOnError, err := parseRecipeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "echo run: "+err.Error())
		return exitUsage
	}

	steps, err := readRecipe(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "echo run: "+err.Error())
		return exitUsage
	}

	sess, _ := newSession(s, p, project, id, stage, version, themeName, username, cwd, cfg)
	ctx := context.Background()

	runStep := func(name string, sargs []string) int {
		sess.dispatchParsed(ctx, name, sargs)
		return sess.exitCode
	}
	return runRecipeSteps(steps, continueOnError, runStep, sess.runLog)
}

// runLog emits an `echo.run` orchestration line in Echo's Odoo log style.
func (sess *session) runLog(level, msg string, fields ...logField) {
	emitOdooLog(level, "echo.run", msg, fields, sess.styles, sess.palette, sess.cfg.DBName)
}

// runRecipeSteps is the fail-fast (or continue-on-error) loop, decoupled
// from session/IO so it can be tested with a stubbed step runner. runStep
// returns each step's exit code; log emits progress/summary lines.
func runRecipeSteps(steps []string, continueOnError bool, runStep func(name string, args []string) int, log func(level, msg string, fields ...logField)) int {
	total := len(steps)
	failed := 0
	var lastCode int

	for i, step := range steps {
		log("INFO", fmt.Sprintf("step %d/%d → %s", i+1, total, step))
		fields := strings.Fields(step)
		code := runStep(fields[0], fields[1:])
		if code != exitOK {
			lastCode = code
			failed++
			if !continueOnError {
				log("ERROR", fmt.Sprintf("stopped at step %d/%d", i+1, total),
					logField{"exit", strconv.Itoa(code)})
				return code
			}
		}
	}

	if failed > 0 {
		log("ERROR", "finished with errors",
			logField{"failed", strconv.Itoa(failed)},
			logField{"steps", strconv.Itoa(total)})
		if lastCode != exitOK {
			return exitError
		}
	}
	log("INFO", fmt.Sprintf("%d steps completed", total))
	return exitOK
}

// parseRecipeArgs extracts the recipe path (first positional; empty or `-`
// means stdin) and the --continue-on-error flag. Unknown flags error.
func parseRecipeArgs(args []string) (path string, continueOnError bool, err error) {
	for _, a := range args {
		switch {
		case a == "--continue-on-error":
			continueOnError = true
		case a == "-":
			path = a
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown flag: %s", a)
		default:
			if path != "" && path != "-" {
				return "", false, fmt.Errorf("multiple recipe files given")
			}
			path = a
		}
	}
	return path, continueOnError, nil
}

// readRecipe opens the recipe (stdin when path is empty or `-`) and parses
// its lines.
func readRecipe(path string) ([]string, error) {
	if path == "" || path == "-" {
		return parseRecipeLines(os.Stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseRecipeLines(f)
}

// parseRecipeLines returns the executable steps: each line with its
// comment stripped and trimmed, dropping the now-empty ones. Both
// full-line comments (`# …`) and trailing comments (`update x  # …`) are
// supported. Pure over an io.Reader for testability.
func parseRecipeLines(r io.Reader) ([]string, error) {
	var steps []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(stripComment(sc.Text()))
		if line == "" {
			continue
		}
		steps = append(steps, line)
	}
	return steps, sc.Err()
}

// stripComment removes a `#` comment from a recipe line. A `#` starts a
// comment only at the start of the line or when preceded by whitespace,
// so a `#` embedded in a token (none occur in Echo args today, but the
// rule stays safe) is left intact. Returns the line up to the comment.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}
