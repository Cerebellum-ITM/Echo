# Unit 31: Non-interactive script mode (`echo <cmd> [args]`)

## Goal

Let Echo run a single command non-interactively and exit, so it can be
driven from shell scripts and CI (e.g. an instance-update script that
chains `echo stop`, `echo up`, `echo update ventas contabilidad`,
`echo restart`). Invoking `echo <command> [args…]` resolves the project,
runs exactly that command once — reusing the same Odoo-style render and
`startLog`/`finalize` frame the REPL uses — and exits with a meaningful
status code. Invoking bare `echo` (no args) still opens the interactive
REPL exactly as today. Commands that would otherwise block on a
`huh.Confirm` or a fuzzy picker **fail closed** (non-zero exit, clear
message) when stdin is not a TTY, instead of hanging a script. A new
`-C/--project-dir` flag lets a one-shot command run from outside the
project directory.

## Design

This is the generalization of the existing projectless `echo connect …`
branch in `main.go`: that case already dispatches one command before the
REPL starts. Unit 31 turns that one special case into the rule for
**every** registered command, while keeping `connect`'s projectless
behavior intact.

Three design pillars:

1. **One-shot = the REPL frame, headless.** The command layer
   (`internal/cmd/`) is already decoupled from the prompt loop — every
   `Run*` takes `Cfg/Root/Args/Palette/StreamOut` and never touches
   bubbletea. The rendering (`session.print` → `renderLogLine`) and the
   start/end log frame (`startLog`, `finalize`, `readonlyFinalize`) live
   as `session` methods but only call `fmt.Println`. So script mode
   constructs a `session` **without** the textinput/history/prompt loop
   and calls the existing `dispatch` once. Output is byte-for-byte the
   same as the REPL's — coherent with Echo's Odoo-style log convention.

2. **The interactivity guard keys on the TTY, not on a mode flag.**
   The blocking call sites are the fuzzy picker (`runFuzzyPicker` /
   `runSingleFuzzyPicker`, reached when a required positional is missing)
   and the red confirms (`confirmProd`, `confirmDrop`, `confirmNeutralize`,
   `confirmI18nProd`, and the `init`/`reset` `huh.Form`s). Each of these
   guards on `term.IsTerminal(os.Stdin.Fd())` at entry: with no TTY it
   returns a new `ErrNonInteractive` sentinel whose message names the
   escape hatch (pass the missing argument, or pass `--force`). This
   single guard covers both script mode and any TTY-less invocation
   without threading a boolean through every `Opts` struct. A human who
   types `echo update` at a real terminal still gets the picker; a script
   (no TTY) gets a clean error and a non-zero exit.

3. **Meaningful exit codes.** `finalize` already branches on
   cancelled / error / error-count / success; script mode records that
   branch as a process exit code:

   | Code | Meaning |
   |------|---------|
   | `0`  | success (command completed, no ERROR lines) |
   | `1`  | execution error (command returned an error, or ERROR/CRITICAL lines were counted) |
   | `2`  | usage error (unknown command, bad args, or `ErrNonInteractive` — the script must be explicit) |
   | `3`  | cancelled (`ErrCancelled` / `huh.ErrUserAborted` — only reachable with a TTY) |

Commands that are interactive by nature (`init`, `reset`, `bash`, `psql`,
`shell`) need no special-casing: run one-shot from a real terminal they
work as usual; run from a script (no TTY) the guard fails them closed.
The REPL-only meta tokens `clear` / `exit` / `quit` are not dispatched in
one-shot mode; `echo help` prints the help sections and exits `0`.

## Implementation

### `main.go` — generalize the entry dispatch

Replace the single `os.Args[1] == "connect"` branch with:

1. Parse a leading `-C <dir>` / `--project-dir <dir>` (and `-C=<dir>`)
   out of `os.Args` early, before any project resolution. When present,
   it overrides the cwd used for `project.FindRoot` (and is stripped from
   the args handed to the command).
2. `connect` stays first and projectless (unchanged
   `cmd.RunDirectConnect`).
