# Unit 55: log-framed picker restyle + stage-tinted accent

## Goal

Restyle Echo's fuzzy picker (`internal/cmd/picker.go`, used by every
interactive selection: connect/i18n-pull target, module, user, recent
sessions) so it reads as part of the Odoo-log stream instead of a boxy
"app" widget, and tint its highlight by the target's stage (dev/staging/prod)
wherever that stage is known. One `View()` is shared by all pickers, so the
restyle applies everywhere at once.

## Design — style "A" (log-framed)

Drop the two elements that made the picker clash with the log lines: the
**bold-accent title** and the hard-coded **40-char `────` divider**. The
block becomes a compact, indented body under a subdued header, bracketed in
practice by the command's own log lines (e.g. `echo.i18n-pull: selecting
remote target` before, `target resolved` after).

Layout (header at col 0, body indented 2):

```
select remote target  (2/2)            ← title dim, counter faint
  filter ›                             ← own line; prompt "filter " faint + "› " accent
  ❯ develop        Ionos-…:/path        ← cursor + name accent, tail (host:path) dim
    habitta_prod   Ionos-…:/path        ← name fg, tail dim
  · ↑↓ move · enter select · ctrl+x quit ← help faint
```

- **No `────` divider, no blank line.** `chromeLines` drops from 6 to 4
  (title, filter, help, +1 safety).
- **Filter on its own line** (the fix to the earlier mockup): the textinput
  prompt becomes `faint("filter ") + accent("› ")`.
- **Two-tone rows**: split each label at its first run of 2+ spaces
  (`splitLabel`) → head (the name) rendered in fg / accent-bold (cursor) /
  Info (recent); tail (the `host:path` / full-name column) always dim. A
  single-word label (module name) has no tail — rendered whole. This gives
  the name/detail contrast for free on the columnar labels without changing
  the item data model.
- **Cursor** `❯ ` in the accent color (below).

## Design — stage-tinted accent

A new `accent lipgloss.Color` field on `fuzzyPicker` drives the cursor `❯`,
the selected/cursor name, and the filter `›`. Default is `palette.Accent`.
When the picker is opened from a context whose stage is known, the accent
is `palette.PromptColor(stage)` — green (dev), yellow (staging), red (prod) —
so a prod session's picker visibly turns red.

- **Where applied (stage known):** the module picker in `i18n-export` /
  `i18n-update` (`cfg.Stage`), the i18n-pull module picker (`prof.Stage`,
  known after the remote profile is read), and the connect user picker
  (`target.stage`).
- **Where NOT applied (stage unknown):** the connect / i18n-pull **target**
  picker. Each candidate target may be a different env, and its stage lives
  in that host's remote profile — unknowable without an SSH round-trip per
  target. These keep the default accent. (Per-target tinting is a possible
  future enhancement if target stage is ever cached locally.)

## Implementation

### `internal/cmd/picker.go`

- Add `accent lipgloss.Color` to `fuzzyPicker`.
- `setAccent(c)` (pointer receiver): sets `m.accent` and rebuilds the filter
  prompt (`faint("filter ") + fg(c)("› ")`). Called in `newFuzzyPicker` with
  `palette.Accent` as default.
- Rewrite `View()` per the layout above; add `splitLabel(s) (head, tail)`.
- Update `chromeLines` 6 → 4 and its comment.
- `runSingleFuzzyPickerStaged(title, available, palette, stage)` and
  `runFuzzyPickerStaged(...)`: like the existing runners but call
  `setAccent(palette.PromptColor(theme.StageFromString(stage)))`. The
  existing `runSingleFuzzyPicker` / `runFuzzyPicker` delegate with the
  default accent (empty stage) so non-staged callers are unchanged.

### Callers (thread the stage)

- `internal/cmd/i18n.go` `pickModuleSingle`: take a `stage` arg, pass it
  through; `RunI18nExport` / `RunI18nUpdate` pass `cfg.Stage`.
- `internal/cmd/i18n_pull.go`: the module picker (`runSingleFuzzyPicker
  "Module to pull translations for"`) becomes the staged variant with
  `prof.Stage`. The **target** picker stays default.
- `internal/cmd/connect.go` `pickConnectUser`: use the staged variant with
  `target.stage`.

## Dependencies

- none (reuses `theme.PromptColor` / `theme.StageFromString`, lipgloss).

## Verify when done

- [ ] All pickers render without the `────` divider and without a bold title;
      filter sits on its own `filter ›` line; rows are indented with a
      two-tone name/detail split.
- [ ] In a `prod` session, the module/user picker cursor + selection render
      red; staging yellow; dev green.
- [ ] The connect / i18n-pull target picker keeps the default accent (stage
      not yet known) and still works.
- [ ] Ctrl+X still quits from the restyled picker; Esc/Ctrl+C still cancel.
- [ ] Filtering, scrolling (`↑/↓ more`), multi-select checkboxes, and the
      empty `(no matches)` state still work.
- [ ] `go build/vet/test ./...` pass; `CHANGELOG.md` `[Unreleased]` gets an
      entry.
```
