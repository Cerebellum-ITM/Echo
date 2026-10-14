# Unit 132 · db-restore-progress
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-14.

## Goal

`db-restore` (and `db-pull --restore`, which shares `restoreBackupFile`) never goes silent for more than 30 s:
the data load and the zip extraction report byte progress as INFO lines, and every long phase (including the
index/constraint tail after the dump is fully read, and the filestore copy) emits a heartbeat with the elapsed
time, so a working restore can no longer look hung.

## Design

Why the restore looks hung today (read in the code, 2026-10-14):

- A native Odoo zip carries `dump.sql`, loaded by `RestoreSQL` with `psql -q`: quiet mode, stdout discarded, so
  the whole load prints nothing between `restoring data format=sql` and the end (`internal/docker/pgdump.go`).
- `unzip` (`internal/cmd/db.go`) extracts a multi-GB archive (filestore included) after one `extracting archive`
  line and nothing else.
- `pg_restore --verbose` prints one DEBUG line per object, so a large table's `COPY` and the post-data
  `CREATE INDEX` / `ADD CONSTRAINT` phase are each one line followed by minutes of silence.
- `copyFilestoreToContainer` (`docker cp`) prints nothing while copying.

Decisions:

1. **Progress is measured on bytes read from the input**, not on database state: the dump is fed through stdin,
   so the bytes the restore process has consumed are a faithful, format-independent measure for both
   `pg_restore` and `psql`. Unzip measures uncompressed bytes written against the sum of
   `UncompressedSize64`.
2. **Emission policy (one helper, `progressReporter`)**: an INFO line each time progress crosses a new 10 %
   bucket (10, 20 … 100), plus a heartbeat INFO line whenever 30 s pass without any line from the reporter.
   No other throttle. A restore of any size prints at most 10 progress lines plus one heartbeat per 30 s.
3. **Line shape (Odoo cohesion)**: same logger and message as the phase's existing milestone, structured fields
   only, no bars or spinners:
   - progress: `echo.db-restore.restore: restoring data progress=40% read=1.1GB total=2.8GB`
   - heartbeat before EOF: same message, fields `progress=<current>% elapsed=<d>`
   - when the input reaches EOF the reporter emits once `finalizing (indexes and constraints)` and from then on
     heartbeats carry that message with `elapsed=<d>`; it stops when the process exits.
   - unzip: message `extracting archive`, same fields; filestore copy: message `copying filestore`, heartbeat
     only (`elapsed=<d>`), since `docker cp` gives no byte count.
   - `elapsed` is a Go `time.Duration` rounded to the second (`1m30s`). Sizes use decimal units with one decimal
     (`kB`, `MB`, `GB`), no space, so the `key=value` field stays one token.
   - The logger is `echo.<command>.restore` / `.restore` for all of them: in the REPL it is built by `runDB`
     from the `step` argument, so `db-pull --restore` gets `echo.db-pull.restore` with no extra work.
