# Unit 06: Fuzzy Picker

## Goal

Replace `huh.MultiSelect` for the module-selection flow with a custom
Bubble Tea model that behaves like `fzf`: the filter is always active
(no `/` prefix), Tab toggles selection, Enter confirms. The picker is
generic enough to be reused later for `db-list` (Unit 10 in the original
build plan).

This unit absorbs the part of Unit 10 (filterable-list) that affects
`modules`/`install`/`update`/`uninstall`.

## Design

### UX

```
Modules to install                                   (3/24)
❯ sale_

────────────────────────────────────────
❯ [×] sale_management
  [ ] sale_purchase
  [ ] sale_stock

type to filter · tab toggle · ↑↓ navigate · enter confirm · esc cancel
```

- Cursor starts on the filter input — typing always filters.
- The first matching item is highlighted; ↑/↓ navigates.
- Tab toggles the selection of the highlighted item; the cursor stays.
- Enter quits, returning the set of selected items (even if cursor is
  on the filter).
- Esc / Ctrl+C cancels and returns ErrCancelled.
- Empty selection on Enter is treated as cancel (returns ErrCancelled).
- Counter `(N/M)` shows visible / total.

### Filter

Substring case-insensitive match for v1. The filter input is a
`textinput.Model` from `bubbles/textinput`; the items list is rendered
manually below. No need for `bubbles/list` since the dataset is small
(tens of modules) and we want full layout control.

If/when we hit larger lists we can swap to fuzzy ranking (e.g.
`sahilm/fuzzy`) without changing callers.

### Theming

Uses the active palette directly via `lipgloss.NewStyle().Foreground(p.X)`:
- Title and selected-row name → `Accent` (bold)
- Cursor `❯` → `Accent2`
- Checkbox empty `[ ]` → `Faint`
- Checkbox filled `[×]` → `Success`
- Counter, no-match text → `Dim`
- Help footer → `Faint`

The picker does not use `BuildHuhTheme` since it is not a huh form.

## Implementation

### `internal/cmd/picker.go` (new)

```go
type fuzzyPicker struct {
    filter   textinput.Model
    items    []pickerItem
    visible  []int
    cursor   int
    title    string
    palette  theme.Palette
    canceled bool
}

type pickerItem struct {
    name     string
    selected bool
}

func newFuzzyPicker(title string, available []string, p theme.Palette) fuzzyPicker
func (m *fuzzyPicker) recompute()
func (m fuzzyPicker) Init() tea.Cmd
func (m fuzzyPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (m fuzzyPicker) View() string
func (m fuzzyPicker) selectedNames() []string

// runFuzzyPicker runs the picker and returns the selected items.
// ErrCancelled if user cancelled or empty.
func runFuzzyPicker(title string, available []string, p theme.Palette) ([]string, error)
```

### `internal/cmd/modules.go` updates

Replace the existing huh-based `pickModulesInteractive` so it delegates
to `runFuzzyPicker`. The `tabToggleKeymap` helper becomes unused — drop
it. `modules --config` (the addons-paths form) **keeps** the huh form
since it benefits from huh's labelled fields and theme.

```go
func pickModulesInteractive(opts ModulesOpts, title string) ([]string, error) {
    available := listAvailableModules(opts.Cfg, opts.Root)
    if len(available) == 0 {
        return nil, ErrNoModulesAvailable
    }
    return runFuzzyPicker(title, available, opts.Palette)
}
```

### REPL impact

None. The REPL already calls `RunInstall/RunUpdate/RunUninstall` which
delegate to `pickModulesInteractive` when args are empty.

## Dependencies

None new — `bubbles/textinput` and `bubbletea` are already in `go.mod`.

## Verify when done

- [ ] `go build ./...` passes.
- [ ] `install` (no args) opens the picker with the filter focused.
- [ ] Typing immediately filters; no `/` press needed.
- [ ] Tab toggles selection without leaving filter.
- [ ] Multiple selections persist across filter changes.
- [ ] Enter confirms; the command runs with the selected modules.
- [ ] Esc cancels with `init cancelled` style warn line.
- [ ] Empty selection on Enter is treated as cancel.
- [ ] Cursor and selection state survive resize (terminal width change).
