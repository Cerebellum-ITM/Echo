# Unit 24: Flag highlight + flag autocomplete

## Goal

Extend the live REPL editing from Unit 21 to **flags**: color flag tokens
(`--force`, `-t`, …) in a distinct color, and add **Tab autocomplete of
flags** for the current command. A known flag of the typed command shows
in an accent color; an unknown/forwarded flag shows faint (never red, so
passthrough commands like `up`/`down`/`logs`/`connect` aren't falsely
flagged). Both features are powered by one new per-command flag registry.

## Design

### Per-command flag registry

A single source of truth next to `Registry` (`internal/repl/commands.go`),
listing the **user-facing** flags each command accepts (the internal ones
Echo builds itself — `-e`, `--no-http`, chrome flags — are excluded):

```go
var commandFlags = map[string][]string{
    "install":     {"--with-demo"},
    "update":      {"--all"},
    "test":        {"--update", "--tags"},
    "modules":     {"--config"},
    "i18n-export": {"--out"},
    "i18n-update": {"--force"},
    "db-backup":   {"--with-filestore"},
    "db-restore":  {"--as", "--force"},
    "db-drop":     {"--force"},
    "down":        {"--force"},
    "logs":        {"-t", "--no-follow", "-c", "--copy", "--all"},
    "connect":     {"--all", "--force"},
    "copy-last":   {"--errors"},
}
```

Commands absent from the map simply have no known flags (everything they
get is treated as unknown/faint — fine for `up`/`stop`/`ps`/shells/etc.).

### Highlight states (validate, never red)

- **command** (token 0): unchanged from Unit 21 — green / default / red.
- **known flag** (token starts with `-`, name ∈ `commandFlags[cmd]`):
  `palette.Accent`, bold.
- **unknown flag** (token starts with `-`, not known): `Faint(true)` —
  visibly a flag, but dim, signaling "not a recognized flag" without the
  aggressiveness of red (so forwarded flags read fine).
- **args / values** (non-flag tokens after the command): default style.

A flag written as `--tags=value` is validated on the part before `=`
(`--tags`), and the whole token is colored as a flag.

### Autocomplete (Tab on a `-` token)

Tab behavior (extends `handleTab`, reusing the 1-match / LCP / list logic
from Unit 13):

- First token, no space yet → complete the **command** (current behavior).
- Otherwise, if the **last token starts with `-`** and the first token is
  a command → complete against `commandFlags[firstToken]`:
  - 1 match → fill it + a trailing space.
  - several → fill the longest common prefix; a second Tab lists them.
- Last token isn't a flag (an arg/value), or the buffer ends in a space →
  no-op (we don't complete arbitrary values).

## Implementation

### `internal/repl/commands.go`

Add `commandFlags`. Extend the `init` guard to also assert every key of
`commandFlags` is a real command (in `Registry`).

### `internal/repl/commandhl.go`

- `flagState` (`flagKnown` / `flagUnknown`) + `classifyFlag(command, token)`
  (strip `=value`, look up in `commandFlags[command]`).
- `flagStyle(state, palette)` → Accent+bold for known, `Faint(true)` for
  unknown.
- `flagsWithPrefix(command, prefix)` for Tab.
- `lineStyles(buf, palette) []*lipgloss.Style` — one entry per rune giving
  the render style (nil = default). Walks tokens: token 0 → `commandStyle`,
  later `-`-tokens → `flagStyle`, rest → nil. Replaces the command-only
  per-rune logic currently inlined in `View()`.

### `internal/repl/lineinput.go`

- `View()` uses `lineStyles` to color every rune, still splicing the
  `textinput` cursor at `Position()` (blink preserved); empty buffer still
  falls back to `m.input.View()`.
- `handleTab()` refactored: a shared `tabComplete(matches, prefix, makeBuf)`
  helper runs the 1/LCP/list flow; command completion passes `makeBuf =
  identity`, flag completion passes `makeBuf` that swaps only the last
  token. Flag path triggers when the last token starts with `-` and a
  command precedes it.

## Dependencies

None new. Reuses `lipgloss`, `Registry`, `matchPrefix`,
`longestCommonPrefix`, `theme.Palette`.

## Verify when done

- [ ] Typing `db-restore --force` shows `--force` in the accent color
      (bold), with `db-restore` still green.
- [ ] An unknown flag (`update --nope`) renders faint, not red; `update`
      stays green and `--all` (known) is accent.
- [ ] Forwarded-flag commands (`down --whatever`) never show red flags.
- [ ] `--tags=:TestX` colors as a flag and validates on `--tags`.
- [ ] `db-restore --f<Tab>` completes to `--force `; `db-restore -<Tab><Tab>`
      lists `--as --force`.
- [ ] `logs -<Tab>` offers `-t --no-follow -c --copy --all`; a second Tab
      lists them when ambiguous.
- [ ] Tab on a non-flag arg (e.g. `install sa<Tab>`) is a no-op (no value
      completion); command Tab and ↑/↓ history still work.
- [ ] Cursor still blinks/positions correctly; all four themes use
      `palette.Accent` (no hardcoded color).
- [ ] Table tests for `classifyFlag`, `flagsWithPrefix`, and a
      `commandFlags`↔`Registry` consistency guard.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/repl/...` pass.
