# Unit 16: copy-output

## Goal

Add two clipboard affordances to the REPL: a manual `copy-last`
command that copies the output of the last executed command, and an
**automatic copy-on-failure** for module commands (`install`, `update`,
`uninstall`) that drops the error+warning lines into the clipboard
when the command fails, and announces it with a single `charmbracelet/log`
error line — so the user can paste the failure straight into a chat,
issue, or terminal.

The unit also generalises the existing clipboard package to **prefer
OSC 52 when the session is remote** (SSH or tmux), while keeping native
helpers (`pbcopy` / `wl-copy` / etc.) as the default for local TTYs.

## Design

### Last-output buffer

A new per-session ring (`sess.lastOutput`) records every `Line` printed
during the **last** command. It is reset at the start of every command
dispatch (right after parseLine produces a non-empty command and
before any handler runs). Lines from the prompt itself
(`$ update sale`) are included so a pasted log makes sense
out-of-context. Cap the buffer at 5_000 lines per command to keep
memory bounded; if exceeded, prepend a `… (output truncated, oldest
lines dropped) …` marker and continue.

Each entry stores `{Kind, Text}` (the same struct used by `sess.print`).
This lets `copy-last` filter by kind without re-classifying.

### `copy-last` command

New top-level command. Registry slot between `clear` and `help`.

| Flag        | Effect                                                |
|-------------|-------------------------------------------------------|
| _(none)_    | Copy **all** lines of the last command (info+out+err+warn) |
| `--errors`  | Copy only lines with `Kind in {err, warn}`           |

Output format: one line per buffered line, plain text (styles stripped
— lipgloss `Render` output is discarded; the buffer holds raw `Text`
from `Line`). A trailing newline closes the payload.

Behaviour:

- Buffer empty (first command of the session, or right after `clear`) →
  print `warn`: `no output to copy — run a command first`.
- Clipboard write fails (`clipboard.ErrUnavailable`) → print `err` with
  the helper hint already encoded in the error.
- Success → print `ok`: `copied N line(s) to clipboard` (or `N error line(s)`
  with `--errors`).

`copy-last` itself does **not** push anything into `sess.lastOutput`
(its own confirmation line would clobber whatever you wanted to copy
on the next `copy-last`). Same exclusion applies to `help`, `clear`,
and `copy-last`.

### Auto-copy on module failure

In `runModules`, after the existing `finalize()` would have decided
the outcome, we branch:

```go
switch {
case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
    // unchanged — print "<name> cancelled — no changes saved"
case err != nil, stats.errors > 0:
    sess.copyFailureLog(name, args, err, stats.errors)
default:
    // unchanged — print "✓ <summary> completed"
}
```

`copyFailureLog`:

1. Walk `sess.lastOutput` and keep entries with `Kind in {err, warn}`.
2. Prepend a single header line: `✗ <summary> failed` (the existing
   `modulesSummary` output, so `update (sale, account)` style).
3. Join with `\n`, write via `clipboard.WriteAll`.
4. Emit the final message as a `charmbracelet/log` **error** line on
   stdout (not via `sess.print`). The log handler is a sessionscoped
   `*log.Logger` wired in `repl.Start` with:
   - `log.New(os.Stdout)`
   - `SetLevel(log.ErrorLevel)`
   - `SetReportTimestamp(false)`
   - Styled with the active palette via `log.Styles` (re-applied on
     `stage` change, same way `theme.Styles` is rebuilt).
5. The line includes structured fields:

   ```
   ERRO update failed module=sale,account copied=true
   ```

   When `clipboard.WriteAll` returns `ErrUnavailable`, the field is
   `copied=false` and a second `info` line via `sess.print` shows the
   install hint from the clipboard error.

The pre-`finalize` empty separator line (`Kind: out, Text: ""`) is
still printed first so the spacing matches successful runs.

### Charm/log integration

`internal/repl/session.go` (or wherever `session` lives) gets a new
field:

