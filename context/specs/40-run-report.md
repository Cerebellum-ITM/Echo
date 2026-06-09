# Unit 40: `report` — query the last run's logs by step and level

> Note (later refinement): `report --copy` emits a single Odoo-style line
> (`echo.report: copied N lines to clipboard run=… step=… level=…`) rather
> than a header plus a separate plain confirmation; the no-match and
> copy-failed cases are likewise single WARNING/ERROR lines.

## Goal

After `echo run`, let the operator inspect or copy the previous run's
output filtered by step and log level, across process boundaries:

```
echo report --step=1 --level=warn --copy   # copy step 1's WARNING lines
echo report --min-level=error              # all error+critical lines, all steps
report --step=2                            # (in the REPL) step 2's full output
```

`report` works without remembering any flag on the `run`: every `echo run`
persists a structured record of its steps and lines, and `report` reads it.

## Design

`report` is a separate invocation with no memory of the run, so the run
must persist a queryable record. Each `echo run` writes
`~/.config/echo/run-logs/last-run.json` — the recipe label plus, per step,
the command, its status, and its captured lines tagged with a log level.
This reuses what's already there: the per-command `lastOutputBuffer`
(every printed line, with its `Kind`), the step boundaries from the recipe
runner (Unit 37), and the run-logs dir (Unit 34). `report` loads that
record and filters.

**Level per line.** Each stored line's level token (`DEBUG` / `INFO` /
`WARNING` / `ERROR` / `CRITICAL`, or empty) is parsed from the line text at
capture time via the existing prefixes (`odooLogPrefix`,
`loguruLogPrefix`, `parseLooseSeverity`) — so `ERROR` and `CRITICAL` stay
distinct. When the text carries no token, it falls back to the line's
classified `Kind` (`levelFromKind`: faint→DEBUG, info→INFO, warn→WARNING,
err→ERROR) so Echo's own leveled lines and inherited traceback frames still
get a level (Kind can't distinguish CRITICAL, so an explicit token always
wins). Lines that resolve to no level are excluded by any level filter,
included when there's no filter.

**Step isolation.** After snapshotting each step, the runner calls
`sess.lastOutput.Reset()` — meta commands (help/clear) don't reset the
buffer themselves, so without this a meta step would re-capture the prior
step's lines. (Safe: the recipe session is discarded after the run.)

**Filtering.**
- `--step=<N>` (1-based) selects one step; omitted → all steps.
- `--level=<lvl>` exact match (only that level).
- `--min-level=<lvl>` threshold (that level and more severe), rank
  `DEBUG < INFO < WARNING < ERROR < CRITICAL`.
- `--level` and `--min-level` are mutually exclusive. `lvl` accepts
  `debug|info|warn|warning|error|critical` (`warn`≡`warning`).
- `--copy` puts the matched lines on the clipboard (reusing
  `internal/clipboard`, OSC 52-aware); without it, the lines print to
  stdout (pipeable), colored by level in a TTY.

**Surfaces.** `report` is a normal command: one-shot (`echo report …`) and
in the REPL (`report …`), so it joins `Registry` / `dispatchNames` /
`helpSections` / `commandFlags`.