3. If `os.Args[1]` is a one-shot-eligible command (see `repl.IsScriptCommand`
   below): resolve `root` (from `-C` or cwd via `project.FindRoot`),
   `cfg`, compose flavor, `palette`/`stage`/`styles`, and `username`
   exactly as the REPL path does today (factor the shared setup so it
   isn't duplicated), then call `code := repl.RunOnce(...)` and
   `os.Exit(code)`.
4. Otherwise (no args) start the REPL as today.

`project.FindRoot` failures in one-shot mode must exit `2` with the same
"not inside a project" hint, **not** drop into the REPL.

### `internal/repl/script.go` — headless one-shot runner (new file)

- `func RunOnce(styles theme.Styles, palette theme.Palette, stage theme.Stage, cfg *config.Config, root, username string, name string, args []string) int`
  - Builds a `session` via a factored `newSession(...)` helper (extracted
    from `Start`) that sets the rendering fields (`styles`, `palette`,
    `cfg`, `lastOutput`, run-stats) but **does not** create the
    textinput/history or enter the read loop.
  - Calls `sess.dispatch(context.Background(), name+" "+strings.Join(args, " "))`
    once (or a direct `sess.dispatchParsed(ctx, name, args)` to avoid
    re-splitting).
  - Returns `sess.exitCode`.
- `func IsScriptCommand(name string) bool` — true for every `Registry`
  command except the REPL-only meta (`clear`, `exit`, `quit`); used by
  `main.go` to decide one-shot vs REPL. `connect` is handled earlier in
  `main.go`, so it need not be excluded here.

### `internal/repl/repl.go` — exit-code tracking + factored setup

- Add `exitCode int` to the `session` struct (default `0`).
- Extract the session construction in `Start` into `newSession(...)` so
  `RunOnce` reuses it; `Start` then builds the session, attaches the
  prompt loop, and runs as before.
- `dispatch`: in the `default` (unknown command) branch, set
  `sess.exitCode = 2` alongside the existing warn line.
- `finalize(name, errorCount, warnCount, err)`: set `sess.exitCode` in
  each branch — `ErrCancelled`/`huh.ErrUserAborted` → `3`;
  `errors.Is(err, cmd.ErrNonInteractive)` → `2`; other `err != nil` → `1`;
  `errorCount > 0` → `1`; success → leave `0`. (The printed log line is
  unchanged.)
- `readonlyFinalize(...)`: same mapping for the read-only commands
  (`ps`/`logs`/`modules`/`db-list`) — error → `1`, success → `0`.
- In interactive (REPL) use, `exitCode` is simply ignored, so behavior is
  unchanged.

### `internal/cmd/` — `ErrNonInteractive` + TTY guards

- New sentinel in the `cmd` package:
  ```go
  // ErrNonInteractive is returned when a command needs a terminal
  // (a picker or a confirmation) but stdin is not a TTY — e.g. when Echo
  // is driven from a script. The caller maps it to exit code 2.
  var ErrNonInteractive = errors.New("requires a terminal; pass the argument/flag explicitly to run non-interactively")
  ```
- New helper `func interactive() bool { return term.IsTerminal(int(os.Stdin.Fd())) }`.
- Guard at the entry of the blocking helpers, returning a wrapped
  `ErrNonInteractive` with a context-specific hint:
  - `runFuzzyPicker` / `runSingleFuzzyPicker` (`internal/cmd/picker.go`):
    if `!interactive()`, return `fmt.Errorf("%w: this command needs a selection — pass it as an argument", ErrNonInteractive)`.
  - `confirmProd` (`shell.go`), `confirmDrop` (`db.go`),
    `confirmNeutralize` (`db.go`), `confirmI18nProd` (`i18n.go`): if
    `!interactive()`, return `fmt.Errorf("%w: pass --force to proceed", ErrNonInteractive)` instead of showing the `huh.Confirm`.
  - `init`/`reset` forms: guard at the top of `RunInit`/`RunReset` so a
    TTY-less invocation returns `ErrNonInteractive` rather than a
    half-rendered form.

These guards are equally correct for the REPL (which always has a TTY) —
they only ever fire in script/CI use.

### `-C/--project-dir` plumbing

Handled entirely in `main.go` (a tiny pre-parse before `FindRoot`); no
change to the `cmd`/`repl` signatures beyond passing the resolved `root`.
The flag is consumed by `main.go` and never forwarded to the command.

### Tests (`internal/repl/*_test.go`, `internal/cmd/*_test.go`)

- `IsScriptCommand`: every `Registry` entry except `clear`/`exit`/`quit`
  is script-eligible; the meta trio is not. Keep it in sync via the
  existing `registry_test.go` cross-check style.
- Exit-code mapping: a table test driving `finalize`/`readonlyFinalize`
  through a `session` and asserting `exitCode` for each branch (success,
  err, errorCount>0, cancelled, non-interactive).
- `ErrNonInteractive` guard: a test that forces the non-TTY path (inject
  the `interactive()` result via a package-level hook, mirroring the
  `dockerInspectFn` test seam) and asserts the picker/confirm helpers
  return `ErrNonInteractive`.

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: non-interactive one-shot mode
  (`echo <cmd> [args]`), TTY-based fail-closed guard, exit codes,
  `-C/--project-dir`.
- `context/architecture.md`: add the one-shot entry point to
  `main.go`'s description and to the System Boundaries; add an invariant
  for the exit-code contract and the no-TTY-no-prompt rule.
- `context/project-overview.md`: add script-driven invocation to scope.
- `context/progress-tracker.md`: mark Unit 31 done with a session note.

## Dependencies

None new. `golang.org/x/term` (TTY detection) is already a direct module
dependency; reuses `huh`, the picker, `project.FindRoot`, and the
existing `session` render/finalize frame.

## Verify when done

- [ ] `echo` with no args opens the interactive REPL, exactly as before.
- [ ] `echo ps` (and `echo up`, `echo stop`, `echo restart`) runs the one
      command, streams Odoo-style output identical to the REPL, and exits.
- [ ] `echo update ventas contabilidad` updates exactly those modules
      non-interactively and exits `0`; `echo update` (no module) in a
      script (no TTY) exits `2` with an `ErrNonInteractive` message.
- [ ] `echo install <bad-module>` (or any command hitting an Odoo ERROR)
      exits `1`.
- [ ] `echo db-drop <db>` without a TTY and without `--force` exits `2`
      and does **not** drop; `echo db-drop <db> --force` drops and exits `0`.
- [ ] `echo down` / `echo db-neutralize <db>` against a `stage=prod`
      project without a TTY exits `2` unless `--force` is passed.
- [ ] Exit codes chain correctly in a shell script
      (`echo stop && echo up && echo update sale && echo restart` stops at
      the first failing step).
- [ ] `echo -C /path/to/project ps` runs from outside the project dir;
      an invalid `-C` (or cwd with no `docker-compose.yml`) exits `2` with
      the "not inside a project" hint and does **not** open the REPL.
- [ ] `echo connect …` projectless mode is unchanged.
- [ ] `echo help` prints the help sections and exits `0`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
