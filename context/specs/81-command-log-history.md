# Unit 81: command log history — persist every run's logs to disk

## Goal

Persist the captured output of **every** dispatched command (REPL,
one-shot, and recipe steps alike) as one JSON record per run under
`~/.config/echo/cmd-logs/<project-key>/`, with automatic retention
(age- and count-based pruning) so the store never grows unbounded.
This unit is pure infrastructure: it writes and prunes the history;
Unit 82 adds the interactive `logview` browser that reads it.

## Design

**Capture what the user saw, tagged by level.** The session already
buffers every printed line per command in `lastOutputBuffer` (reset by
`dispatchParsed` for each non-meta command) — the history record is that
buffer, snapshotted when the command finishes, with each line tagged by
level exactly the way recipe steps do it today (`runStepCaptured`):
`lineLevel(text)` first (keeps ERROR vs CRITICAL distinct), falling back
to `levelFromKind(kind)`. The stored shape reuses `config.ReportLine`
(`{level, text}`) so `report`'s filter machinery and Unit 82's browser
share one line format.

**One JSON file per run.** Records live under
`~/.config/echo/cmd-logs/<project-key>/` — the per-project key is the
existing `config.ProjectKey` hash, same as `projects/<key>.toml`. The
filename is sortable and self-describing:
`<unix-millis>-<command>.json` (e.g. `1751847123456-update.json`; the
millisecond timestamp makes collisions practically impossible and
lexicographic order = chronological order). Each record carries the
metadata the browser needs to render a run list without opening bodies:

```json
{
  "cmd":     "update sale --level debug",   // full command line
  "command": "update",                       // bare verb, for filtering
  "db":      "muutrade",
  "stage":   "dev",
  "from":    "",                             // remote target/--remote runs: "prod", "" = local
  "exit":    0,
  "started": "2026-07-06T18:12:03Z",
  "duration_ms": 8452,
  "errors":  0,
  "warnings": 1,
  "truncated": false,                        // lastOutputCap dropped lines
  "lines":   [{"level": "INFO", "text": "…"}, …]
}
```

**Hook: end of `dispatchParsed`.** After the switch runs the command,
snapshot and save. Skipped runs (no record written):

- Meta commands (`isMetaCommand`: help/clear/copy-last) and `report` —
  they describe the REPL, not project actions, and `report`/`copy-last`
  re-reading their own output would recurse. Unit 82's `logview` joins
  this skip list.
- Empty captures (`lastOutput.IsEmpty()`) — e.g. `unknown command`.
- Interactive alt-screen commands leave whatever *was* captured (their
  start/finalize frames); the PTY passthrough of `shell`/`bash` never
  reaches the buffer, which is fine — the frame is still a useful trace.

Saving is **best-effort**: a write failure must never break the command
(mirror `SaveRunReport`'s contract — callers ignore the error). Writes
are atomic (`writeAtomic`).

**Retention: age + count, pruned opportunistically.** Two knobs in
`global.toml`, section `[cmd_logs]`:

```toml
[cmd_logs]
retention_days = 7    # 0 = keep forever
max_runs       = 500  # per project; 0 = unlimited
disabled       = false
```

Defaults: 7 days, 500 runs, enabled. Pruning runs best-effort (errors
swallowed) at two moments: once at session start (REPL `Start` / one-shot
entry) and after each save — delete files older than `retention_days`,
then trim the oldest beyond `max_runs`. Pruning only ever touches
`*.json` inside the project's own `cmd-logs/<key>/` directory.

**No behavioral change anywhere else.** `report`, `copy-last`,
`last-run.json`, and the recipe capture keep working exactly as today;
this unit only adds a parallel sink.

## Implementation

### `internal/config/cmd_logs.go` — new file

- `type CmdLogRecord struct` — the JSON shape above (`Lines
  []ReportLine`).
- `type CmdLogMeta struct` — the record minus `Lines`, plus `Path` and
  the parsed `time.Time`; what listings load.
- `CmdLogsDir(root string) (string, error)` →
  `~/.config/echo/cmd-logs/<ProjectKey(abs(root))>/`.
- `SaveCmdLog(root string, r CmdLogRecord) error` — MkdirAll + marshal +
  `writeAtomic` to `<unix-millis>-<command>.json`.
- `ListCmdLogs(root string) ([]CmdLogMeta, error)` — read the dir,
  decode each file's metadata (a `json.Decoder` over the full record is
  fine at these sizes; skip unparseable files), newest first.