**Scope notes (v1).** One global `last-run.json` (the last run, period —
not keyed per project); a later `--run=<id>` can select older logs.
Loose-severity lines reformatted via `emitOdooLog` (Unit 36) bypass
`lastOutputBuffer`, so they aren't captured in v1 — the streamed Odoo log
lines (the bulk of what you'd grep) are.

## Implementation

### Persisted record (`internal/config/run_report.go`, new)

JSON (nested arrays — cleaner than TOML here) at
`RunLogsDir()/last-run.json`, written with the existing `writeAtomic`:

```go
type ReportLine struct {
    Level string `json:"level"` // DEBUG/INFO/WARNING/ERROR/CRITICAL or ""
    Text  string `json:"text"`
}
type StepReport struct {
    Index  int          `json:"index"`  // 1-based
    Cmd    string       `json:"cmd"`
    Status string       `json:"status"` // ok/failed/cancelled/skipped
    Lines  []ReportLine `json:"lines"`
}
type RunReport struct {
    Recipe string       `json:"recipe"`
    Steps  []StepReport `json:"steps"`
}

func SaveRunReport(r RunReport) error      // MkdirAll + writeAtomic(json)
func LoadRunReport() (RunReport, bool)      // (zero,false) if missing/bad
```

### Capture during the run (`internal/repl/recipe.go`)

`RunRecipe`'s `runStep` closure already runs each step via
`dispatchParsed`; after it returns, `sess.lastOutput` holds that step's
lines. Snapshot them into a `config.RunReport` accumulator (step index via
a counter — steps run in order, so it matches), then persist after the run:

```go
report := config.RunReport{Recipe: recipeLabel(path)}
stepNum := 0
runStep := func(name string, sargs []string) stepOutcome {
    stepNum++
    start := time.Now()
    sess.dispatchParsed(ctx, name, sargs)
    out := stepOutcome{code: sess.exitCode, errors: sess.lastErrors,
        warnings: sess.lastWarnings, duration: time.Since(start)}
    lines := sess.lastOutput.Filtered(nil)
    rls := make([]config.ReportLine, 0, len(lines))
    for _, l := range lines {
        rls = append(rls, config.ReportLine{Level: lineLevel(l.Text), Text: l.Text})
    }
    report.Steps = append(report.Steps, config.StepReport{
        Index: stepNum, Cmd: strings.TrimSpace(name + " " + strings.Join(sargs, " ")),
        Status: stepStatus(out.code), Lines: rls,
    })
    return out
}
// … runRecipeSteps(…) …
_ = config.SaveRunReport(report) // best-effort, never fails the run
```

`lineLevel` (new, `internal/repl/loglevel.go`) returns a line's level token:

```go
func lineLevel(text string) string {
    if m := odooLogPrefix.FindStringSubmatch(text); m != nil {
        return m[1]
    }
    if m := loguruLogPrefix.FindStringSubmatch(text); m != nil {
        return m[1]
    }
    if ll, ok := parseLooseSeverity(text); ok {
        return ll.level
    }
    return ""
}
```

### `report` command (`internal/repl/report.go`, new)

```go
// runReport implements `report [--step=N] [--level=lvl | --min-level=lvl]
// [--copy]`: load the last run's record and print or copy the matching
// lines, filtered by step and level.
func (sess *session) runReport(args []string)
```

- Parse flags (`parseReportArgs`): `--step=N`, `--level=lvl`,
  `--min-level=lvl` (mutually exclusive), `--copy`. Validate `lvl`
  (normalize `warn`→`WARNING`) against the level set; reject unknown.
- `config.LoadRunReport()`; if absent → `report: no run yet — run a recipe
  first` (warn, exit 2 in one-shot).
- Select steps (`--step` → that one; out of range → clear error) and
  filter each step's lines: keep when no level filter, or
  `--level` exact, or `--min-level` rank ≥.
- Emit an Odoo-style header via `emitOdooLog("INFO", "echo.report", …)`
  with `run` / `step` / `level` / `lines` fields, then:
  - `--copy`: join matched line texts plain, `clipboard.WriteAll`, report
    `copied N line(s)`; if nothing matched, say so and don't copy.
  - else: `sess.print` each matched line with `Kind` derived from its
    level (so warn=yellow, err=red) — reuse a small `kindFromLevel`.
- Sets `sess.exitCode` (ok / exit 2 on usage / empty).

Level rank + parsing helpers live in `report.go`.

### Wiring

- `internal/repl/commands.go`: add `"report"` to `Registry`; add
  `"report": {"--step", "--level", "--min-level", "--copy"}` to
  `commandFlags`.
- `internal/repl/repl.go`: `dispatchNames` += `"report"`; `dispatch`
  switch `case "report": sess.runReport(args)`; add a `helpSections`
  entry (new short "Reporting" group or under the output commands) so the
  Registry↔help cross-check stays green. `report` reads a global file and
  needs no project state, but goes through the normal dispatch (you're in
  the project you just ran in).
- `IsScriptCommand` picks it up automatically (it's in `dispatchNames`),
  so `echo report …` works one-shot.

### Tests

- `internal/config/run_report_test.go`: save→load round-trips steps/lines;
  missing file → `(zero,false)`.
- `internal/repl/loglevel_test.go` (or report_test): `lineLevel` parses
  Odoo / loguru / loose / none.
- `internal/repl/report_test.go`: `parseReportArgs` table (`--step`,
  `--level`, `--min-level`, both-levels→error, bad level→error, `--copy`);
  a pure filter helper `filterReport(report, step, exact, min)` returns the
  right lines for exact vs threshold vs all, and step selection / out of
  range.
- Keep `registry_test.go` / `commandhl_test.go` cross-checks green (new
  command + flags must be consistent across Registry/help/dispatch).

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `report` command + every run
  persisting `last-run.json`.
- `context/progress-tracker.md` → mark Unit 40 done.
- `context/specs/00-build-plan.md` → add the Unit 40 row.

## Dependencies

None new. Reuses `lastOutputBuffer`, the level regexes, `internal/clipboard`,
`config.writeAtomic` / `RunLogsDir`, `encoding/json` (stdlib).

## Verify when done

- [ ] `echo run …` writes `~/.config/echo/run-logs/last-run.json` (always,
      no flag needed).
- [ ] `echo report --step=1 --level=warn --copy` copies exactly step 1's
      WARNING lines; reports the count; copies nothing when none match.
- [ ] `--min-level=error` returns ERROR and CRITICAL (not WARNING);
      `--level=error` returns ERROR only (not CRITICAL).
- [ ] `--step` out of range and an unknown `--level` value error clearly;
      `--level` + `--min-level` together is rejected.
- [ ] No run yet → a clear `no run yet` message (exit 2 one-shot).
- [ ] Without `--copy`, matched lines print to stdout, colored by level in
      a TTY; works both as `echo report …` and `report …` in the REPL.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