```go
log *log.Logger
```

Built in `repl.Start` after `theme.New`:

```go
sess.log = log.NewWithOptions(os.Stdout, log.Options{
    ReportTimestamp: false,
    Level:           log.ErrorLevel,
})
sess.log.SetStyles(buildLogStyles(palette))
```

`buildLogStyles` mirrors `theme.New` — the `ERROR` level uses
`palette.Error` background, white foreground; `WARN` uses
`palette.Warning`. Stage changes (when Unit 14's `stage` command lands)
must call `sess.log.SetStyles(buildLogStyles(palette))` too — add a
hook in the existing stage-switch path or, if Unit 14 isn't merged
yet, in `repl.Start` only and revisit when Unit 14 reapplies the
palette.

### Clipboard backend: remote-aware priority

`internal/clipboard/clipboard.go` currently:

1. Tries OS-specific native helpers.
2. Falls back to OSC 52.

The unit inverts that order **when the session looks remote**:

```go
func WriteAll(text string) error {
    if isRemote() {
        if err := writeOSC52(text); err == nil {
            return nil
        }
        // fall through to native helpers
    }
    // existing native-first path...
}

func isRemote() bool {
    return os.Getenv("SSH_TTY") != "" ||
        os.Getenv("SSH_CONNECTION") != "" ||
        os.Getenv("TMUX") != ""
}
```

Rationale: under SSH, `pbcopy`/`wl-copy` would copy to the **remote**
clipboard which the user can't see. Inside tmux, OSC 52 is the only
reliable path that propagates through the multiplexer to the
host terminal. Outside those contexts, native helpers are faster and
don't depend on the terminal supporting OSC 52.

`isRemote` is intentionally simple — no probe, no escape sequence
test — because both env vars are set reliably by SSH and tmux. Users
who want to force one mode can `unset SSH_TTY` or `TMUX` in a wrapper.

### Out of scope (revisit later)

- Auto-copy for `docker` commands (`up`/`down`/`restart`), `db-*`,
  `i18n-*`. Locked to module commands only per the mockup.
- A persistent log history (writing failures to a file). The buffer is
  in-memory only.
- A `copy-prev N` to copy older outputs. Today the ring holds only the
  most recent command.
- `--copy-on-fail` opt-in flag on individual commands. Auto-copy is
  unconditional for module failures.

## Implementation

### Files

- `internal/repl/lastoutput.go` *(new)*: `lastOutputBuffer` struct
  (slice of `Line` + cap), `Add(Line)`, `Reset()`, `Filtered(kinds
  map[string]bool) []Line`, `Plain() string`.
- `internal/repl/repl.go`:
  - `session` gets `lastOutput *lastOutputBuffer` and `log *log.Logger`
    fields.
  - `dispatch()` (or wherever the prompt loop calls into commands)
    resets `sess.lastOutput` before routing, except for `copy-last`,
    `help`, `clear`.
  - `sess.print` appends each printed line to `sess.lastOutput`.
  - `runModules` replaces its `finalize` call (only for the
    `err/errors>0` branch) with `sess.copyFailureLog`.
  - New `case "copy-last":` in dispatch routing to `sess.runCopyLast`.
  - `helpSections` gets a new entry in the **Shell** section:
    `{"copy-last", "Copy the last command's output to clipboard"}` and
    `{"  --errors", "Only copy error/warning lines"}`.
- `internal/repl/commands.go`: add `"copy-last"` to `Registry` (placed
  between `clear` and `help` to match the help ordering).
- `internal/repl/copylast.go` *(new)*: `runCopyLast(args []string)`
  and `copyFailureLog(name, summary, err, errCount)`. Pure REPL code,
  no `internal/cmd/` package needed — there is no Odoo or Docker
  involvement.
- `internal/repl/logstyle.go` *(new)*: `buildLogStyles(palette)` that
  produces a `*log.Styles` matching the palette's error/warn colors.
- `internal/clipboard/clipboard.go`: extract the inner native-helpers
  loop into `writeNative()`; add `isRemote()`; rewrite `WriteAll` to
  branch on `isRemote()`.
- `internal/clipboard/clipboard_test.go` *(new)*: table-driven test
  for `isRemote()` using `t.Setenv` (`SSH_TTY`, `SSH_CONNECTION`,
  `TMUX`, none → expected bool).
- `internal/repl/registry_test.go`: extend `TestRegistryMatchesDispatch`
  and `TestRegistryMatchesHelp` so `copy-last` is exercised (it should
  pass automatically once added to all three sources — the tests are
  set-equality checks).
- `.unverified/untested.html`: add a new section listing the manual
  checks (see below).

### Dispatch reset ordering

The reset must run **before** `sess.print(Line{Kind: "info", Text: "$
" + display})`, so that the prompt line of the **new** command is the
first entry of its own buffer. Concretely, in `dispatch`:

```go
if !isMetaCommand(cmd) {
    sess.lastOutput.Reset()
}
```

`isMetaCommand` returns true for `"copy-last"`, `"help"`, `"clear"`.

### Final message formatting (Q4)

The charm/log line replaces the prior `✗ <summary> failed: <err>`
output. Keypoints:

- Field `module=<a>,<b>` is the comma-joined module list extracted from
  args via the existing `modulesSummary` helper (already strips flags).
  For `--all`, use `module=all`.
- Field `copied=true|false` reflects whether `clipboard.WriteAll`
  returned nil.
- The error message itself (`err.Error()`) goes into the log message
  key: `sess.log.Error("update failed", "module", mods, "copied",
  ok, "err", err)`. If `err` is nil but `errCount > 0`, replace `"err"`
  field with `"errors", errCount`.

### Plain-text rendering for the clipboard

The buffer stores `Line.Text` already un-styled (lipgloss renders
happen inside `sess.print`, after the Text field is captured). So
`Plain()` is just:

```go
func (b *lastOutputBuffer) Plain() string {
    var sb strings.Builder
    for _, l := range b.lines {
        sb.WriteString(l.Text)
        sb.WriteByte('\n')
    }
    return sb.String()
}
```

`Filtered` does the same with a kind allow-list.

## Dependencies

No new modules. Reuses:

- `github.com/charmbracelet/log` — already a direct dependency
  (`main.go` uses it for fatals).
- `github.com/charmbracelet/lipgloss` — for `log.Styles`.
- `internal/clipboard` — existing package.

## Verify when done

- [ ] `copy-last` after `update sale` (success) copies the full
      output including the `✓ update (sale) completed` final line;
      pasting reproduces the run verbatim.
- [ ] `copy-last --errors` after a noisy `update` copies only the
      lines tagged `err`/`warn` (e.g. the Odoo ERROR/CRITICAL lines).
- [ ] `update sale` against a broken module triggers the auto-copy:
      clipboard contains the `✗ update (sale) failed` header plus
      all error+warn lines; stdout shows a single
      `ERRO update failed module=sale copied=true` line styled with
      the active palette's error color.
- [ ] Same scenario without a clipboard helper available emits
      `copied=false` and an extra `info` line with the install hint;
      no panic.
- [ ] `copy-last` on a fresh session (before any command) prints the
      warn fallback and returns to the prompt.
- [ ] `copy-last` after `clear` warns the same way (clear resets the
      buffer).
- [ ] Inside `tmux` (with `$TMUX` set), `update sale` triggers
      auto-copy via OSC 52 — the host clipboard (not the tmux paste
      buffer) ends up with the log; native helpers are not invoked.
- [ ] Outside tmux/SSH, `pbcopy`/`wl-copy` is invoked as before
      (no OSC 52 escape printed to the terminal).
- [ ] `TestRegistryMatchesDispatch` / `TestRegistryMatchesHelp` still
      pass after adding `copy-last`.
- [ ] `go build ./... && go vet ./... && go test ./...` clean.
- [ ] `.unverified/untested.html` updated with the manual checks for
      this unit.

---

## Post-merge evolution (2026-05-15 → 2026-05-18)

The unit shipped against this spec on 2026-05-15. The sections below
override the original design where it diverged during follow-up
polish — kept inline so the spec stays the source of truth for the
implementation as it stands today.

### Output formatting — manual Odoo-style render replaces `charmbracelet/log`

The original spec routed the failure announce through a session-scoped
`*log.Logger`. That was scrapped: charm/log's text formatter can't
produce Odoo's exact line shape (timestamp with comma-separated
milliseconds, PID slot, db slot between LEVEL and logger). The REPL
no longer depends on `charmbracelet/log` at all (the package lives
only in `main.go` for fatal/warn during boot).

