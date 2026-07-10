package cmd

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// remoteCmdLogMarker prefixes each record's JSON in the SSH payload so the
// concatenated files can be split back apart. A pretty-printed CmdLogRecord
// (MarshalIndent) never contains this token, and record filenames are
// sanitized to [A-Za-z0-9_-] so a name can't forge a marker either.
const remoteCmdLogMarker = "@@ECHOLOGFILE "

// FetchRemoteCmdLogs reads a remote target's per-command log history over SSH.
// It resolves the target (a named `--from`, or this directory's `link`
// binding when from is ""), derives the remote project's cmd-logs directory
// from the deterministic ProjectKey of its remote path — the same key Echo
// used to save the records on the server — and streams every record's JSON in
// one round-trip. The remote side is strictly read-only. Returns the run
// metadata newest first, a basename→record map for on-demand detail loading,
// and the resolved target label.
func FetchRemoteCmdLogs(ctx context.Context, cfg *config.Config, palette theme.Palette, root, from string, log func(level, sub, msg, db string, fields ...[2]string)) ([]config.CmdLogMeta, map[string]config.CmdLogRecord, string, error) {
	sshHost, remotePath, fromName, err := resolveRemoteTarget(cfg, palette, from, log)
	if err != nil {
		return nil, nil, "", err
	}
	key := config.ProjectKey(remotePath)
	// Respect the remote's XDG override, defaulting to ~/.config; glob every
	// record and bracket each with the marker + its basename.
	script := `dir="${XDG_CONFIG_HOME:-$HOME/.config}/echo/cmd-logs/` + key +
		`"; for f in "$dir"/*.json; do [ -e "$f" ] || continue; ` +
		`printf '` + remoteCmdLogMarker + `%s@@\n' "$(basename "$f")"; cat "$f"; done`
	out, err := runSSH(ctx, sshHost, script, nil)
	if err != nil {
		return nil, nil, "", err
	}
	metas, byPath := parseRemoteCmdLogs(string(out))
	return metas, byPath, fromName, nil
}

// parseRemoteCmdLogs splits the marker-delimited SSH payload into records,
// building the same CmdLogMeta a local listing would (with meta.Path set to
// the record's basename, the key into the returned map). Metas come back
// newest first — basenames are millis-prefixed, so descending name order is
// descending time. Unparseable sections are skipped, mirroring LoadCmdLog.
func parseRemoteCmdLogs(out string) ([]config.CmdLogMeta, map[string]config.CmdLogRecord) {
	byPath := map[string]config.CmdLogRecord{}
	var names []string
	var curName string
	var buf []string

	flush := func() {
		if curName == "" {
			return
		}
		var r config.CmdLogRecord
		if json.Unmarshal([]byte(strings.Join(buf, "\n")), &r) == nil {
			byPath[curName] = r
			names = append(names, curName)
		}
		curName, buf = "", nil
	}

	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, remoteCmdLogMarker) && strings.HasSuffix(ln, "@@") {
			flush()
			curName = strings.TrimSuffix(strings.TrimPrefix(ln, remoteCmdLogMarker), "@@")
			continue
		}
		if curName != "" {
			buf = append(buf, ln)
		}
	}
	flush()

	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	metas := make([]config.CmdLogMeta, 0, len(names))
	for _, name := range names {
		r := byPath[name]
		metas = append(metas, config.CmdLogMeta{
			Path:       name,
			Cmd:        r.Cmd,
			Command:    r.Command,
			DB:         r.DB,
			Stage:      r.Stage,
			From:       r.From,
			Exit:       r.Exit,
			Started:    r.Started,
			DurationMS: r.DurationMS,
			Errors:     r.Errors,
			Warnings:   r.Warnings,
			Truncated:  r.Truncated,
			LineCount:  len(r.Lines),
		})
	}
	return metas, byPath
}
