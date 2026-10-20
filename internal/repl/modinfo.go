package repl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
)

// stripFlag removes every occurrence of flag from args, reporting whether
// it was present. Used for session-only `--last` handling.
func stripFlag(args []string, flag string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == flag {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// runModinfo implements `modinfo <mod...> | --all [--json] [--from <t> |
// --remote] [--copy] [--last]`: compare the version Odoo recorded as
// installed (ir_module_module.latest_version + state) against each module's
// manifest version and emit one `echo.modinfo` line per module, the verdict
// driving its level. `--json` prints only the array on stdout. `--last`
// replays the session's last modinfo modules (in-memory only, local only).
func (sess *session) runModinfo(ctx context.Context, args []string) {
	args, last := stripFlag(args, "--last")
	wantJSON := seqHasFlag(args, "--json")
	if last {
		if from, remote := remoteRunFlags(args); from != "" || remote {
			sess.modinfoFailed(wantJSON, fmt.Errorf("%w: --last is local-only, drop --from/--remote", cmd.ErrUsage))
			return
		}
		if len(sess.lastModinfoModules) == 0 {
			emitOdooLog("WARNING", "echo.modinfo", "no previous modinfo this session",
				nil, sess.styles, sess.palette, sess.cfg.DBName)
			sess.exitCode = exitUsage
			return
		}
		args = append(append([]string{}, sess.lastModinfoModules...), args...)
	}

	logFn := sess.cmdOdooLogger("modinfo")
	if wantJSON {
		logFn = sess.stderrOdooLogger("modinfo")
	}
	rep, err := cmd.RunModinfo(ctx, cmd.ModinfoOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Args:    args,
		Palette: sess.palette,
		Log:     logFn,
	})
	if err != nil {
		sess.modinfoFailed(wantJSON, err)
		return
	}
	sess.lastModinfoModules = make([]string, 0, len(rep.Modules))
	for _, m := range rep.Modules {
		sess.lastModinfoModules = append(sess.lastModinfoModules, m.Module)
	}

	if rep.JSON {
		sess.emitModinfoJSON(rep)
		return
	}
	sess.emitModinfoLines(rep)
}

// modinfoFailed reports a failed run: on stderr in --json mode so stdout
// stays empty, framed by finalize otherwise.
func (sess *session) modinfoFailed(wantJSON bool, err error) {
	if sess.handleQuit(err) {
		return
	}
	if wantJSON {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.modinfo", "modinfo failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
	} else {
		errorCount := 1
		if errors.Is(err, cmd.ErrCancelled) || errors.Is(err, huh.ErrUserAborted) ||
			errors.Is(err, cmd.ErrNonInteractive) {
			errorCount = 0
		}
		sess.finalize("modinfo", errorCount, 0, err)
	}
	sess.exitCode = exitCodeFor(err)
}

func (sess *session) emitModinfoJSON(rep cmd.ModinfoReport) {
	type modinfoJSON struct {
		Module          string  `json:"module"`
		State           string  `json:"state"`
		DBVersion       string  `json:"db_version"`
		ManifestVersion string  `json:"manifest_version"`
		DeployedVersion *string `json:"deployed_version,omitempty"`
		Status          string  `json:"status"`
	}
	out := make([]modinfoJSON, 0, len(rep.Modules))
	for _, m := range rep.Modules {
		row := modinfoJSON{
			Module:          m.Module,
			State:           m.DBState,
			DBVersion:       m.DBVersion,
			ManifestVersion: m.Manifest,
			Status:          m.Status,
		}
		if rep.Remote {
			deployed := m.Deployed
			row.DeployedVersion = &deployed
		}
		out = append(out, row)
	}
	b, err := json.Marshal(out)
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.modinfo", "encode failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, rep.DB)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(append(b, '\n'))
	sess.exitCode = exitOK
}

// emitModinfoLines prints one verdict line per module and, for several, a
// closing count line. --copy copies the plain verdicts first and marks the
// last line copied.
func (sess *session) emitModinfoLines(rep cmd.ModinfoReport) {
	type verdict struct {
		level  string
		fields []logField
	}
	lines := make([]verdict, 0, len(rep.Modules))
	plain := make([]string, 0, len(rep.Modules))
	for _, m := range rep.Modules {
		fields := modinfoFields(m, rep.Remote)
		level := "INFO"
		if m.Status != "in sync" {
			level = "WARNING"
		}
		lines = append(lines, verdict{level, fields})
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			parts = append(parts, f.key+"="+f.value)
		}
		plain = append(plain, strings.Join(parts, " "))
	}

	var summary []logField
	if len(rep.Modules) > 1 {
		summary = modinfoSummary(rep.Modules)
	}

	if rep.Copy {
		if err := clipboard.WriteAll(strings.Join(plain, "\n")); err != nil {
			emitOdooLog("ERROR", "echo.modinfo", "copy failed: "+err.Error(),
				nil, sess.styles, sess.palette, rep.DB)
			sess.exitCode = exitError
			return
		}
		copied := logField{"copied", "true"}
		if summary != nil {
			summary = append(summary, copied)
		} else if len(lines) > 0 {
			lines[len(lines)-1].fields = append(lines[len(lines)-1].fields, copied)
		}
	}

	for _, l := range lines {
		emitOdooLog(l.level, "echo.modinfo", "module inspected", l.fields,
			sess.styles, sess.palette, rep.DB)
	}
	if summary != nil {
		emitOdooLog("INFO", "echo.modinfo", "checked", summary,
			sess.styles, sess.palette, rep.DB)
	}
	sess.exitCode = exitOK
}

// modinfoFields renders one verdict as log fields; deployed= appears only
// for a remote target, where a deploy lock can exist.
func modinfoFields(m cmd.ModinfoResult, remote bool) []logField {
	orDash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	state := m.DBState
	if state == "" {
		state = "absent"
	}
	fields := []logField{
		{"module", m.Module},
		{"db", orDash(m.DBVersion)},
		{"state", state},
		{"manifest", orDash(m.Manifest)},
	}
	if remote {
		fields = append(fields, logField{"deployed", orDash(m.Deployed)})
	}
	return append(fields, logField{"status", m.Status})
}

func modinfoSummary(modules []cmd.ModinfoResult) []logField {
	var pending, ahead, missing int
	for _, m := range modules {
		switch m.Status {
		case "update pending":
			pending++
		case "db ahead":
			ahead++
		case "not installed":
			missing++
		}
	}
	return []logField{
		{"modules", strconv.Itoa(len(modules))},
		{"pending", strconv.Itoa(pending)},
		{"ahead", strconv.Itoa(ahead)},
		{"missing", strconv.Itoa(missing)},
	}
}