- `LoadCmdLog(path string) (CmdLogRecord, bool)` — full record;
  missing/corrupt → `(zero, false)`, never an error (LoadRunReport
  contract).
- `PruneCmdLogs(root string, retentionDays, maxRuns int) (removed int, err error)` —
  age pass then count pass, both tolerant of individual remove failures.
- `ClearCmdLogs(root string) (removed int, err error)` — delete every
  `*.json` in the project's dir (Unit 82's `logview --clear` backend).
- Config: `CmdLogsRetentionDays`, `CmdLogsMaxRuns`, `CmdLogsDisabled`
  fields on the global config (`globalFile` TOML tags under
  `[cmd_logs]`), with the 7/500/false defaults applied on load.

### `internal/repl/repl.go` — the save hook

At the end of `dispatchParsed`, after the switch:

```go
sess.saveCmdLog(cmd, args, time.Since(started))
```

`saveCmdLog` (new, in a small `internal/repl/cmdlog.go`):

- returns immediately for meta commands, `report`, `logview` (registered
  in the skip list), empty buffers, or `CmdLogsDisabled`;
- builds `config.ReportLine`s from `sess.lastOutput.Filtered(nil)` with
  the `lineLevel`→`levelFromKind` tagging (extract that loop from
  `runStepCaptured` into a shared helper `captureReportLines(lines
  []Line) []config.ReportLine` and reuse it in both places);
- fills `from` with the resolved remote switch when the args carry one
  (`remoteRunFlags(args)`: the named `--from` target, or the literal
  `"remote"` for a bare `--remote`); `""` for local runs — remote
  output already flows through the same buffer (`runSSHStream` →
  `emitStreamLine` → `print`), this field only labels the record;
- fills the metadata (`sess.cfg.DBName`, `sess.stage`, `sess.exitCode`,
  `sess.lastErrors`/`lastWarnings`, `lastOutput.truncated`, duration)
  and calls `config.SaveCmdLog` + `config.PruneCmdLogs`, both
  best-effort.

`dispatchParsed` measures `started := time.Now()` at its top. Recipe
steps call `dispatchParsed` internally, so each step lands as its own
record with no extra wiring — but note `runStepCaptured` resets the
buffer *after* the dispatch returns, which is after the hook fired:
ordering already works.

### Session start pruning

In `Start` (REPL) and `RunOnce` (one-shot), fire a best-effort
`config.PruneCmdLogs` once with the configured knobs (goroutine not
needed — pruning a few hundred files is instant; keep it synchronous
and simple).

### Tests

`internal/config/cmd_logs_test.go`:

- Save → List round-trip: metadata (cmd/db/exit/started) survives;
  newest-first ordering by filename.
- `PruneCmdLogs`: age pass removes back-dated files (write with an old
  timestamp in the name and old mtime), count pass trims to `max_runs`
  keeping the newest; `0` disables each pass.
- `LoadCmdLog` on a corrupt file → `(zero, false)`.

`internal/repl/cmdlog_test.go`:

- `captureReportLines` tags by text token first, Kind fallback second
  (table over the `lineLevel`/`levelFromKind` cases).
- Skip list: meta commands and empty buffers produce no file (drive
  `saveCmdLog` against a temp `HOME`).

## Dependencies

None new — stdlib + existing `config` helpers (`writeAtomic`,
`ProjectKey`, `ReportLine`).

## Verify when done

- [ ] Running `update sale` (or any module command) in the REPL leaves a
      `<millis>-update.json` under `~/.config/echo/cmd-logs/<key>/` with
      the same lines `copy-last` would copy, each tagged with a level.
- [ ] One-shot `echo modstate` and each step of an `echo run` recipe
      produce one record apiece.
- [ ] A remote run (`test sale --from prod`, `logs --remote`) persists
      its streamed lines like a local one, with `from` naming the
      target.
- [ ] `help`, `clear`, `copy-last`, `report` and unknown commands leave
      no record.
- [ ] With `retention_days = 7`, records older than 7 days disappear on
      the next session start; with `max_runs = 500`, the oldest records
      beyond 500 are trimmed after a save.
- [ ] `disabled = true` under `[cmd_logs]` stops all writes (and
      pruning) without any other behavior change.
- [ ] A read-only `cmd-logs` dir (forced write failure) does not break
      or delay the command that triggered the save.
- [ ] `report` and `copy-last` behave byte-for-byte as before.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/config/...
      ./internal/repl/...` pass.