A new `internal/repl/logemit.go` owns rendering:

- `emitOdooLog(level, logger, msg, fields, styles, palette, db)` prints
  the styled line to stdout in the same shape as the Odoo logs in the
  stream above. Timestamp dim, PID faint, level chip per level color
  (4-char `DEBU`/`INFO`/`WARN`/`ERRO`/`CRIT`), db in `palette.Accent`,
  logger via a stable FNV-8 pastel rotation (`loggerPalette`), msg in
  default fg, per-key colors on the structured fields.
- `plainOdooLog(level, logger, msg, db)` produces the unstyled sibling
  used as the **first line of the clipboard payload** so the copy
  starts with a self-describing header in the same format as the
  lines that follow.

### Hierarchical loggers

The logger name encodes the event class so a single command produces
a greppable triplet sharing a common prefix:

| Phase     | Logger pattern                              | Level   |
|-----------|---------------------------------------------|---------|
| start     | `echo.<cmd>.start`                          | INFO    |
| completed | `echo.<cmd>` (no suffix)                    | INFO    |
| error     | `echo.<cmd>.error`                          | ERROR   |
| cancelled | `echo.<cmd>.cancelled` (interactive shells) | WARNING |

For module commands the path further embeds the resolved target:

- `echo.update.module.sale[.start|.error]`  — single module
- `echo.update.modules[.start|.error]`      — several modules
- `echo.update.all[.start|.error]`          — `--all`

