# Unit 13: history-autocomplete

## Goal

Complete the partially-done history+autocomplete unit by adding **Tab
autocomplete on top-level command names**. The ↑/↓ history ring with
persistence in `~/.config/echo/history` is already implemented
(`internal/repl/lineinput.go`, `internal/repl/history.go`). This unit
adds Tab behaviour modelled after bash readline and introduces a
single source of truth for the command registry, eliminating the
existing duplication between `dispatch()` and `runHelp()`.

## Design

### Tab UX (bash-style)

Pressing `Tab` on a non-empty prefix runs prefix matching against the
registry and falls into one of these branches:

| Matches | Behaviour                                                                |
|---------|--------------------------------------------------------------------------|
| 0       | Beep is suppressed (we don't ring the terminal). Nothing happens.        |
| 1       | The buffer is replaced by the full command, followed by a single space.  |
| >1      | Buffer is extended to the **longest common prefix** of the matches. If the LCP equals the current buffer (no progress possible), a second consecutive `Tab` prints the match list below the prompt, then re-renders the prompt with the current buffer intact. |

Pressing `Tab` on an **empty buffer** is ignored — no listing, no
literal tab inserted. This matches the explicit decision in the
mockup.

Any key other than `Tab` resets the "second-Tab" latch, so the listing
only fires on two *consecutive* Tabs.

### Matching

**Strict prefix, case-sensitive.** All Echo commands are lowercase
already; case-insensitive matching would add complexity without
helping the user, and fuzzy matching is reserved for the picker
(`runSingleFuzzyPicker`) where the candidate space is large and
user-defined. Command names are a small, curated set — prefix is
clearer and avoids surprises.

Only the **first token** of the buffer is autocompleted. If the user
has already typed a space (e.g. `install sa<Tab>`), Tab is a no-op.
Argument completion (modules, DBs, files) is out of scope for this
unit.

### Registry as single source of truth

A new file `internal/repl/commands.go` declares the canonical ordered
list of command names. Both `dispatch()` and `runHelp()` consume it:

- `dispatch()` only needs the **set** of valid names to route. Today
  it's a `switch` with hardcoded literals; the unit keeps the switch
  shape (no reflection or table-driven dispatch — overkill) but
  introduces an assertion in tests / startup that every name in the
  registry is reachable from the switch and vice-versa.
- `runHelp()` already groups commands into labelled sections. The
  registry stays a flat `[]string`; the help table remains a separate
  structure in `repl.go`. We only enforce that **every name in the
  help table is in the registry** (and the other way around) — same
  invariant, asserted at startup.

The registry order **must match** the order commands first appear in
the help output, so that the match list printed under the prompt is
predictable and groups naturally (Project → Modules → Database →
Shell → Docker → meta).

### Match list rendering

When the second consecutive Tab fires, Echo:

1. Prints a newline.
2. Prints the matching command names separated by two spaces, wrapped
   to the terminal width (use `lipgloss.Width` for cell-aware width;
   fall back to 80 when width is unknown). All names share the `Info`
   style from the active palette.
3. Re-renders the prompt with the current buffer and cursor at the
   end.

The Bubble Tea text input model needs to **emit the lines above the
prompt without leaving them inside the input view**. The cleanest path
is: capture the list as a `string`, store it on `lineModel` as
`pendingPrint`, and return a `tea.Cmd` (`tea.Println(pendingPrint)`)
which Bubble Tea renders above the inline view without disturbing the
input.

## Implementation

### `internal/repl/commands.go` — new file

```go
package repl

// Registry is the canonical, ordered list of top-level command names
// recognised by the REPL. The order matches the help output and
// determines the order of the match list rendered on a double-Tab.
var Registry = []string{
    // Project
    "init", "reset",
    // Modules
    "install", "update", "uninstall", "modules",
    // Database
    "db-backup", "db-restore", "db-drop", "db-list",
    // Shell
    "bash", "psql", "shell",
    // Docker
    "up", "down", "restart", "ps", "logs",
    // Meta
    "clear", "help", "exit", "quit",
}

// matchPrefix returns the entries in Registry that start with prefix,
// preserving Registry order. prefix == "" returns nil (Tab on empty
// buffer is a no-op).
func matchPrefix(prefix string) []string { ... }

// longestCommonPrefix returns the longest string that is a prefix of
// every entry in matches. Returns "" for an empty slice.
func longestCommonPrefix(matches []string) string { ... }
```

### `internal/repl/lineinput.go` — extend `lineModel`

Add two fields:

```go
type lineModel struct {
    // ...existing fields...
    lastWasTab   bool   // true if the previous key was Tab (for double-Tab listing)
    pendingPrint string // set when a list needs to be printed above the prompt
}
```

Handle the `tab` key inside `Update`:

```go
case "tab":
    buf := m.input.Value()
    // Only complete the first token; bail if a space exists already.
    if buf == "" || strings.Contains(buf, " ") {
        m.lastWasTab = false
        return m, nil
    }
    matches := matchPrefix(buf)
    switch len(matches) {
    case 0:
        m.lastWasTab = false
        return m, nil
    case 1:
        full := matches[0] + " "
        m.input.SetValue(full)
        m.input.SetCursor(len(full))
        m.lastWasTab = false
        return m, nil
    default:
        lcp := longestCommonPrefix(matches)
        if lcp != buf {
            m.input.SetValue(lcp)
            m.input.SetCursor(len(lcp))
            m.lastWasTab = true
            return m, nil
        }
        if m.lastWasTab {
            m.pendingPrint = renderMatchList(matches)
            m.lastWasTab = false
            cmd := tea.Println(m.pendingPrint)
            m.pendingPrint = ""
            return m, cmd
        }
        m.lastWasTab = true
        return m, nil
    }
```

Any other key path (existing `enter`, `up`, `down`, `ctrl+c`,
`ctrl+d`, character input) sets `m.lastWasTab = false` before
returning, so the latch only persists across two consecutive Tabs.

`renderMatchList(matches []string) string` lives in `lineinput.go`:
joins names with two-space separators, wraps to terminal width
(`golang.org/x/term`-free fallback: read `$COLUMNS`, default 80),
styles them via a passed-in `Info` lipgloss style. Because `lineModel`
doesn't currently carry styles, add `infoStyle lipgloss.Style` to it
and pass it through from `newLineModel` / `readLine`.

### `internal/repl/repl.go` — consume the registry

- `readLine` call in `Start` now passes `sess.styles.Info`.
- Add a startup assertion (called once from `Start` before the loop):
  ```go
  func init() {
      seen := map[string]bool{}
      for _, name := range Registry {
          if seen[name] {
              panic("repl: duplicate command in Registry: " + name)
          }
          seen[name] = true
      }
  }
  ```
- Add a test-only invariant: every literal in the `dispatch` switch
  and every entry in the help table belongs to `Registry`. Encoded as
  a Go test in `internal/repl/registry_test.go` (see Verify below).
- `runHelp` keeps its grouped structure but builds the per-section
  entries from a local `helpEntries []helpEntry` where each entry has
  `Name string`. A `TestHelpCoversRegistry` test asserts the set of
  names equals `Registry`.

### `internal/repl/registry_test.go` — new file

Two tests:

1. `TestRegistryUnique` — no duplicates.
2. `TestRegistryMatchesHelp` — collect the names from the help table
   (expose `helpCommandNames()` as a package-private helper that
   returns the flat slice) and assert it equals `Registry` as sets.
3. `TestRegistryMatchesDispatch` — parse `dispatch` would be brittle;
   instead, factor the routing into a `routeName(string) bool` helper
   (or a static `dispatchableNames` slice) and assert it equals the
   registry. Lowest-cost option: declare a private
   `dispatchNames = []string{...}` next to `dispatch` and have the
   `switch` build from it via a generated map — but to keep diff
   small, just maintain `dispatchNames` by hand and let the test
   guard it.

A few unit tests for `matchPrefix` and `longestCommonPrefix`:

- `matchPrefix("")` returns `nil`.
- `matchPrefix("db-")` returns `["db-backup", "db-restore", "db-drop", "db-list"]` in that order.
- `matchPrefix("zz")` returns `nil`.
- `longestCommonPrefix(["db-backup", "db-restore"])` returns `"db-re"` is wrong → returns `"db-"` (common prefix only).
- `longestCommonPrefix(["install"])` returns `"install"`.
- `longestCommonPrefix(nil)` returns `""`.

## Dependencies

None new. Uses existing `github.com/charmbracelet/bubbletea`
(`tea.Println`), `github.com/charmbracelet/lipgloss`, and stdlib.

## Verify when done

- [ ] Typing `db-<Tab>` extends the buffer to `db-` (already there) and a second Tab prints the four `db-*` commands under the prompt; the prompt is re-rendered with `db-` and cursor at the end.
- [ ] Typing `in<Tab>` completes to `install ` (with trailing space). Typing `ini<Tab>` completes to `init `.
- [ ] Typing `up<Tab>` completes to `up ` (only one match: `up`).
- [ ] Typing `zz<Tab>` is a no-op (no beep, no change).
- [ ] Tab on empty buffer is a no-op.
- [ ] Tab after a space (e.g. `install sa<Tab>`) is a no-op.
- [ ] Any non-Tab key after a single Tab resets the double-Tab latch (a Tab → letter → Tab does **not** list).
- [ ] ↑/↓ history navigation still works unchanged; history persistence still writes to `~/.config/echo/history`.
- [ ] `Registry`, `dispatch`, and `runHelp` are kept consistent by the new tests; removing or adding a command in one place without the others fails `go test ./internal/repl/...`.
- [ ] `go build ./...`, `go vet ./...`, and `go test ./internal/repl/...` all pass.
