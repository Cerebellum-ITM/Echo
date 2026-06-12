package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/theme"
)

// scriptsDir resolves the directory `shell-run` lists `.py` scripts from.
// Precedence: an explicit cfg.ScriptsDir (relative → under the project root,
// absolute → as-is); else a conventional top-level `scripts/` folder when it
// exists; else the project root. The convention means dropping scripts in
// `<project>/scripts/` needs no config at all.
func (sess *session) scriptsDir() string {
	if d := sess.cfg.ScriptsDir; d != "" {
		if filepath.IsAbs(d) {
			return d
		}
		return filepath.Join(sess.projectDir, d)
	}
	if conv := filepath.Join(sess.projectDir, "scripts"); isDir(conv) {
		return conv
	}
	return sess.projectDir
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// pythonScriptsIn returns the names of the *.py files directly in dir
// (top-level, no recursion), newest-first by creation time — the same
// ordering the recipe picker uses. Pure over the filesystem so it's
// unit-testable.
func pythonScriptsIn(dir string) ([]string, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var entries []recipeEntry
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		var created time.Time
		if info, ierr := e.Info(); ierr == nil {
			created = fileCreated(info)
		}
		entries = append(entries, recipeEntry{name: e.Name(), created: created})
	}
	sortRecipesByCreation(entries)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
	}
	return names, nil
}

// pickScriptFile lists the *.py scripts in dir and opens a single-select
// picker, returning the absolute path of the chosen script. Returns a clear
// error when none are found, or ErrCancelled on Esc.
func pickScriptFile(dir string, p theme.Palette) (string, error) {
	names, err := pythonScriptsIn(dir)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no .py scripts found in %s", dir)
	}
	name, err := cmd.PickOne("Python script to run", names, p)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// resolveScriptArg turns a positional `shell-run` argument into an absolute
// path. A bare name (no separator) is looked up in scriptsDir; a path with a
// separator (or absolute) is resolved against projectDir. The target must be
// an existing, non-directory `.py` file.
func resolveScriptArg(arg, scriptsDir, projectDir string) (string, error) {
	if !strings.HasSuffix(arg, ".py") {
		return "", fmt.Errorf("not a .py script: %s", arg)
	}
	var path string
	switch {
	case filepath.IsAbs(arg):
		path = arg
	case strings.ContainsRune(arg, filepath.Separator):
		path = filepath.Join(projectDir, arg)
	default:
		path = filepath.Join(scriptsDir, arg)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("script not found: %s", path)
	}
	if info.IsDir() {
		return "", fmt.Errorf("not a file: %s", path)
	}
	return path, nil
}

// runShellRun implements `shell-run [<file>] [--no-copy]`: pipe a local .py
// through the Odoo shell (stdin), stream the output Odoo-colored like
// `update`, and auto-copy the captured output to the clipboard on success.
func (sess *session) runShellRun(ctx context.Context, args []string) {
	sess.startLog("shell-run", args)

	noCopy := false
	var from string
	var remote bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--no-copy":
			noCopy = true
		case a == "--remote":
			remote = true
		case a == "--from":
			// Consume the value too, so it is never mistaken for a script.
			if i+1 < len(args) {
				from = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--from="):
			from = strings.TrimPrefix(a, "--from=")
		case strings.HasPrefix(a, "-"):
			// ignore unknown flags rather than treating them as a script name
		default:
			positional = append(positional, a)
		}
	}

	dir := sess.scriptsDir()
	var scriptPath string
	var err error
	if len(positional) > 0 {
		scriptPath, err = resolveScriptArg(positional[0], dir, sess.projectDir)
	} else {
		scriptPath, err = pickScriptFile(dir, sess.palette)
	}
	if err != nil {
		sess.readonlyFinalize("shell-run", err)
		return
	}

	lc := &logColorer{}
	stats := &runStats{}
	runErr := cmd.RunShellScript(ctx, cmd.ShellScriptOpts{
		Cfg:        sess.cfg,
		Root:       sess.projectDir,
		ScriptPath: scriptPath,
		Args:       args,
		From:       from,
		Remote:     remote,
		Palette:    sess.palette,
		Log:        sess.cmdOdooLogger("shell-run"),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
	})

	if runErr == nil && !noCopy && !sess.lastOutput.IsEmpty() {
		sess.copyShellRunOutput()
	}
	sess.readonlyFinalize("shell-run", runErr)
}

// copyShellRunOutput copies only the script's own output to the clipboard —
// the plain stdout lines — dropping the Odoo shell's boot/init log lines so a
// `print(...)` result lands clean. (`copy-last` still copies everything.)
func (sess *session) copyShellRunOutput() {
	lines := scriptOutputLines(sess.lastOutput.lines)
	if len(lines) == 0 {
		sess.print(Line{Kind: "warn", Text: "no script output to copy (only shell logs)"})
		return
	}
	if err := clipboard.WriteAll(linesToPlain(lines, false)); err != nil {
		sess.print(Line{Kind: "warn", Text: "copy failed: " + err.Error()})
		return
	}
	plural := ""
	if len(lines) != 1 {
		plural = "s"
	}
	sess.print(Line{Kind: "ok", Text: fmt.Sprintf("copied %d line%s to clipboard", len(lines), plural)})
}

// scriptOutputLines keeps only the script's stdout: the lines that are NOT
// Odoo/loguru/loose-severity log lines (those are the shell's boot/init
// noise), with leading/trailing blank lines trimmed.
func scriptOutputLines(lines []Line) []Line {
	var out []Line
	for _, l := range lines {
		if lineLevel(l.Text) != "" {
			continue // an Odoo-format log line → shell init noise, not output
		}
		out = append(out, l)
	}
	start, end := 0, len(out)
	for start < end && strings.TrimSpace(out[start].Text) == "" {
		start++
	}
	for end > start && strings.TrimSpace(out[end-1].Text) == "" {
		end--
	}
	return out[start:end]
}
