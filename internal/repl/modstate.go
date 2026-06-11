package repl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/docker"
)

// runModstate implements `modstate [--all] [--json]`: dump every module's
// name/state/latest_version from ir_module_module for the active DB.
// Default is installed-only; --all widens to every state. Human mode emits
// an aligned `name | state | version` table; --json emits a clean JSON
// array to stdout only (logs/errors to stderr), so the output can be piped
// straight into jq.
func (sess *session) runModstate(ctx context.Context, args []string) {
	wantJSON := false
	for _, a := range args {
		if a == "--json" {
			wantJSON = true
			break
		}
	}

	res, err := cmd.RunModstate(ctx, cmd.ModstateOpts{
		Cfg:  sess.cfg,
		Root: sess.projectDir,
		Args: args,
	})
	if err != nil {
		// Both modes map the error the same way (usage vs execution); only
		// where the diagnostic lands differs. In --json mode it goes to
		// stderr so stdout stays empty; otherwise it frames via finalize.
		code := exitCodeFor(err)
		if wantJSON {
			emitOdooLogTo(os.Stderr, "ERROR", "echo.modstate", "modstate failed",
				[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		} else {
			sess.finalize("modstate", 1, 0, err) // finalize sets exitCode...
		}
		sess.exitCode = code // ...so override it with the usage-aware code
		return
	}

	if wantJSON {
		sess.emitModstateJSON(res.Rows)
		return
	}
	sess.emitModstateTable(res)
}

// exitCodeFor maps a RunModstate error to the script exit code: a missing
// DB config or a bad flag is a usage error (2), anything else execution (1).
func exitCodeFor(err error) int {
	if errors.Is(err, cmd.ErrNoDB) || strings.Contains(err.Error(), "unknown flag") ||
		strings.Contains(err.Error(), "takes no arguments") {
		return exitUsage
	}
	return exitError
}

// emitModstateJSON marshals the rows to a JSON array and writes it — and
// only it — to stdout. A NULL latest_version serializes as JSON null.
func (sess *session) emitModstateJSON(rows []docker.ModuleStateRow) {
	type modJSON struct {
		Name    string  `json:"name"`
		State   string  `json:"state"`
		Version *string `json:"version"`
	}
	out := make([]modJSON, 0, len(rows))
	for _, r := range rows {
		m := modJSON{Name: r.Name, State: r.State}
		if !r.VersionNull {
			v := r.Version
			m.Version = &v
		}
		out = append(out, m)
	}
	b, err := json.Marshal(out)
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.modstate", "encode failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
	sess.exitCode = exitOK
}

// emitModstateTable renders the human `name | state | version` table
// through the session theme and closes with a count line.
func (sess *session) emitModstateTable(res cmd.ModstateResult) {
	scope := "installed"
	if res.All {
		scope = "all"
	}
	if len(res.Rows) == 0 {
		emitOdooLog("INFO", "echo.modstate", "no modules",
			[]logField{{"scope", scope}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitOK
		return
	}

	const (
		nameHdr = "name"
		stHdr   = "state"
		verHdr  = "version"
	)
	nameW, stW := len(nameHdr), len(stHdr)
	for _, r := range res.Rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if len(r.State) > stW {
			stW = len(r.State)
		}
	}

	accent := lipgloss.NewStyle().Bold(true).Foreground(sess.palette.Accent)
	header := accent.Render(pad(nameHdr, nameW)) + "  " +
		accent.Render(pad(stHdr, stW)) + "  " + accent.Render(verHdr)
	sess.print(Line{Kind: "table", Text: header})

	for _, r := range res.Rows {
		ver := r.Version
		verStyle := sess.styles.Out
		if r.VersionNull || ver == "" {
			ver = "-"
			verStyle = sess.styles.Dim
		}
		line := sess.styles.Out.Render(pad(r.Name, nameW)) + "  " +
			sess.stateStyle(r.State).Render(pad(r.State, stW)) + "  " +
			verStyle.Render(ver)
		sess.print(Line{Kind: "table", Text: line})
	}

	emitOdooLog("INFO", "echo.modstate", "modules listed",
		[]logField{{"count", strconv.Itoa(len(res.Rows))}, {"scope", scope}},
		sess.styles, sess.palette, sess.cfg.DBName)
	sess.exitCode = exitOK
}

// stateStyle colors a module state cell: installed reads as success,
// pending transitions as info, removed/uninstallable dim, else default.
func (sess *session) stateStyle(state string) lipgloss.Style {
	switch state {
	case "installed":
		return sess.styles.Ok
	case "to upgrade", "to install", "to remove":
		return sess.styles.Info
	case "uninstalled", "uninstallable":
		return sess.styles.Dim
	default:
		return sess.styles.Out
	}
}

// pad right-pads s with spaces to width w (no truncation).
func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
