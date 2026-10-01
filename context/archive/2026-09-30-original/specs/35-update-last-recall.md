# Unit 35: `update --last` recall + last-modules highlight & confirm

## Goal

Make re-running an `update` against the same module set effortless in the
interactive REPL. Three behaviors, all scoped to the `update` command and
to interactive (REPL) mode only — never to `echo run <file>` /
one-shot dispatch:

1. **`update --last`** repeats the last `update` for the current
   project+database (the resolved module list, or `--all` if that was
   last). It bypasses the picker and runs directly, with no confirmation.
2. **The fuzzy picker highlights the previous run's modules** in a distinct
   color, so the last set is easy to spot.
3. **An empty picker confirm falls back to "repeat last".** When you open
   the picker (`update` with no args) and confirm it with **nothing
   selected**, Echo shows a brief confirmation listing the previous run's
   modules; confirming repeats that update. (Explicit `update <mods>` and
   `update --all` run directly — no confirmation.)

So the empty-picker fallback and `--last` are two routes to the same
"repeat last update": `--last` is the keystroke-free shortcut, the picker
fallback is the discoverable path (open the picker, see the highlighted
last set, press Enter to repeat after a one-key confirm).

The "last update" is persisted on disk (survives REPL restart), one record
per (project, database).

## Design

Today `update` resolves its module list three ways — `--all`, explicit
positional args, or the fuzzy picker — and runs immediately
([`RunUpdate`](../../internal/cmd/modules.go) lines 153–182). This unit
adds a persisted memory of the last resolved target and the two ergonomics
on top of it.

**Persistence.** Mirror the `connect-session` cache pattern
([`internal/config/connect_session.go`](../../internal/config/connect_session.go)):
a per-project TOML file keyed by `Config.ProjectKey`, holding a table of
`DBName → LastUpdate`. Best-effort — a missing/unreadable file means "no
previous update", and a save failure never aborts the command. Stored at
`~/.config/echo/last-updates/<ProjectKey>.toml`.

**What counts as "the last update".** The record is written whenever an
`update` actually executes against a concrete target — the `--all` branch,
the explicit-args/picker branch, and the `--last` re-run —
**regardless of exit status**, because re-running after a *failed* update
is the prime use case ("a veces necesito correr el update en el mismo
módulo"). It is **not** written when the run is cancelled (picker Esc, or
the repeat-last confirmation declined). The `--level` in effect is stored
alongside, so `--last` and the picker fallback faithfully repeat verbosity
too (overridable by passing `--level` on the repeat).

**Empty picker = "use last".** The current picker collapses both Esc and
an empty-but-confirmed selection into `ErrCancelled`
([`picker.go`](../../internal/cmd/picker.go) `runFuzzyPicker`). The update
flow needs to tell them apart: Esc → cancel; Enter with nothing selected →
fall back to repeating the previous list (behind a confirmation). This is
done by exposing the picker's `canceled` flag to an update-specific call
path, leaving the existing `install`/`uninstall`/`test` semantics
(empty selection cancels the op) untouched.

**Interactive-only.** The picker highlight and the repeat-last
confirmation appear only in the interactive REPL, never under
`echo run <file>` or `echo <cmd>` script mode. The empty-picker fallback
can only happen via the interactive picker anyway, but gate it explicitly
on a `ModulesOpts.Interactive` flag (set true only by the REPL prompt
loop) so a one-shot `echo update` (no args, TTY) cancels on empty
selection exactly as today instead of offering a repeat.

**Colors** (palette tokens via `opts.Palette`, see `context/ui-context.md`):
- Previous-run modules in the picker → `palette.Info`, with a faint legend
  hint `· highlighted = last update` shown only when such a module exists.
- The repeat-last confirmation reuses the existing `huh` theme
  (`BuildHuhTheme`), consistent with `confirmProd`/`confirmDrop`
  ([`shell.go`](../../internal/cmd/shell.go) lines 96–120).