4. **The DEBUG per-object stream of `pg_restore --verbose` stays** as is (the two-layer design in
   [database](../../knowledge/database.md#restore-db-restore-shared-restorebackupfile)): INFO milestones and progress
   mark phases, DEBUG shows activity.
5. **`psql` stays `-q`**: its per-statement echo (`ALTER TABLE`, `COPY n`) would be thousands of lines and adds
   nothing the byte progress does not; progress comes from the reporter.
6. **Only when `opts.Log` is set**: one-shot/REPL wire it; a nil logger means no reporter, no goroutine, and
   `docker.Restore` keeps running without `--verbose` (current behaviour).
7. Scope stops at `restoreBackupFile` and `restoreFromZip`. Checkpoint restore and `db-backup` are untouched
   (`db-backup` has no progress by design).

## Implementation

### `internal/cmd/restore_progress.go` (new)

- `type progressReporter struct` holding `emit func(msg string, fields ...[2]string)`, `msg string`,
  `total int64`, an atomic `read int64`, last bucket, last-emit time, a `done` channel.
- `newProgressReporter(emit, msg, total)`; `Add(n int64)` (called by the counting reader / writer, emits on a new
  10 % bucket); `Start()` launches the heartbeat goroutine (ticker every 1 s, emits when 30 s passed since the
  last line); `EOF()` switches the message to `finalizing (indexes and constraints)` and emits it once;
  `Stop()` closes `done` and waits for the goroutine. `total <= 0` disables the percentage fields and keeps the
  heartbeat.
- `type countingReader struct { r io.Reader; p *progressReporter }`: `Read` calls `p.Add(n)` and `p.EOF()` on
  `io.EOF` (once).
- `formatBytes(int64) string` for the `read`/`total` fields.
- The clock is a package var (`progressNow = time.Now`) and the heartbeat interval a package var, so tests drive
  it without sleeping, per the test-seam rule in [code-standards](../../knowledge/code-standards.md#test-seams).

### `internal/docker/pgdump.go`

- `Restore` and `RestoreSQL` take `in io.Reader` instead of `inPath string` (the caller opens the file and wraps
  it). Nothing else in their behaviour changes; the comment drops the "pipes the contents of inPath" wording.

### `internal/cmd/db.go`

- A small helper `restoreData(ctx, opts, target, path, sql bool)` opens the file, `Stat`s it for `total`, builds
  the reporter with message `restoring data` (emit = `opts.log("INFO", "restore", …, target, fields…)`), wraps
  the file in `countingReader`, calls `docker.Restore` or `docker.RestoreSQL`, and `Stop()`s the reporter on
  every return path. With `opts.Log == nil` it passes the bare file and no reporter.
- `restoreBackupFile` (plain `.backup` path) and both branches of `restoreFromZip` call `restoreData`.
- `unzip(src, dst string, p *progressReporter)`: sums `UncompressedSize64` for the total, wraps each entry's
  writer copy so `p.Add` counts bytes written; `p` may be nil. `restoreFromZip` creates the reporter with message
  `extracting archive` and stops it after `unzip` returns.
- `copyFilestoreToContainer` runs under a heartbeat-only reporter (`total = 0`, message `copying filestore`).

### Docs and changelog

- [database](../../knowledge/database.md#restore-db-restore-shared-restorebackupfile): replace the "Two layers by
  design" sentence with the three layers (INFO milestones, INFO byte progress + heartbeat, DEBUG pg_restore
  stream) and the trap "progress reaches 100 % before indexes are built; the tail is `finalizing`".
- `CHANGELOG.md` `[Unreleased]` → `Changed`: db-restore reports byte progress and a heartbeat during load,
  extraction and filestore copy.

## Dependencies

- none (standard library only: `io`, `sync/atomic`, `time`).

## Verify when done

- [ ] Unit test: `progressReporter` with a fake clock emits exactly one line per 10 % bucket for a 1000-byte
      input read in uneven chunks, and a heartbeat after 30 s of no reads, and none before.
- [ ] Unit test: `countingReader` triggers `EOF()` exactly once and the next heartbeat carries
      `finalizing (indexes and constraints)`.
- [ ] Unit test: `unzip` with a reporter reaches `progress=100%` on a generated zip; with `nil` it behaves as
      before (existing tests unchanged).
- [ ] Unit test: `formatBytes` (`999` → `999B`, `1500000` → `1.5MB`, `2800000000` → `2.8GB`).
- [ ] Live, local stack: `db-restore` of a native Odoo zip (`dump.sql` + filestore) shows extraction progress,
      load progress, `finalizing`, heartbeats and the filestore heartbeat, with no gap over 30 s; same for an Echo
      `.zip` (`dump.backup`). Register the result in the
      [ledger](../../knowledge/operations.md#live-verification-ledger).
- [ ] `--json` mode: every new line is a JSON log object on stdout (they go through `emitOdooLog`).
- [ ] Done gate from [code-standards](../../knowledge/code-standards.md#done-gate): `go build ./...`,
      `go vet ./...`, `go test ./...`, changed files `gofmt`-clean, `CHANGELOG.md` entry.
