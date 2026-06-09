# Unit 32: Recipe runner (`echo run <file>`)

## Goal

Let a whole instance-update routine live in a single file instead of N
separate `echo` invocations. `echo run <file>` reads a recipe — one Echo
command per line — and executes them in order, streaming each command's
Odoo-style output, and **stops at the first command that exits non-zero**
(fail-fast). Reading the recipe from stdin (`echo run -`, or `echo run`
with piped input) is also supported, so a recipe can be generated on the
fly. Builds directly on the Unit 31 one-shot dispatch and exit codes.

## Design

A recipe is a plain text file, e.g. `update.echo`:

```
# Update the instance (comments and blank lines are ignored)
stop
db-backup
up
update ventas contabilidad
test ventas
restart
```

Execution rules:

- Lines are run top-to-bottom through the **same** one-shot path Unit 31
  added (`repl.RunOnce`) — each line is parsed like a REPL line
  (`strings.Fields`), so identical rendering and the `startLog`/`finalize`
  frame apply per step.
- Blank lines and lines starting with `#` are skipped.
- **Fail-fast:** the first step whose exit code is non-zero aborts the
  run; `echo run` exits with that step's code. Remaining steps are not
  executed. A final summary line reports how far it got
  (`echo.run: stopped at step N/<total>` on failure,
  `echo.run: <total> steps completed` on success), emitted in Echo's
  Odoo log style.
- A `--continue-on-error` flag runs every step regardless and exits `1`
  if any step failed (useful for best-effort teardown recipes).
- The whole run is non-interactive by construction: because steps go
  through `RunOnce`, the Unit 31 TTY guard already makes any step that
  would prompt fail closed. A recipe must be explicit (pass module names,
  `--force`, etc.) — the same contract as chaining `echo` calls in bash,
  but in one file and one process.

Recipes are **not** project config and live wherever the user keeps them
(repo, `~/`, CI checkout); Echo does not manage or discover them.

## Implementation

### `main.go` — route `run` before the generic one-shot

`run` is a meta-command that orchestrates other commands, so it is **not**
a member of `Registry` (it never appears in the REPL). Handle it in
`main.go` right after the `connect` branch and before the generic
one-shot dispatch:

```go
if os.Args[1] == "run" {
    code := repl.RunRecipe(/* resolved styles/palette/stage/cfg/root/username */, os.Args[2:])
    os.Exit(code)
}
```

It resolves the project (`-C/--project-dir` honored, same pre-parse as
Unit 31) before reading the recipe, since every step needs `root`/`cfg`.

### `internal/repl/recipe.go` — the runner (new file)

- `func RunRecipe(styles theme.Styles, palette theme.Palette, stage theme.Stage, cfg *config.Config, root, username string, args []string) int`
  - Parses args: first non-flag positional is the recipe path (`-` or
    absent → read `os.Stdin`); recognizes `--continue-on-error`.
  - Reads and splits into lines; trims, skips blanks and `#` comments.
  - For each step `i`:
    - Emits a step header in Odoo log style
      (`INFO echo.run: step i/N → <line>`), so the transcript shows the
      recipe progressing.
    - Parses the line into `name, args` and runs it through the Unit 31
      one-shot path. To run multiple steps in one process without leaking
      per-command state, reuse the factored `newSession(...)`: build a
      fresh `session` per step (resetting `lastOutput`/run-stats/
      `exitCode`) and call `dispatchParsed`, then read its `exitCode`.
    - On non-zero exit: if `!continueOnError`, emit the
      `stopped at step i/N` line and `return code`. Otherwise record the
      failure and continue.
  - After the loop, emit the success/finished-with-errors summary and
    return `0` (all green) or `1` (any step failed under
    `--continue-on-error`).
- Unknown step command inside a recipe → that step's `RunOnce` returns
  exit `2`, which under fail-fast aborts the whole run with `2`.

### Help / discoverability

- Add a short line to `helpSections()` (a new "Scripting" group, or the
  Shell group) documenting `echo run <file>` and `--continue-on-error`.
  Note it is one-shot only (not a REPL command), so it is excluded from
  the `Registry`/`dispatch` cross-check tests by design.

### Tests (`internal/repl/recipe_test.go`)

- Recipe parsing: comments, blank lines, and leading/trailing whitespace
  are stripped; the resulting step list matches expectation.
- Fail-fast: given a step list where step 2 returns a non-zero code (via
  a stubbed dispatch hook), steps 3+ do not run and `RunRecipe` returns
  step 2's code.
- `--continue-on-error`: all steps run; return is `1` when any failed,
  `0` when all pass.

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `echo run <file>` recipe runner
  with fail-fast and `--continue-on-error`.
- `context/architecture.md`: note `run` as a one-shot orchestration entry
  in `main.go` (not part of `Registry`).
- `context/progress-tracker.md`: mark Unit 32 done with a session note.

## Dependencies

None new. Pure composition over Unit 31's `RunOnce`/`newSession` and the
existing render/finalize frame.

## Verify when done

- [ ] `echo run update.echo` executes each non-comment line in order,
      streaming each step's output with the standard start/end frame.
- [ ] A recipe with a failing step (e.g. `update <bad-module>`) stops
      immediately, does not run later steps, prints
      `echo.run: stopped at step N/<total>`, and exits with that step's
      code.
- [ ] A clean recipe runs all steps and exits `0` with a
      `<total> steps completed` summary.
- [ ] `--continue-on-error` runs every step and exits `1` if any failed,
      `0` if all passed.
- [ ] `echo run -` (and piped stdin) reads the recipe from stdin.
- [ ] Comments (`#`) and blank lines are ignored.
- [ ] A step that would prompt (missing arg / prod confirm without
      `--force`) fails closed via the Unit 31 guard and aborts the run
      under fail-fast.
- [ ] `-C/--project-dir` is honored for the whole recipe.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
