# Unit 34: `echo run … --log` recipe transcript

## Goal

Let `echo run <file> --log` capture the whole run — every step's streamed
output plus the per-step result and the final verdict — into a plain-text
`.log` file, so an update routine leaves an auditable record (Odoo
warnings/errors included). The log is opt-in via `--log`: bare `--log`
writes to a timestamped file under `~/.config/echo/run-logs/`; `--log=<path>`
writes to an explicit path. Without the flag, `echo run` behaves exactly
as today (no file written).

## Design

Two decisions drive the design:

- **Opt-in, never a surprise file.** Logging only happens with `--log`.
  This keeps the architecture invariant intact (Echo writes nothing to
  the project repo by default) — and the default location is the config
  dir, not the project, so even `--log` honors it. An explicit
  `--log=<path>` is the escape hatch when the user wants it elsewhere.
- **Full transcript, plain.** The log mirrors what scrolled past on
  screen but with ANSI styling stripped: the streamed Odoo output of each
  step (already captured as plain `Line.Text`) plus the `echo.run` /
  `startLog` / `finalize` lines (rendered through `plainOdooLog`). A
  short header and the same fail-fast/summary lines bracket it.

Capture is done by teeing: while a `--log` run is active, `sess.print`
and `emitOdooLog` additionally write a plain copy of each line to a
package-level sink. The sink is set only for the duration of `RunRecipe`
with `--log`; in the REPL and in plain `echo run` it stays nil and the
tee is skipped. The run is sequential (no concurrent steps), and
`sess.print` is already treated as single-threaded per command (the
existing un-locked `lastOutput.Add`), so the sink needs no locking beyond
what already holds.

## Implementation

### Sink plumbing (`internal/repl/logemit.go`, `repl.go`)

- Add a package-level `var runLogSink io.Writer` (nil = no capture).
- `sess.print`: after `fmt.Println(text)`, if `runLogSink != nil`, write
  `l.Text + "\n"` to it (plain — `Line.Text` never carries ANSI).
- `emitOdooLog`: after the `os.Stdout.WriteString`, if `runLogSink != nil`,
  write the plain rendering **including fields**. Factor a
  `plainOdooLogFields(level, logger, msg, fields, db) string` (extends the
  existing `plainOdooLog` with the ` key=val` tail using `quoteIfNeeded`)
  and reuse it for the sink.

### Config path (`internal/config/paths.go`)

```go
// RunLogsDir is where `echo run --log` writes recipe transcripts:
// ~/.config/echo/run-logs.
func RunLogsDir() (string, error) {
    root, err := configRoot()
    if err != nil {
        return "", err
    }
    return filepath.Join(root, "run-logs"), nil
}
```

### Arg parsing (`internal/repl/recipe.go`)

Extend `parseRecipeArgs` to also return the log destination. Support
`--log` (bare → default path) and `--log=<path>` (explicit). Avoid the
space form `--log <path>` so it can't be confused with the recipe
positional.

```go
func parseRecipeArgs(args []string) (path string, continueOnError bool, logDest string, logEnabled bool, err error)
```

- `--continue-on-error` → as today.
- `--log` → `logEnabled = true`, `logDest = ""` (means default path).
- `--log=<p>` → `logEnabled = true`, `logDest = p`.
- unknown `--flag` → error (unchanged).
- first non-flag (or `-`) → recipe path (unchanged).

### Wiring the sink in `RunRecipe`

When `logEnabled`:

1. Resolve the destination: explicit `logDest`, else
   `<RunLogsDir>/<timestamp>-<name>.log` where `name` is the recipe
   basename without extension (`stdin` when reading from `-`/stdin) and
   `timestamp` is `time.Now().Format("20060102-150405")`. `os.MkdirAll`
   the parent.
2. `os.Create` the file; on error, emit an `echo.run` WARNING and
   continue **without** logging (a log failure must not abort the run).
3. Set `runLogSink = f` (wrap in a `bufio.Writer`); `defer` flush + close
   + `runLogSink = nil`.
4. Write a header line to the file (e.g.
   `# echo run <name> — <RFC3339 timestamp>`).
5. Run the steps as today (they tee automatically through the patched
   `print`/`emitOdooLog`).
6. After the summary, emit one on-screen `echo.run` INFO line naming the
   file: `log written path=<dest>` (this line is itself teed, so the log
   ends with its own path — harmless and useful).

`time` and `bufio` imports are added to `recipe.go`.

### Help

- Update the "Scripting" footer in `runHelp` (`repl.go`) to list
  `--log[=<path>]` under `echo run`.

### Tests (`internal/repl/recipe_test.go`)

- `parseRecipeArgs`: `--log` → `logEnabled=true, logDest=""`;
  `--log=/tmp/x.log` → `logEnabled=true, logDest="/tmp/x.log"`; combined
  with `--continue-on-error` and a recipe path; order-independent.
- `runRecipeSteps` is unchanged, so its tests stay; add a focused test
  that the tee writes plain lines: set `runLogSink` to a `bytes.Buffer`,
  call `emitOdooLog`/`sess.print` (or a tiny helper), and assert the
  buffer contains the plain text without ANSI escapes. (Keep this at the
  sink level to avoid needing a real recipe run.)

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `echo run … --log[=<path>]`.
- `context/architecture.md`: note `run-logs/` under the config dir in the
  Storage Model.
- `context/progress-tracker.md` → mark Unit 34 done with a session note.

## Dependencies

None new. Standard library `bufio`, `io`, `time`, `os`, plus the existing
config dir helper.

## Verify when done

- [ ] `echo run r.echo` with no `--log` writes no file (behavior
      unchanged).
- [ ] `echo run r.echo --log` creates
      `~/.config/echo/run-logs/<ts>-r.log` containing the full transcript
      (each step's output) plus the `echo.run` step/summary lines, all in
      plain text with no ANSI escape codes.
- [ ] `echo run r.echo --log=/tmp/out.log` writes to that exact path.
- [ ] `echo run - --log` (stdin recipe) names the file `…-stdin.log`.
- [ ] On fail-fast, the log contains the steps that ran and the
      `stopped at step N/total` line; the run's exit code is unchanged by
      logging.
- [ ] A log-file creation error (e.g. unwritable path) warns but the run
      still executes and exits with the normal code.
- [ ] The final on-screen line reports the log path.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