`--all` is intentionally **not** confirmed (it's already an explicit flag),
and `--last` is the fast path (no confirmation by design).

## Implementation

### `config.LastUpdate` persistence (`internal/config/last_update.go`, new)

Follows `connect_session.go` exactly (TOML, atomic write, best-effort load):

```go
// LastUpdate is the last `update` target for one (project, database),
// reused by `update --last` and the empty-picker repeat. Modules is the
// resolved list; All is true when the last run was `update --all` (then
// Modules is empty). Level is the --log-level in effect (may be empty).
type LastUpdate struct {
    Modules []string  `toml:"modules"`
    All     bool      `toml:"all"`
    Level   string    `toml:"level"`
    SavedAt time.Time `toml:"saved_at"`
}

// lastUpdatesFile is the on-disk shape: a table of dbName → LastUpdate,
// one file per project key at ~/.config/echo/last-updates/<key>.toml.
type lastUpdatesFile struct {
    Updates map[string]LastUpdate `toml:"updates"`
}

func lastUpdatesPath(projectKey string) (string, error) // root + "last-updates/" + key + ".toml"

// LoadLastUpdate returns the saved target for (projectKey, db) and whether
// one exists. A missing/unparseable file or absent db key yields (zero,
// false) — never an error; the cache is an optimization, not a dependency.
func LoadLastUpdate(projectKey, db string) (LastUpdate, bool)

// SaveLastUpdate upserts the record for db into the project's file,
// preserving other databases' entries, and writes it atomically. Reuses
// writeAtomic + MkdirAll(0o700) from the connect-session pattern.
func SaveLastUpdate(projectKey, db string, u LastUpdate) error
```

`SavedAt` is set by the caller via `time.Now()` before `SaveLastUpdate`
(keep the package free of clock calls, same as elsewhere).

### `RunUpdate` rework (`internal/cmd/modules.go`)

Parse `--last` alongside `--all`; `--level` is already stripped first by
`extractLevel`. Add to the arg loop:

```go
all, last := false, false
modules := make([]string, 0, len(rest))
for _, a := range rest {
    switch a {
    case "--all":
        all = true
    case "--last":
        last = true
    default:
        modules = append(modules, a)
    }
}
```

**`--last` branch (first, fast path).** Mutually exclusive with `--all`
and with explicit module names:

```go
if last {
    if all || len(modules) > 0 {
        return nil, ErrLastExclusive // "--last takes no modules and can't combine with --all"
    }
    prev, ok := config.LoadLastUpdate(opts.Cfg.ProjectKey, opts.Cfg.DBName)
    if !ok {
        return nil, ErrNoLastUpdate // "no previous update to repeat for this database"
    }
    if level == "" {
        level = prev.Level // inherit verbosity unless overridden this run
    }
    if prev.All {
        saveLastUpdate(opts, nil, true, level)
        return []string{"--all"}, runOdoo(ctx, opts, odoo.WithLogLevel(odoo.UpdateAll(buildConn(opts)), level))
    }
    saveLastUpdate(opts, prev.Modules, false, level) // refresh timestamp/level
    return prev.Modules, runOdoo(ctx, opts, odoo.WithLogLevel(odoo.Update(buildConn(opts), prev.Modules), level))
}
```

**`--all` branch.** Unchanged behavior, plus a save:

```go
if all {
    saveLastUpdate(opts, nil, true, level)
    return []string{"--all"}, runOdoo(ctx, opts, odoo.WithLogLevel(odoo.UpdateAll(buildConn(opts)), level))
}
```

**Explicit-args / picker branch.** Explicit modules run directly. With no
args, open the picker (previous modules highlighted); an empty confirm
falls back to repeating the previous list behind a confirmation:

```go
prev, hasPrev := config.LoadLastUpdate(opts.Cfg.ProjectKey, opts.Cfg.DBName)
if len(modules) == 0 {
    picked, canceled, err := pickModulesForUpdate(ctx, opts, prev.Modules)
    if err != nil {
        return nil, err
    }
    if canceled {
        return nil, ErrCancelled // Esc
    }
    if len(picked) == 0 {
        // Enter with nothing selected → offer to repeat the last update.
        if !opts.Interactive || !hasPrev || prev.All || len(prev.Modules) == 0 {
            return nil, ErrCancelled // nothing concrete to fall back to
        }
        if err := confirmRepeatLast(opts.Palette, prev.Modules); err != nil {
            return nil, err // declined → ErrCancelled
        }
        modules = prev.Modules
        if level == "" {
            level = prev.Level
        }
    } else {
        modules = picked
    }
}
saveLastUpdate(opts, modules, false, level)
return modules, runOdoo(ctx, opts, odoo.WithLogLevel(odoo.Update(buildConn(opts), modules), level))
```

New errors next to the existing ones:

```go
ErrNoLastUpdate  = errors.New("no previous update to repeat for this database")
ErrLastExclusive = errors.New("--last takes no module names and can't combine with --all")
```

`saveLastUpdate` is a tiny best-effort helper (ignores the error) so
persistence never breaks a run:

```go
func saveLastUpdate(opts ModulesOpts, modules []string, all bool, level string) {
    _ = config.SaveLastUpdate(opts.Cfg.ProjectKey, opts.Cfg.DBName, config.LastUpdate{
        Modules: modules, All: all, Level: level, SavedAt: time.Now(),
    })
}
```

### Picker: highlight + canceled seam (`internal/cmd/picker.go`)

Thread a `recent []string` through so previous-run modules render in
`palette.Info`, and expose the `canceled` flag so the update flow can tell
Esc from an empty confirm:

- `pickerItem` gains `recent bool`.
- `newFuzzyPicker(title, available, recent, palette)` builds a
  `map[string]bool` from `recent` and sets `items[i].recent`.
- In `View()`, when a row is **not** under the cursor and `it.recent`,
  render `name` with `lipgloss…Foreground(p.Info)`. Cursor highlight
  (Accent bold) and the selected `[×]` checkbox keep precedence. Append the
  faint legend `· highlighted = last update` to `helpText` only when any
  item is recent.
- Refactor the runner into a core that surfaces `canceled`:

  ```go
  // runFuzzyPickerCore runs the multi-select and returns the selected
  // names, whether the user canceled (Esc/ctrl+c), and any run error.
  // An empty `picked` with `canceled == false` means Enter on an empty
  // selection — the caller decides what that means.
  func runFuzzyPickerCore(title string, available, recent []string, palette theme.Palette) (picked []string, canceled bool, err error)
  ```

  `runFuzzyPicker` becomes a thin wrapper preserving today's semantics
  (empty or canceled → `ErrCancelled`) for `install`/`uninstall`/`test`;
  `runSingleFuzzyPicker` is unchanged aside from the new (nil) `recent`
  arg.

- `pickModulesInteractive(ctx, opts, title, recent)` gains the `recent`
  param and forwards it; the `install`/`uninstall`/`test` call sites pass
  `nil`. A new `pickModulesForUpdate(ctx, opts, recent)` resolves the
  available modules and calls `runFuzzyPickerCore`, returning
  `(picked, canceled, err)` so `RunUpdate` can implement the empty-confirm
  fallback. (`ErrNoModulesAvailable` still surfaces as an error.)

### `confirmRepeatLast` (`internal/cmd/modules.go`)

A `huh.NewConfirm` styled like `confirmProd`. Skips silently when stdin
isn't a TTY (defensive — the empty-picker path is already interactive).
Lists `modules` one per line in `palette.Info`. Affirmative "Update",
Negative "Cancel", default focus on Update so a single Enter proceeds;
returns `ErrCancelled` when declined.

```go
func confirmRepeatLast(palette theme.Palette, modules []string) error {
    if !stdinIsTTY() {
        return nil
    }
    info := lipgloss.NewStyle().Foreground(palette.Info)
    var b strings.Builder
    for _, m := range modules {
        b.WriteString("  " + info.Render(m) + "\n")
    }
    confirmed := false
    form := huh.NewForm(huh.NewGroup(
        huh.NewConfirm().
            Title(fmt.Sprintf("Repeat last update — %d module(s)?", len(modules))).
            Description(b.String()).
            Affirmative("Update").
            Negative("Cancel").
            Value(&confirmed),
    )).WithTheme(BuildHuhTheme(palette)).WithInput(os.Stdin).WithOutput(os.Stdout)
    if err := form.Run(); err != nil {
        return err
    }
    if !confirmed {
        return ErrCancelled
    }
    return nil
}
```

### REPL wiring

- `internal/cmd/modules.go` `ModulesOpts`: add `Interactive bool` — true
  only in the interactive REPL, false under recipe/one-shot, so the
  empty-picker repeat fallback never fires in `echo run <file>` /
  `echo update …`.
- `internal/repl/repl.go`:
  - `session` struct: add `interactive bool`.
  - `Start` (after `newSession`): set `sess.interactive = true`. `RunOnce`
    and `RunRecipe` leave it false (default).
  - `runModules`: set `Interactive: sess.interactive` on the
    `cmd.ModulesOpts`.
- `internal/repl/commands.go` `commandFlags`: append `"--last"` to the
  `update` slice (so it highlights as a known flag and Tab-completes).
- `internal/repl/repl.go` `helpSections()` Modules section: add a
  `{"  --last", "repeat the last update for this database"}` sub-row under
  `update`.

### Tests

- `internal/config/last_update_test.go` (new): save then load round-trips
  Modules/All/Level; per-db isolation within one project file; a missing
  file yields `(zero, false)`; upsert preserves a second db's entry.
- `internal/cmd/modules_test.go`:
  - `update --last` with no saved record → `ErrNoLastUpdate`.
  - `update --last sale` / `update --last --all` → `ErrLastExclusive`.
  - After a normal `update sale` (no picker — explicit args), the saved
    record is `{Modules:[sale]}`; a follow-up `--last` builds an Update
    argv for `[sale]`; level is inherited when omitted and overridden when
    `--level` is passed with `--last`.
  - `update --all` saves `{All:true}`; `--last` then builds `UpdateAll`.
  - Explicit `update sale` runs with **no** confirmation (the
    confirmation/fallback only lives on the empty-picker path).
  - `--last` / explicit args are not mistaken for module names.
- `internal/cmd/picker_test.go` (or `interactive_test.go`): `recent` marks
  the right `pickerItem.recent`; `nil` recent leaves all false; the legend
  hint appears only when a recent item is present; `runFuzzyPickerCore`
  reports `canceled` distinctly from an empty confirm. Use the
  `stdinIsTTY` seam to force paths without a real TTY.
- Keep the `commandhl_test.go` `commandFlags`↔`Registry` guard and
  `registry_test.go` help/Registry cross-check green (the new `--last`
  flag and help row must stay consistent).

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `update --last` to repeat the
  last update per database; the picker now highlights the previous run's
  modules and an empty picker confirm offers to repeat them.
- `context/progress-tracker.md` → mark Unit 35 done; record the decisions
  (scope = update only; persist on disk; `--last` repeats `--all`;
  confirmation only on an empty picker selection, interactive-only).
- `context/specs/00-build-plan.md` → Unit 35 row (added).

## Dependencies

None new. Reuses `BurntSushi/toml`, `charmbracelet/huh`,
`charmbracelet/lipgloss`, and the existing config atomic-write helpers.

## Verify when done

- [ ] `update sale` (explicit) runs directly — no confirmation — and saves
      `{Modules:[sale]}`.
- [ ] `update --last` then re-runs `sale` directly (argv `-u sale`), no
      picker, no confirmation.
- [ ] `update` (no args) opens the picker with previous-run modules tinted
      `palette.Info` and the legend hint; selecting modules updates those
      directly.
- [ ] Confirming the picker with **nothing selected** offers "Repeat last
      update — …" listing the previous modules; Update repeats them, Cancel
      aborts (nothing saved).
- [ ] Empty picker confirm with no prior record (or last was `--all`)
      cancels as today — no fallback.
- [ ] `update --all` then `update --last` repeats `--all` (UpdateAll argv).
- [ ] `update --last` with no prior run for the current DB →
      `ErrNoLastUpdate`; `update --last sale` / `update --last --all` →
      `ErrLastExclusive`.
- [ ] `--last`/empty-picker repeat inherit the previous `--level`; a
      `--level` passed on the repeat overrides it.
- [ ] `echo run <recipe>` and `echo update sale` (script mode) show **no**
      confirmation and behave byte-for-byte as before; the last-update
      record is still written so a later interactive `--last` works.
- [ ] The record persists across REPL restarts and is isolated per
      (project, database).
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
