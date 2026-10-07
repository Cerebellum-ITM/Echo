package repl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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

// remoteRunFlags extracts the remote-mode switches (`--from <v>`,
// `--from=<v>`, `--remote`) from an argument list.
func remoteRunFlags(args []string) (from string, remote bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--remote":
			remote = true
		case a == "--from":
			if i+1 < len(args) {
				from = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--from="):
			from = strings.TrimPrefix(a, "--from=")
		case a == "-E", a == "--env":
			if i+1 < len(args) {
				from = cmd.ReverbRef(args[i+1])
				i++
			}
		case strings.HasPrefix(a, "-E="):
			from = cmd.ReverbRef(strings.TrimPrefix(a, "-E="))
		case strings.HasPrefix(a, "--env="):
			from = cmd.ReverbRef(strings.TrimPrefix(a, "--env="))
		}
	}
	return from, remote
}

// parseShellRunArgs splits shell-run's argument list into its flags and
// positionals. A lone `-` is a positional (the stdin source), NOT an
// unknown flag — the dash-prefix catch-all must not swallow it. The
// `--from`/`-E` value is consumed so it is never mistaken for a script
// name; other unknown flags are ignored rather than treated as scripts.
func parseShellRunArgs(args []string) (noCopy bool, from string, remote bool, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--no-copy":
			noCopy = true
		case a == "--remote":
			remote = true
		case a == "--from":
			if i+1 < len(args) {
				from = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--from="):
			from = strings.TrimPrefix(a, "--from=")
		case a == "-E", a == "--env":
			if i+1 < len(args) {
				from = cmd.ReverbRef(args[i+1])
				i++
			}
		case strings.HasPrefix(a, "-E="):
			from = cmd.ReverbRef(strings.TrimPrefix(a, "-E="))
		case strings.HasPrefix(a, "--env="):
			from = cmd.ReverbRef(strings.TrimPrefix(a, "--env="))
		case a == "-":
			positional = append(positional, a)
		case strings.HasPrefix(a, "-"):
			// ignore unknown flags rather than treating them as a script name
		default:
			positional = append(positional, a)
		}
	}
	return noCopy, from, remote, positional
}

// runShellRun implements `shell-run [<file>] [--no-copy]`: pipe a local .py
// through the Odoo shell (stdin), stream the output Odoo-colored like
// `update`, and auto-copy the captured output to the clipboard on success.
func (sess *session) runShellRun(ctx context.Context, args []string) {
	sess.startLog("shell-run", args)

	noCopy, from, remote, positional := parseShellRunArgs(args)

	dir := sess.scriptsDir()
	var scriptPath string
	var body []byte
	var err error
	switch {
	case len(positional) > 0 && positional[0] == "-":
		// Explicit stdin source, mirroring `echo run -`. A TTY stdin would
		// block forever waiting for input, so it fails fast instead.
		if !cmd.StdinPiped() {
			err = fmt.Errorf("stdin is a terminal — pipe a script or pass a file")
		} else {
			body, err = io.ReadAll(os.Stdin)
		}
	case len(positional) > 0:
		scriptPath, err = resolveScriptArg(positional[0], dir, sess.projectDir)
	default:
		scriptPath, err = pickScriptFile(dir, sess.palette)
	}
	// The body read here is the one that runs, so the record's hash is of
	// exactly what Odoo executed.
	if err == nil && scriptPath != "" {
		body, err = os.ReadFile(scriptPath)
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
		Stdin:      bytes.NewReader(body),
		Args:       args,
		From:       from,
		Remote:     remote,
		Palette:    sess.palette,
		Log:        sess.cmdOdooLogger("shell-run"),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
	})
	sess.extras.script = newScriptFields(scriptPath, body, sess.lastOutput.lines)

	if runErr == nil && !noCopy && !sess.lastOutput.IsEmpty() {
		sess.copyShellRunOutput()
	}
	sess.readonlyFinalize("shell-run", runErr)
}

// runShellPiped is `shell`'s headless pipe mode: stdin is not a TTY, so
// the piped content is fed to the Odoo shell — local or remote per
// --from/--remote — through the shell-run machinery. Output streams
// Odoo-colored; unlike shell-run there is no auto-copy (a pipeline
// consumer owns the output; `copy-last` still works). The caller
// (runShell) already emitted the start line.
func (sess *session) runShellPiped(ctx context.Context, args []string) {
	from, remote := remoteRunFlags(args)

	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		sess.readonlyFinalize("shell", err)
		return
	}

	lc := &logColorer{}
	stats := &runStats{}
	runErr := cmd.RunShellScript(ctx, cmd.ShellScriptOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Stdin:   bytes.NewReader(body),
		Args:    args,
		From:    from,
		Remote:  remote,
		Palette: sess.palette,
		Log:     sess.cmdOdooLogger("shell"),
		StreamOut: stats.wrap(func(line string) {
			sess.emitStreamLine(lc, line)
		}),
	})
	sess.extras.script = newScriptFields("", body, sess.lastOutput.lines)
	sess.readonlyFinalize("shell", runErr)
}

// scriptBodyMax caps the script body kept in a history record; the hash
// still covers the whole body.
const scriptBodyMax = 64 << 10

// scriptFields is the script part of a `shell-run` / piped `shell` record.
type scriptFields struct {
	path          string
	sha256        string
	body          string
	bodyTruncated bool
	outputLines   []string
}

// newScriptFields builds the record's script fields from the script's path
// ("" for stdin), its full body and the lines captured by the run. A body
// over scriptBodyMax is cut back to the last whole UTF-8 rune.
func newScriptFields(path string, body []byte, captured []Line) *scriptFields {
	sum := sha256.Sum256(body)
	sf := &scriptFields{path: path, sha256: hex.EncodeToString(sum[:])}
	kept := body
	if len(kept) > scriptBodyMax {
		cut := scriptBodyMax
		for cut > 0 && !utf8.RuneStart(kept[cut]) {
			cut--
		}
		kept = kept[:cut]
		sf.bodyTruncated = true
	}
	sf.body = string(kept)
	for _, l := range scriptOutputLines(captured) {
		sf.outputLines = append(sf.outputLines, l.Text)
	}
	return sf
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
