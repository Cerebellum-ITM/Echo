package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CmdLogRecord is the persisted transcript of a single dispatched command:
// the metadata a listing needs plus every captured output line, tagged by
// level. One record is written per run under
// ~/.config/echo/cmd-logs/<projectKey>/<unix-millis>-<command>.json.
type CmdLogRecord struct {
	Cmd        string       `json:"cmd"`         // full command line
	Command    string       `json:"command"`     // bare verb, for filtering
	DB         string       `json:"db"`          // database name at run time
	Stage      string       `json:"stage"`       // dev/staging/prod
	From       string       `json:"from"`        // remote target, or "" for local
	Exit       int          `json:"exit"`        // process/dispatch exit code
	Started    time.Time    `json:"started"`     // when the command began
	DurationMS int64        `json:"duration_ms"` // wall-clock duration
	Errors     int          `json:"errors"`      // ERROR/CRITICAL line count
	Warnings   int          `json:"warnings"`    // WARNING line count
	Truncated  bool         `json:"truncated"`   // buffer dropped oldest lines
	Lines      []ReportLine `json:"lines"`       // captured output, level-tagged
}

// CmdLogMeta is a CmdLogRecord's header without its Lines, plus the file
// path and parsed timestamp — what a run listing loads to render rows
// without opening bodies.
type CmdLogMeta struct {
	Path       string
	Cmd        string
	Command    string
	DB         string
	Stage      string
	From       string
	Exit       int
	Started    time.Time
	DurationMS int64
	Errors     int
	Warnings   int
	Truncated  bool
	LineCount  int // number of captured lines, for a listing without the body
}

// CmdLogsDir returns the per-project command-log directory,
// ~/.config/echo/cmd-logs/<ProjectKey(abs(root))>/. The caller is
// responsible for MkdirAll before writing.
func CmdLogsDir(root string) (string, error) {
	base, err := configRoot()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return filepath.Join(base, "cmd-logs", ProjectKey(abs)), nil
}

// cmdLogFilename builds the sortable, self-describing record filename
// `<unix-millis>-<command>.json`. The millisecond stamp makes collisions
// practically impossible and lexicographic order = chronological order.
func cmdLogFilename(started time.Time, command string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, command)
	if safe == "" {
		safe = "cmd"
	}
	return strconv.FormatInt(started.UnixMilli(), 10) + "-" + safe + ".json"
}

// SaveCmdLog writes one command-log record atomically, creating the
// project's cmd-logs dir if needed. Best-effort: callers ignore the error
// so a write failure never breaks the command that triggered it.
func SaveCmdLog(root string, r CmdLogRecord) error {
	dir, err := CmdLogsDir(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, cmdLogFilename(r.Started, r.Command)), data)
}

// ListCmdLogs reads the project's cmd-logs dir and returns each record's
// metadata, newest first (lexicographic filename order reversed). A missing
// dir yields an empty slice and no error; unparseable files are skipped.
func ListCmdLogs(root string) ([]CmdLogMeta, error) {
	dir, err := CmdLogsDir(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	// Filenames lead with unix-millis, so lexicographic desc = newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	out := make([]CmdLogMeta, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		rec, ok := LoadCmdLog(path)
		if !ok {
			continue
		}
		out = append(out, CmdLogMeta{
			Path:       path,
			Cmd:        rec.Cmd,
			Command:    rec.Command,
			DB:         rec.DB,
			Stage:      rec.Stage,
			From:       rec.From,
			Exit:       rec.Exit,
			Started:    rec.Started,
			DurationMS: rec.DurationMS,
			Errors:     rec.Errors,
			Warnings:   rec.Warnings,
			Truncated:  rec.Truncated,
			LineCount:  len(rec.Lines),
		})
	}
	return out, nil
}

// LoadCmdLog reads a full record and whether it exists. A missing or
// unparseable file yields (zero, false) — never an error (the LoadRunReport
// contract).
func LoadCmdLog(path string) (CmdLogRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CmdLogRecord{}, false
	}
	var r CmdLogRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return CmdLogRecord{}, false
	}
	return r, true
}

// PruneCmdLogs trims the project's cmd-logs dir: first an age pass (remove
// records whose filename timestamp is older than retentionDays), then a
// count pass (drop the oldest beyond maxRuns). A value of 0 disables that
// pass. Both passes tolerate individual remove failures — pruning is
// best-effort and never touches anything but `*.json` in the project's own
// directory. Returns how many files were removed.
func PruneCmdLogs(root string, retentionDays, maxRuns int) (removed int, err error) {
	dir, err := CmdLogsDir(root)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // oldest first (millis-prefixed)

	// Age pass: drop anything older than the cutoff.
	if retentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
		kept := names[:0:0]
		for _, name := range names {
			if ts, ok := millisFromName(name); ok && ts < cutoff {
				if os.Remove(filepath.Join(dir, name)) == nil {
					removed++
				}
				continue
			}
			kept = append(kept, name)
		}
		names = kept
	}

	// Count pass: trim the oldest beyond maxRuns.
	if maxRuns > 0 && len(names) > maxRuns {
		excess := len(names) - maxRuns
		for _, name := range names[:excess] {
			if os.Remove(filepath.Join(dir, name)) == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// ClearCmdLogs deletes every `*.json` record in the project's cmd-logs dir,
// tolerating individual remove failures. It is the backend for Unit 82's
// `logview --clear`. A missing dir is a no-op.
func ClearCmdLogs(root string) (removed int, err error) {
	dir, err := CmdLogsDir(root)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}

// millisFromName extracts the leading unix-millis stamp from a record
// filename (`<millis>-<command>.json`).
func millisFromName(name string) (int64, bool) {
	i := strings.IndexByte(name, '-')
	if i <= 0 {
		return 0, false
	}
	ts, err := strconv.ParseInt(name[:i], 10, 64)
	if err != nil {
		return 0, false
	}
	return ts, true
}