For non-module commands the start line carries positional args as a
structured `args=` field instead of baking them into the path.

### Resolved modules returned by RunInstall/Update/Uninstall

`RunInstall`, `RunUpdate`, `RunUninstall` were updated to return
`([]string, error)` — the second value is the resolved targets after
flag stripping and picker resolution (sentinel `["--all"]` for
`--all`). `runModules` propagates the slice to the start / success /
failure renders so the report always names the real modules — even
when the user invoked the command with no args.

### Failure capture: from first err/warn onwards

`lastOutputBuffer.FromFirstError()` returns every line from the first
err/warn-tagged entry to the end, used as the clipboard payload on
auto-copy. That keeps the failing command's leading warnings, the
traceback, and the trailing INFO lines Odoo emits while unwinding
(shutdown, closed connections). Falls back to the full buffer when
no err/warn was logged.

The Odoo log classifier was tightened in passing: it now anchors on
the full prefix (`^ts pid LEVEL `) so traceback frames that mention
the literal words `DEBUG` / `INFO` (e.g. inside a `--log-handler`
hint string) no longer break err-kind inheritance mid-stream.

### Auto-copy extended to every subprocess command

The original spec scoped auto-copy to module commands only. After
the manual render landed the scope was widened to every command that
runs a subprocess and could produce a useful failure capture:

- module: `install` / `update` / `uninstall`           — via `lastOutputBuffer`
- shells: `bash` / `psql` / `shell`                    — via PTY-tee'd output
- i18n:   `i18n-export` / `i18n-update`                — via `lastOutputBuffer`
- db:     `db-backup` / `db-restore` / `db-drop`       — via `lastOutputBuffer`
- docker: `up` / `down` / `restart`                    — via `lastOutputBuffer`

