# Unit 41: per-step `--silent` in `echo run`

## Goal

Let a recipe step suppress its own output — on screen **and** in the
`--log` transcript — by appending `--silent` (drop everything) or
`--silent=<lvl>` (drop that level and below, keep more severe):

```
update sale --silent          # this step's output is hidden
stop --silent=info            # hide DEBUG/INFO, still show WARNING/ERROR
```

The runner's `step N/M →` line and the per-step recap stay visible, so you
still see the step ran (and its `ok`/`failed`/`warnings`). Silenced lines
are still captured for `report`, so `report --step=N` can pull them up
later. `--silent` is recipe-only — it's intercepted by the `run` runner and
never reaches the underlying command, so it works for any non-interactive
step (`stop`, `up`, `update`, `db-backup`, …).

## Design

Every step's output flows through `sess.print` (streamed command lines →
stdout + the `--log` tee) and `emitOdooLog` (the command's start/finalize
lines, reformatted compose/loose lines → stdout + tee). Suppression gates
both at the moment of writing, keyed by the line's level rank, so it's
command-agnostic — the runner toggles it around each step's
`dispatchParsed`.

- A package switch `suppressLevel` (-1 = inactive) mirrors the existing
  `runLogSink` pattern (a run is sequential). A line of level rank `r` is
  dropped when `suppressLevel >= 0 && r <= suppressLevel`. Ranks:
  `""`(plain)=0 < DEBUG=1 < INFO=2 < WARNING=3 < ERROR=4 < CRITICAL=5.
  Bare `--silent` → `silentAll` (99, drops all incl. plain + CRITICAL);
  `--silent=<lvl>` → `levelRank[lvl]` (drops that level and below).
- `sess.print` still calls `lastOutput.Add` **before** the suppression
  check, so `report` captures silenced lines (suppression is about live
  noise, not data loss). `emitOdooLog` returns early when suppressed (it
  never fed `lastOutput`, so no change there).
- The runner toggles `suppressLevel` only around `dispatchParsed`; its own
  `step N/M →` marker and the recap/summary (`sess.runLog`) run with
  suppression inactive, so they're always shown — including in `--log`.

Because the gate is level-keyed, `--silent=warn` still lets `ERROR`/
`CRITICAL` through — a failure in a "quiet" step is never hidden.

## Implementation

### `internal/repl/silence.go` (new)

```go
var suppressLevel = -1
const silentAll = 99

func outputSuppressed(level string) bool {
    return suppressLevel >= 0 && levelRank[level] <= suppressLevel
}

// stripSilent removes --silent / --silent=<lvl> from a step's args,
// returning the cleaned args, the suppression level (-1/silentAll/rank),
// a display label ("all"/level name/""), and any invalid level value.
func stripSilent(args []string) (clean []string, suppress int, label, bad string)
```

`levelRank` and `normalizeLevel` are reused from `report.go` (Unit 40).

### Gating the writers

- `sess.print` (`repl.go`): move `lastOutput.Add(l)` to the top, then
  `if outputSuppressed(levelFromKind(l.Kind)) { return }` before the
  stdout `Println` + `teeRunLog`.
- `emitOdooLog` (`logemit.go`): `if outputSuppressed(level) { return }` at
  the top.

### Runner (`internal/repl/recipe.go`)

- `runStep` gains a `suppress int` parameter; the `RunRecipe` closure sets
  `suppressLevel = suppress` before `dispatchParsed` and resets to `-1`
  after (capture of `lastOutput` happens after, unaffected).
- `runRecipeSteps`'s `runStep` type updates accordingly. Per step, it calls
  `stripSilent(fields[1:])`, warns on a bad level (`log("WARNING", …)`,
  runs without suppression), passes the cleaned args + level to `runStep`,
  and records the `silent` label on the result.
- The recap (`stepFields`) gains the `silent` label → a `silent=<all|lvl>`
  field on a silenced step's recap line, so it's clear the output was
  hidden (and `report` is the way back to it).

### Help (`repl.go` Scripting footer)

```
  <step> --silent[=lvl]   Silence a step's output (screen+log); =lvl keeps that level and above
```

### Tests

- `recipe_test.go`: `TestStripSilent` (none / bare / `=info` / `=warn` /
  bad level, and clean-arg extraction); `TestOutputSuppressed` (inactive →
  nothing; `silentAll` → all incl. plain + CRITICAL; `=info` → DEBUG/INFO/
  plain suppressed, WARNING+ shown). Update the existing `runStep` stubs to
  the new 3-arg signature (suppress ignored); exit-code tests unchanged.

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: per-step `--silent` /
  `--silent=<lvl>` in `echo run`.
- `context/progress-tracker.md` → Unit 41 done.
- `context/specs/00-build-plan.md` → Unit 41 row.

## Dependencies

None new. Reuses `levelRank` / `normalizeLevel` / `levelFromKind`,
`runLogSink`/`teeRunLog`, and the recipe runner.

## Verify when done

- [ ] `<step> --silent` hides that step's output on screen and in `--log`;
      the `step N →` and recap lines stay (recap shows `silent=all`).
- [ ] `<step> --silent=info` hides DEBUG/INFO/plain but shows WARNING+;
      `--silent=warn` still lets ERROR/CRITICAL through.
- [ ] A silenced step's lines are still in `last-run.json` — `report
      --step=N` shows them.
- [ ] `--silent` works on any non-interactive step (`stop`, `up`,
      `update`, …) and never reaches the command (not treated as an arg).
- [ ] An invalid `--silent=<bad>` warns and runs the step un-silenced.
- [ ] Exit codes and the run summary are unchanged.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
