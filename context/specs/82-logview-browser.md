# Unit 82: `logview` — interactive browser over the command log history

## Goal

New `logview` command: an interactive, alt-screen browser over the
per-project command log history that Unit 81 persists. Two levels: a
**run list** (newest first, type-to-filter) and, on `enter`, a **log
view** of that run where typing filters lines live and `tab` cycles the
level filter (all → DEBUG → INFO → WARNING → ERROR → CRITICAL).
Headless escape hatches: `--list` (plain table), `--last` (open the
newest run directly), `--clear` (purge the project's history).

## Design

**Same visual language as the existing interactive surfaces.** The
browser is a bubbletea alt-screen program styled like `helpPager` and
the fuzzy picker: every row hangs off a left `│` bar tinted by the
project stage (`palette.PromptColor(sess.stage)`), the filter input is a
`filter ›` line with a Faint placeholder, dim/faint secondary text, and
a faint key-legend footer. `ctrl+x` closes Echo entirely via the
`cmd.ErrQuit` convention (`handleQuit`), exactly like the pickers and
the help pager.

**View 1 — run list.** One row per record from
`config.ListCmdLogs(root)`, newest first:

```
│ logview — 42 runs (7d retention)
│ filter › upd
│
│ ❯ 18:12:03  update sale --level debug     ok    124 lines  muutrade
│   17:55:41  update sale account           ok     98 lines  muutrade
│   09:02:10  update l10n_mx_edi            err    412 lines  muutrade
│
│ ↑↓ move · enter open · type filter · esc close · ctrl+x quit
```

- Columns: time (`15:04:05` today, `Jan 02 15:04` older), the full
  `cmd` line, exit status (`ok` dim / `err` in Err red / `cancel`
  faint), line count, db. Status derives from the record's `exit`.
- Typing filters rows by case-insensitive substring over the `cmd`
  line (the same simple `strings.Contains` matching the fuzzy picker
  uses); `backspace` edits, `ctrl+u` clears.
- `esc` with a non-empty filter clears it; with an empty filter closes
  the browser. `q` always closes. `enter` opens the selected run.
- ↑/↓ move the cursor; the list scrolls within the viewport
  (`WindowSizeMsg`-sized, helpPager's chrome/clamp pattern).

**View 2 — log view.** The selected record's lines, rendered with the
same level→color mapping the live REPL uses (`kindFromLevel` →
styles, so an INFO line looks like it did when it scrolled by):

```
│ update sale --level debug — 18:12:03 · ok · muutrade
│ filter › cache   level › WARNING+
│
│ 2026-07-06 18:12:04,102 1 WARNING muutrade odoo.modules…: …
│ ↓ 12 more
│
│ ↑↓ scroll · tab level · type filter · ctrl+o copy · esc back · ctrl+x quit
```

- **Text filter**: typing appends to the filter; lines are matched
  case-insensitively by substring. Editing keys as in view 1.
- **Level filter**: `tab` cycles all → DEBUG+ → INFO+ → WARNING+ →
  ERROR+ → CRITICAL → all. It is a **min-level threshold** (reuses
  `levelRank`), not an exact match — "solo INFO" in practice means
  hiding DEBUG noise, and thresholds compose better with the text
  filter; `shift+tab` cycles backwards. Unleveled lines (`level == ""`)
  only show on "all".
- Both filters compose (AND). The header shows the active level as
  `level › WARNING+` (dim when "all").
- **`ctrl+o` copies the currently visible (filtered) lines** as plain
  text to the clipboard and closes with the standard copied INFO frame —
  the interactive analog of `report --copy`.
- `esc`: clear the text filter if non-empty, else go back to the run
  list (level filter resets on back). `q` closes the browser from
  either view.
- Scrolling: ↑/↓/pgup/pgdown with the `↑ N more`/`↓ N more` markers,
  helpPager's exact clamp logic. When a filter change shrinks the list,
  the offset re-clamps to keep the view valid.

**Flags / one-shot behavior.**

- `logview` (no flags) — the browser; **TTY-guarded** (`requireTTY`
  semantics: non-TTY without a headless flag fails closed with the
  usage hint `use --list`).
- `logview --list` — plain, non-interactive table of the run list
  (time, cmd, status, lines, db), newest first; works piped/CI.
- `logview --last` — skip the run list, open the newest record's log
  view directly (usage warning when the history is empty).
- `logview --clear` — delete the project's history
  (`config.ClearCmdLogs`) after a confirm prompt; `--force` skips the
  prompt; non-TTY without `--force` fails closed. Closes with an INFO
  frame naming how many records were removed.
- `logview` is a **meta command** (`isMetaCommand`) — it must not reset
  `lastOutput` (so `copy-last` still copies the previous command) and
  Unit 81's skip list already excludes it from being logged itself.

**Logging frame.** On close the command emits a single
`echo.logview` INFO line (`runs=N` for the list, `run=<cmd>
lines=<shown>/<total>` after a log view, `removed=N` for `--clear`) —
`view`'s outcome-frame pattern. Cancellation (esc/q) is a normal close,
not `ErrCancelled`: browsing then leaving *is* the use case.

## Implementation

### `internal/repl/logview.go` — new file (model + command)

- `logviewModel` bubbletea model with `mode` (list / detail), the
  loaded `[]config.CmdLogMeta`, the open `config.CmdLogRecord`, both
  filters, cursor/offset, `height`, palette/accent, `quit bool`
  (ctrl+x) and `copied int` (lines copied via ctrl+o, reported in the
  close frame).
- Pure helpers, unit-testable without a TTY:
  - `filterRuns(metas []config.CmdLogMeta, q string) []config.CmdLogMeta`
  - `filterLogLines(lines []config.ReportLine, q, minLevel string) []config.ReportLine`
    (threshold via the existing `levelRank`; `minLevel == ""` = all,
    unleveled lines only pass on all)
  - `cycleLevel(cur string, back bool) string`
  - `runStatusLabel(exit int) string` (`ok`/`err`/`cancel` per the
    exit-code constants)
  - `logviewTimeLabel(t, now time.Time) string`
- `runLogview(ctx, args)` on `session`: parse flags (`--list`,
  `--last`, `--clear`, `--force`; unknown → usage), load
  `config.ListCmdLogs(sess.projectDir)`, branch per flag, else start
  the tea program (alt-screen) and emit the close frame; `ErrQuit` on
  ctrl+x through `handleQuit`.
- Copy path reuses `clipboard.WriteAll` over the filtered lines' plain
  text.

### Registration

- `Registry` / `dispatchNames` / dispatch `case "logview"` /
  `commandFlags["logview"] = {"--list", "--last", "--clear", "--force"}`.
- `isMetaCommand` gains `"logview"`; Unit 81's save-skip list already
  references it.
- Help, Session section (next to `report`, its non-interactive
  sibling):
  `{"logview", "Browse past commands' logs (filter by text and level)"}`,
  `{"  --list", "Print the run list without the interactive browser"}`,
  `{"  --last", "Open the most recent run directly"}`,
  `{"  --clear", "Delete this project's log history (--force skips confirm)"}`.
- One-shot: `logview --list`/`--clear --force` work headless; the
  browser requires a TTY (fails closed otherwise). Not projectless —
  the history is keyed by the local project dir, which must exist.

### Tests

`internal/repl/logview_test.go`:

- `filterLogLines`: text-only, level-threshold-only (WARNING+ hides
  INFO/DEBUG and unleveled), composed AND, empty-filter identity;
  unleveled lines visible only on "all".
- `cycleLevel` full forward/backward cycle including wrap.
- `filterRuns` case-insensitive substring over `cmd`.
- `runStatusLabel` / `logviewTimeLabel` table cases.
- Registry/help/dispatch consistency (`registry_test.go`) picks up
  `logview` automatically — keep it green.

## Dependencies

None new — `bubbletea` + `lipgloss` (already direct), Unit 81's
`config.ListCmdLogs`/`LoadCmdLog`/`ClearCmdLogs`.

## Verify when done

- [ ] `logview` opens the run list newest-first with the stage-tinted
      `│` frame; typing `upd` narrows to the `update …` runs live.
- [ ] `enter` opens a run; typing filters its lines; `tab` steps the
      level filter (WARNING+ hides INFO/DEBUG; unleveled lines only on
      all); filters compose.
- [ ] `ctrl+o` in the log view copies exactly the visible lines to the
      clipboard and the close frame reports it.
- [ ] `esc` clears the active filter first, then navigates back / out;
      `q` closes from anywhere; `ctrl+x` exits Echo entirely.
- [ ] `logview --list` prints a plain table with no TTY required;
      `logview --last` opens the newest run's log view directly.
- [ ] `logview --clear` confirms then empties
      `~/.config/echo/cmd-logs/<key>/`; `--force` skips the prompt;
      non-TTY without `--force` fails closed.
- [ ] After browsing, `copy-last` still copies the **previous**
      command's output (logview is meta) and no `logview` record
      appears in the history.
- [ ] Empty history: `logview` and `--last` warn `no runs recorded yet`
      (usage exit), nothing crashes.
- [ ] `help` shows the `logview` block; registry/help/dispatch
      consistency tests stay green.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/repl/...`
      pass.
