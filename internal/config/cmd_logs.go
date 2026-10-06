package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
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
	// DeployedTip is the full SHA of the branch tip a `watch-deploy` cycle
	// shipped — empty for every other command. It lets a headless caller test
	// `git merge-base --is-ancestor <commit> <DeployedTip>` to learn whether a
	// specific commit made it into an auto-deploy (watch batches, so the tip,
	// not an exact SHA, is the deployed frontier).
	DeployedTip string `json:"deployed_tip,omitempty"`
}

// CmdLogMeta is a CmdLogRecord's header without its Lines, plus the file
// path and parsed timestamp — what a run listing loads to render rows
// without opening bodies.
type CmdLogMeta struct {
	Path        string    `json:"-"` // local record path — internal, not agent-facing
	Cmd         string    `json:"cmd"`
	Command     string    `json:"command"`
	DB          string    `json:"db"`
	Stage       string    `json:"stage"`
	From        string    `json:"from"`
	Exit        int       `json:"exit"`
	Started     time.Time `json:"started"`
	DurationMS  int64     `json:"duration_ms"`
	Errors      int       `json:"errors"`
	Warnings    int       `json:"warnings"`
	Truncated   bool      `json:"truncated"`
	LineCount   int       `json:"line_count"`             // captured lines, without the body
	DeployedTip string    `json:"deployed_tip,omitempty"` // branch tip a `watch-deploy` cycle shipped
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
	return cmdLogStem(started, command) + ".json"
}

// cmdLogStem is the `<unix-millis>-<command>` name shared by a record and
// the live file of the same run.
func cmdLogStem(started time.Time, command string) string {
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
	return strconv.FormatInt(started.UnixMilli(), 10) + "-" + safe
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

const cmdLogLiveSuffix = ".running.ndjson"

// cmdLogLiveMaxBytes caps a live file; a var only so tests can shrink it.
var cmdLogLiveMaxBytes = 2 << 20

// CmdLogLiveHeader is the first line of a live file. Its fields hold what
// the run's CmdLogRecord will hold; PID lets a reader that cannot flock
// tell a running writer from an orphan.
type CmdLogLiveHeader struct {
	Schema    int       `json:"schema"`
	Cmd       string    `json:"cmd"`
	Command   string    `json:"command"`
	DB        string    `json:"db"`
	Stage     string    `json:"stage"`
	From      string    `json:"from"`
	Started   time.Time `json:"started"`
	PID       int       `json:"pid"`
	Truncated bool      `json:"truncated,omitempty"`
}

// CmdLogLive is the `<unix-millis>-<command>.running.ndjson` file a run
// appends its captured lines to while it happens: the header, then one
// ReportLine per line. The writer holds LOCK_EX on it until Remove, so a
// file whose lock can be taken belongs to a dead process.
type CmdLogLive struct {
	file    *os.File
	path    string
	header  CmdLogLiveHeader
	written int
}

// OpenCmdLogLive creates the live file for a run, locks it and writes the
// header. On any failure nothing is left on disk.
func OpenCmdLogLive(root string, h CmdLogLiveHeader) (*CmdLogLive, error) {
	dir, err := CmdLogsDir(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Created under a temporary name and renamed once locked: a prune in
	// another Echo process must never see the final name without a lock,
	// or it would take the new file for an orphan. The lock follows the
	// inode through the rename.
	path := filepath.Join(dir, cmdLogStem(h.Started, h.Command)+cmdLogLiveSuffix)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	h.Schema = 1
	l := &CmdLogLive{file: f, path: tmp, header: h}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		l.Remove()
		return nil, err
	}
	if err := l.writeJSON(h); err != nil {
		l.Remove()
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		l.Remove()
		return nil, err
	}
	l.path = path
	return l, nil
}

// Append writes one line. When it would push the file past the cap, the
// file is first rewritten in place from keep() (the lines the run still
// buffers) under a header marked truncated; a reader that sees the file
// shrink starts over.
func (l *CmdLogLive) Append(line ReportLine, keep func() []ReportLine) error {
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if l.written+len(data) > cmdLogLiveMaxBytes {
		if err := l.compact(keep()); err != nil {
			return err
		}
	}
	return l.write(data)
}

// compact rewrites the file as the truncated header plus the newest of
// kept that fit in half the cap, so the next compaction is another half cap
// of output away instead of on the very next line.
func (l *CmdLogLive) compact(kept []ReportLine) error {
	h := l.header
	h.Truncated = true
	header, err := json.Marshal(h)
	if err != nil {
		return err
	}
	header = append(header, '\n')

	budget := cmdLogLiveMaxBytes/2 - len(header)
	var tail [][]byte
	for i := len(kept) - 1; i >= 0; i-- {
		data, err := json.Marshal(kept[i])
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if len(data) > budget {
			break
		}
		budget -= len(data)
		tail = append(tail, data)
	}

	buf := header
	for i := len(tail) - 1; i >= 0; i-- {
		buf = append(buf, tail[i]...)
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	l.written = 0
	return l.write(buf)
}

func (l *CmdLogLive) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return l.write(append(data, '\n'))
}

func (l *CmdLogLive) write(data []byte) error {
	n, err := l.file.Write(data)
	l.written += n
	return err
}

// Remove closes the live file, releasing its lock, and deletes it.
func (l *CmdLogLive) Remove() {
	if l == nil {
		return
	}
	_ = l.file.Close()
	_ = os.Remove(l.path)
}

// isOrphanLiveFile reports whether nobody holds the live file's lock, i.e.
// the process that wrote it is gone.
func isOrphanLiveFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

// removeOrphanLiveFiles deletes the live files in dir left by dead
// processes and returns how many it removed.
func removeOrphanLiveFiles(dir string, entries []os.DirEntry) (removed int) {
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), cmdLogLiveSuffix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if isOrphanLiveFile(path) && os.Remove(path) == nil {
			removed++
		}
	}
	return removed
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
			Path:        path,
			Cmd:         rec.Cmd,
			Command:     rec.Command,
			DB:          rec.DB,
			Stage:       rec.Stage,
			From:        rec.From,
			Exit:        rec.Exit,
			Started:     rec.Started,
			DurationMS:  rec.DurationMS,
			Errors:      rec.Errors,
			Warnings:    rec.Warnings,
			Truncated:   rec.Truncated,
			LineCount:   len(rec.Lines),
			DeployedTip: rec.DeployedTip,
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
// best-effort and never touches anything but `*.json` records and orphaned
// live files in the project's own directory. Orphans count in the returned
// number of removed files but not toward maxRuns.
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
	removed = removeOrphanLiveFiles(dir, entries)
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

// ClearCmdLogs deletes every `*.json` record and every orphaned live file
// in the project's cmd-logs dir, tolerating individual remove failures. It
// is the backend for Unit 82's `logview --clear`. A missing dir is a no-op.
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
	removed = removeOrphanLiveFiles(dir, entries)
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