Read-only inspections (`ps`, `logs`, `db-list`, `modules`) and meta
commands stay opt-out — `copy-last` is the manual path.

Two helpers in `internal/repl/copylast.go` handle the two capture
shapes:

- `commandFailureLog(name, runErr, errCount, warnCount)` — reads
  from `lastOutputBuffer.FromFirstError()`. Used by everything that
  streams through `sess.print`.
- `shellFailureLog(name, captured, runErr)` — uses the raw string
  captured by `ExecInteractive`. Used by the interactive shells
  whose output bypasses `sess.print`.

### Interactive shells need a host-side PTY

`docker compose exec` with the default `-t` allocates a PTY inside
the container and fuses the container's stdout and stderr into one
stream — tee'ing only stderr from the host captured nothing useful.
`ExecInteractive` now spawns the docker subprocess under a host
PTY via `github.com/creack/pty`:

- host stdin → raw mode (`golang.org/x/term`) → PTY master
- PTY master → `io.MultiWriter(os.Stdout, &captureBuf)` so the user
  still sees the live stream AND we keep the bytes for auto-copy
- `SIGWINCH` propagated via `pty.InheritSize` so resizing the host
  window reaches the container's TTY
- fallback to plain pipe-tee when stdin is not a TTY (tests, pipes)

The function returns `(captured, interrupted, err)`. `interrupted`
is set from an `atomic.Bool` toggled inside the SIGINT handler in
the parent process — we use this rather than the exit code because
Odoo's shell catches SIGINT, prints a `KeyboardInterrupt` traceback,
and exits with status 1 (not 130). When `interrupted` is true the
REPL emits the WARN-level `echo.<cmd>.cancelled` line instead of
running `shellFailureLog` (the traceback the user already saw and
deliberately triggered shouldn't end up in their clipboard).

### Other small fixes that landed alongside

- `ErrCancelled` text generalised from `"init cancelled"` to
  `"cancelled by user"` — it's reused by every picker and prod
  confirm, the old text was misleading when shown outside `init`.
- `RunOdooShell` builds its connection argv via `odoo.Conn.Flags()`
  (the same helper `install`/`update` use) instead of inline
  string concatenation. Empty values are skipped so a missing
  `POSTGRES_PORT` in `.env` doesn't produce a literal `--db_port=`
  that crashed Odoo with `ValueError: int('')`. Defaults to `5432`
  defensively. `Conn.flags()` is exposed publicly as `Conn.Flags()`.
- Charm palette `Warning` switched from orange `#f6ad55` to pastel
  yellow `#fde047` for better visual separation from `Error`.

### Verify when done (additions)

- [ ] `update <broken-mod>` produces a triplet
      `echo.update.module.<mod>.start` (INFO) → traceback →
      `echo.update.module.<mod>.error` (ERROR auto-copy).
- [ ] `update <ok-mod>` produces `…start` (INFO) → INFO log stream →
      `echo.update.module.<mod>` (INFO `completed warnings=N`).
- [ ] `shell` then `exit` produces `echo.shell.start` → shell session
      → `echo.shell: shell exited` (INFO).
- [ ] `shell` then Ctrl+C produces `echo.shell.start` → traceback →
      `echo.shell.cancelled: shell interrupted by user` (WARN), no
      auto-copy.
- [ ] `shell` with broken config (e.g. unset `POSTGRES_PORT`) emits
      `echo.shell.error: shell failed err="exit status 1" copied=true`;
      clipboard starts with the plain Odoo log header line.
- [ ] `db-backup` / `db-restore` / `db-drop` / `i18n-*` / docker
      `up`/`down`/`restart` honour the same start/error pattern.
- [ ] Clipboard payload starts with a real plain Odoo log line, not
      the legacy `✗ <name> failed` marker.
