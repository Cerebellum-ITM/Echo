# Unit 37: recipe run summary

## Goal

At the end of `echo run <file>`, emit a per-step summary so the operator
sees, for each step, whether it completed, how many warnings it raised,
and how long it took — plus a totals line. Today the runner only logs a
`step N/M → <cmd>` line before each step and a terse `N steps completed` /
`finished with errors` at the end; there's no per-step outcome recap. The
summary is emitted as `echo.run` Odoo-style log lines (no ASCII tables /
generic decorations), so it sits in the same log stream as everything else
and is captured by `--log` for free.

## Design

`RunRecipe` runs each step through `dispatchParsed` and reads the step's
exit code from `session.exitCode`
([recipe.go](../../internal/repl/recipe.go),
[script.go](../../internal/repl/script.go)). The per-step warning/error
counts already exist as the `stats` (`runStats`) local to each `run*`
handler, but they aren't visible to the runner. This unit surfaces them on
the session — the same way `exitCode` already is — so the runner can record
a richer outcome per step without changing how commands execute.

**Per-step outcome.** The runner's `runStep` closure measures wall time
around `dispatchParsed` and returns a `stepOutcome{code, errors, warnings,
duration}`. The pure `runRecipeSteps` accumulates these and, after the run
loop, emits the recap. Keeping the clock in the closure leaves
`runRecipeSteps` clock-free and unit-testable, exactly as it is today.

**Status per step:**

| Condition                              | status      | recap level |
| -------------------------------------- | ----------- | ----------- |
| exit 0                                 | `ok`        | INFO        |
| exit 3 (cancelled)                     | `cancelled` | WARNING     |
| any other non-zero                     | `failed`    | ERROR       |
| not reached (fail-fast stopped before) | `skipped`   | WARNING     |

Under fail-fast, the step that fails is the last one executed; every step
after it is recorded as `skipped` so the recap accounts for all N steps.
Under `--continue-on-error`, all steps run and none are skipped.

**Counting source.** Warnings/errors are the same counts the per-command
finalize line already shows (`runStats` via `successLog` / `finalize` /
`copyFailureLog` / `commandFailureLog`), so the recap is consistent with
what each step printed. Loose-severity warnings (Unit 36) are included,
loose errors aren't counted — unchanged here.

**Return codes are preserved.** The existing exit-code contract
(all-pass → 0, fail-fast → the failing step's code, continue-on-error →
`exitError` if any failed) stays byte-for-byte; only the logging around it
is enriched. The current `recipe_test.go` exit-code tests must stay green
(with the `runStep` signature updated to return `stepOutcome`).

## Implementation

### Surface per-command counts on the session (`internal/repl`)

- `session` struct ([repl.go](../../internal/repl/repl.go)): add
  `lastErrors, lastWarnings int` next to `exitCode`, with a comment that
  they mirror the last dispatched command's `runStats`, read by
  `RunRecipe` for the per-step summary.
- `dispatchParsed`: reset both to 0 at the top, alongside
  `sess.exitCode = exitOK`.
- Set them in the four terminal helpers that carry counts:
  - `successLog` (copylast.go): `lastWarnings = warnCount` (errors stay 0).
  - `finalize` (repl.go): `lastErrors = errorCount`, `lastWarnings = warnCount`.
  - `copyFailureLog` (copylast.go): `lastErrors = errCount`, `lastWarnings = warnCount`.
  - `commandFailureLog` (copylast.go): same.
  - The remaining terminal paths (shell/connect/readonly/cancelled) carry
    genuinely-zero counts, so the dispatch-time reset already covers them.

### `stepOutcome` + runner recap (`internal/repl/recipe.go`)

```go
// stepOutcome is one recipe step's result, captured by the runStep
// closure after dispatchParsed: the exit code plus the command's
// runStats (surfaced on the session) and the wall-clock duration.
type stepOutcome struct {
    code     int
    errors   int
    warnings int
    duration time.Duration
}
```

`RunRecipe`'s `runStep` becomes:

```go
runStep := func(name string, sargs []string) stepOutcome {
    start := time.Now()
    sess.dispatchParsed(ctx, name, sargs)
    return stepOutcome{
        code:     sess.exitCode,
        errors:   sess.lastErrors,
        warnings: sess.lastWarnings,
        duration: time.Since(start),
    }
}
```

`runRecipeSteps` signature changes its `runStep` return type to
`stepOutcome` and, after the run loop, emits the recap. Sketch:

```go
func runRecipeSteps(steps []string, continueOnError bool,
    runStep func(name string, args []string) stepOutcome,
    log func(level, msg string, fields ...logField)) int {

    total := len(steps)
    type result struct {
        step   string
        out    stepOutcome
        status string
    }
    var results []result
    failed, skipped := 0, 0
    lastCode := exitOK
    stopped := -1

    for i, step := range steps {
        log("INFO", fmt.Sprintf("step %d/%d → %s", i+1, total, step))
        fields := strings.Fields(step)
        out := runStep(fields[0], fields[1:])
        st := stepStatus(out.code)
        results = append(results, result{step, out, st})
        if out.code != exitOK {
            lastCode = out.code
            failed++
            if !continueOnError {
                stopped = i
                break
            }
        }
    }
    if stopped >= 0 {
        for j := stopped + 1; j < total; j++ {
            results = append(results, result{steps[j], stepOutcome{}, "skipped"})
            skipped++
        }
    }

    // Per-step recap.
    var warnTot int
    var durTot time.Duration
    for i, r := range results {
        warnTot += r.out.warnings
        durTot += r.out.duration
        log(recapLevel(r.status),
            fmt.Sprintf("step %d/%d %s", i+1, total, r.status),
            stepFields(r.step, r.out, r.status)...)
    }

    // Totals.
    okN := total - failed - skipped
    totFields := []logField{{"steps", strconv.Itoa(total)}, {"ok", strconv.Itoa(okN)}}
    if failed > 0 {
        totFields = append(totFields, logField{"failed", strconv.Itoa(failed)})
    }
    if skipped > 0 {
        totFields = append(totFields, logField{"skipped", strconv.Itoa(skipped)})
    }
    // Always report the error/warning totals so the summary states the
    // counts even when they're zero.
    totFields = append(totFields,
        logField{"errors", strconv.Itoa(errTot)},
        logField{"warnings", strconv.Itoa(warnTot)})
    totFields = append(totFields, logField{"took", fmtDur(durTot)})
    totLevel := "INFO"
    if failed > 0 {
        totLevel = "ERROR"
    }
    log(totLevel, "run summary", totFields...)

    if stopped >= 0 {
        return lastCode
    }
    if failed > 0 {
        return exitError
    }
    return exitOK
}
```

Helpers (same file):

```go
func stepStatus(code int) string {
    switch code {
    case exitOK:
        return "ok"
    case exitCancelled:
        return "cancelled"
    default:
        return "failed"
    }
}

func recapLevel(status string) string {
    switch status {
    case "failed":
        return "ERROR"
    case "cancelled", "skipped":
        return "WARNING"
    default:
        return "INFO"
    }
}

// stepFields builds the recap fields for one step: always the cmd; the
// warning count when non-zero; on failure the error count + exit code;
// and the duration for any step that actually ran.
func stepFields(step string, out stepOutcome, status string) []logField {
    fields := []logField{{"cmd", step}}
    if out.warnings > 0 {
        fields = append(fields, logField{"warnings", strconv.Itoa(out.warnings)})
    }
    if status == "failed" {
        if out.errors > 0 {
            fields = append(fields, logField{"errors", strconv.Itoa(out.errors)})
        }
        fields = append(fields, logField{"exit", strconv.Itoa(out.code)})
    }
    if status != "skipped" {
        fields = append(fields, logField{"took", fmtDur(out.duration)})
    }
    return fields
}

// fmtDur renders a step/total duration compactly (e.g. "1.23s", "180ms").
func fmtDur(d time.Duration) string {
    return d.Round(time.Millisecond).String()
}
```

### Tests (`internal/repl/recipe_test.go`)

- Update the three existing `runRecipeSteps` stubs to return
  `stepOutcome` (the exit-code assertions stay identical).
- Add a test that captures the `log` calls into a slice and asserts the
  recap: e.g. a 3-step recipe with one failure under fail-fast emits a
  `step 2/3 failed` ERROR recap line, a `step 3/3 skipped` line, and a
  final `run summary` line carrying `failed=1` and `skipped=1`. A warning
  on an ok step shows `warnings=N` on its recap line and in the totals.
- The duration field is present but value-agnostic in tests (the stub
  returns `duration: 0`, so `took=0s`).

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `echo run <file>` now prints a
  per-step summary (status / warnings / duration) and a totals line.
- `context/progress-tracker.md` → mark Unit 37 done.
- `context/specs/00-build-plan.md` → add the Unit 37 row.

## Dependencies

None new. Reuses `emitOdooLog` via `sess.runLog`, `runStats`, and the
`time` import already present in recipe.go.

## Verify when done

- [ ] A clean 3-step recipe ends with three `step i/3 ok` recap lines
      (each with `took=…`) and a `run summary steps=3 ok=3 errors=0
      warnings=0 took=…` line — `errors`/`warnings` are always present.
- [ ] A step that raised warnings shows `warnings=N` on its recap line and
      contributes to the totals `warnings=…`.
- [ ] Under fail-fast, the failing step shows `failed exit=<code>` and the
      unrun steps show as `skipped`; the totals carry `failed`/`skipped`
      and the process exit code is unchanged (the failing step's code).
- [ ] Under `--continue-on-error`, every step has a recap line and the
      totals reflect all failures; exit is `exitError` if any failed.
- [ ] With `--log`, the recap and totals lines are captured in the
      transcript (they go through `sess.runLog`).
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
