# Unit 87: `watch` — follow remote logs between cycles (monitor mode)

## Goal

`watch` becomes a true monitor: while idle between polls it **follows the
remote Odoo container's logs live** (the `logs --remote` stream), and when
the branch advances it pauses the follow, runs the existing push+deploy
cycle untouched, then resumes the follow. Default behavior; a new
`--no-logs` flag restores today's silent waiting (tmux/CI where only the
cycle frames matter).

## Design

**Follow is decoration; polling is the job.** The ticker loop from Unit 84
stays the single source of truth — the log follower is a side goroutine
that must never block, delay, or kill a cycle. Any follower failure
degrades to WARNING + retry; the watcher itself only dies for the same
reasons it does today (branch gone, unrecoverable setup).

**One SSH stream, same rendering as `logs --remote`.** The follower runs
`compose logs --no-log-prefix -f --tail <N> <odooContainer>` through
`remoteComposeCmd` + `runSSHStream` against the already-resolved
`remoteShellContext` — no second target resolution. Lines flow through the
same `opts.StreamOut` the push/deploy remote lines already use, so the
REPL's `logColorer`/`emitStreamLine` recolors Odoo log lines for free. No
new rendering code.

**Pause/resume protocol — output must never interleave.** The follower
owns a derived context and a `done` channel:

1. Startup (after the `watching branch` frame): start the follower with
   `--tail 20` (a screenful of context, mirroring the spirit of `logs`).
   Frame it: `echo.watch.logs: following logs service=<odoo>`.
2. On a detected advance: `cancel()` the follower ctx, **block on
   `<-done`** so the SSH process is dead and its scanner drained, log
   `echo.watch.logs: follow paused — running cycle`, then run
   `watchCycle` exactly as today.
3. After the cycle (success *or* failure): restart the follower with
   `--tail 0` — the deploy output was just printed; replaying pre-cycle
   lines would duplicate what the user saw. The deploy's `stop`/`up -d`
   recreates containers, which is precisely why each resume opens a
   **fresh** `compose logs` stream instead of trying to survive the
   restart.
4. `Ctrl+C`: cancel follower, `<-done`, then the existing
   `watch stopped cycles=N deployed=M` summary. The summary must be the
   last thing printed.

**Unexpected stream death = retry, not crash.** If `runSSHStream` returns
while the follower ctx is still alive (network blip, container briefly
gone), log WARNING `log stream ended — retrying` and re-open after
sleeping one poll interval, with `--tail 0`. When the ctx is cancelled
(pause or shutdown) the return is silent — distinguish via `ctx.Err()`.

**`--no-logs`.** Parsed like the other watch switches; skips all of the
above — the loop is byte-for-byte today's behavior. No TTY gating either
way: streaming logs headless is fine, and `--no-logs` exists for whoever
doesn't want them.

## Implementation

### `internal/cmd/watch.go`

- `watchArgs` gains `noLogs bool`; `parseWatchArgs` accepts `--no-logs`.
- Package-level seam for tests:
  `var watchLogStream = runSSHStream` (the follower calls the seam, tests
  substitute a scripted stream).
- New `watchFollower` helper owning the lifecycle:
  - `startWatchLogs(ctx, opts, rsc, tail string) (stop func())` — spawns
    the goroutine, returns a `stop` that cancels and waits on `done`.
    Internally loops: build the remote cmd
    (`remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "logs",
    "--no-log-prefix", "-f", "--tail", tail, rsc.target.odooContainer)`),
    call `watchLogStream`; on return with live ctx → WARNING + sleep
    `interval` + retry with `--tail 0`; on cancelled ctx → close `done`
    and exit.
  - Frames under sub-logger `logs`: `following logs`, `follow paused —
    running cycle`, WARNING `log stream ended — retrying`.
- `RunWatch` wiring (only when `!p.noLogs`):
  - after the `watching branch` frame: `stop := startWatchLogs(…,
    "20")`;
  - in the advance branch: `stop()` **before** `watchCycle`, restart
    with `"0"` after it (regardless of cycle error — the ERROR frame
    already printed);
  - in the `ctx.Done()` branch: `stop()` before the summary frame.
  - With `--no-logs`, none of these calls happen (nil-safe or guarded).

### `internal/repl/repl.go` / `internal/repl/commands.go`

- Help row under `watch`: `{"  --no-logs", "Don't follow remote logs
  between cycles (silent wait)"}`.
- `commandFlags["watch"]` gains `--no-logs`.

### `README.md` / `CHANGELOG.md`

- README `watch` table row for `--no-logs` + one prose sentence in the
  Sync & compare section describing monitor mode (follow → cycle →
  follow).
- `CHANGELOG.md` `[Unreleased]` → `### Changed` entry (in Spanish, house
  style), same commit as the code.

### Tests (`internal/cmd/watch_test.go`)

- `parseWatchArgs`: `--no-logs` parsed; absent → false.
- Follower lifecycle against the `watchLogStream` seam (scripted stream
  that records `(tail, ctx)` calls and blocks until cancelled):
  - `stop()` cancels and returns only after the stream func exited
    (no goroutine leak, ordering guaranteed);
  - unexpected return with live ctx retries with `--tail 0` after the
    interval (use a short interval in the test);
  - cancelled ctx → no retry, `done` closes.
- Pause/resume ordering: a fake cycle asserts the stream was stopped
  before it ran and restarted after (record call order in a slice).

## Dependencies

None new — Unit 84's watch loop, the shared SSH transport
(`runSSHStream`), and the `compose logs` form Unit 76 already uses for
`logs --remote`.

## Verify when done

- [ ] `watch dev --remote` prints the `watching branch` frame, then
      `following logs` and a live, level-colored remote log stream while
      idle.
- [ ] A commit to the branch pauses the follow (`follow paused` frame),
      runs the full push+deploy cycle with **no log lines interleaved**
      into the cycle output, and resumes the follow after the cycle's
      closing frame with no replayed/duplicated lines.
- [ ] A failing cycle still resumes the follow after its ERROR frame.
- [ ] Killing the remote container briefly (or dropping the SSH stream)
      logs the retry WARNING and the follow recovers on its own; the
      poller never misses a commit meanwhile.
- [ ] `watch dev --remote --no-logs` behaves exactly like today —
      silent between cycles.
- [ ] `Ctrl+C` during an active follow closes cleanly; the
      `watch stopped cycles=N` summary is the last output; exit 0.
- [ ] `help`/registry/autocomplete show `--no-logs`; README and
      CHANGELOG updated in the same commit.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` pass; no goroutine leak in the follower tests.
