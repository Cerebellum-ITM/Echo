# Unit 21: Command Highlight (live REPL command validation)

## Goal

While typing in the Echo REPL, color the **first token** (the command word)
in real time to signal whether it is a valid command — green when it
matches a command exactly, red when it can no longer become one, and the
normal text color while it is still a valid prefix. Same idea as
`fish` / `zsh-syntax-highlighting`. Arguments after the command keep the
default color.

## Design

### Scope: command token only

Only the first whitespace-delimited token is recolored. Everything after
the first space (flags, module names, paths) renders in the default text
style. This matches the user's choice of "solo el comando (estilo fish)".

### Three validity states

The classification is driven by the existing command `Registry` and
`matchPrefix` (Unit 13), so it stays automatically in sync with the real
command set:

| State | Condition | Style |
|---|---|---|
| **valid** | token is an exact entry in `Registry` (e.g. `install`) | `palette.Success` (green), bold |
| **typing** | not exact, but `matchPrefix(token)` is non-empty (e.g. `ins` → `install`) | default text style (no recolor) |
| **invalid** | `matchPrefix(token)` is empty (e.g. `xyz`) | `palette.Error` (red) |

The "typing" state is deliberately neutral (not red) so the line does not
flash red while the user is mid-word — only a token that can no longer be
any command turns red. `exit` / `quit` count as valid (they are handled
in `Start`, not in `dispatch`); include them in the validity check even
though they are not in `Registry`.

### Color tokens

From `internal/theme` `Palette` (defined for all four themes), referenced
by name — no raw hex:

- valid → `palette.Success`
- invalid → `palette.Error`
- typing / args → the input's existing default `TextStyle` (unchanged)

### Cursor & blink preserved

`bubbles/textinput` only supports a single uniform `TextStyle`, so the
command-token coloring requires rendering the line ourselves in
`lineModel.View()` instead of returning `m.input.View()` verbatim.

Two facts make this tractable without reimplementing the whole widget:

1. Echo never sets `textinput.Width` (it stays `0`), so there is **no
   horizontal scroll window** to reproduce — the full value is always
   shown. (`textinput.View` itself shortcuts when `Width <= 0`.)
2. The embedded `textinput.Model` keeps owning the cursor: `lineModel.Update`
   still delegates non-special keys to `m.input.Update`, so
   `m.input.Cursor` keeps blinking. The custom `View()` reuses
   `m.input.Cursor` to draw the caret, so blink and focus behavior are
   unchanged.

## Implementation

### `internal/repl/commandhl.go` — new file

Validity classifier + styled-line builder, kept separate from the input
plumbing for unit testing.

```go
// commandValidity reports the highlight state of the first token.
type cmdState int
const (
    cmdTyping  cmdState = iota // neutral: still a valid prefix (or empty)
    cmdValid                   // exact command match
    cmdInvalid                 // cannot become any command
)

// classifyCommand inspects the first token of buf.
func classifyCommand(token string) cmdState {
    if token == "" {
        return cmdTyping
    }
    if isCommandName(token) {
        return cmdValid
    }
    if len(matchPrefix(token)) > 0 {
        return cmdTyping
    }
    return cmdInvalid
}

// isCommandName is an exact lookup over Registry plus exit/quit.
func isCommandName(name string) bool { ... }
```

- Add `isCommandName` backed by a `map[string]bool` built once from
  `Registry` + `{"exit","quit"}` (a package-level var initialised in
  `init`, mirroring the existing `Registry` guard in `commands.go`).
- `firstToken(buf)` returns the leading run of non-space characters and
  the byte index where it ends.

### `internal/repl/lineinput.go` — custom `View()`

Replace `func (m lineModel) View() string { return m.input.View() }` with
a renderer that:

1. If the value is empty → return `m.input.View()` (placeholder + cursor,
   today's behavior).
2. Split into `token` + `rest` at the first space (`rest` includes the
   space).
3. Pick the command style from `classifyCommand(token)` and the active
   palette (the model must carry the palette — extend `lineModel` and
   `newLineModel`/`readLine` to receive `theme.Palette`, threaded from
   `Start`).
4. Build the styled string: styled `token` + default-styled `rest`,
   then splice the cursor at `m.input.Position()` using `m.input.Cursor`
   (set its char to the rune at the cursor and render; at end-of-line the
   cursor renders a trailing space cell, matching textinput).
5. Prepend `m.input.Prompt`.

Keep all key handling (`Update`, history, `handleTab`) untouched — only
`View()` and the constructor signature change.

### Wiring

- `lineModel` gains a `palette theme.Palette` field.
- `newLineModel(prompt, history, info, palette)` and
  `readLine(prompt, history, info, palette)` gain the palette param.
- `repl.go` `Start` already has the palette (`sess.palette`); pass it into
  `readLine`.

## Dependencies

None new. Uses `github.com/charmbracelet/lipgloss` (already present),
`bubbles/textinput` (already present), and the existing `Registry` /
`matchPrefix` / `theme.Palette`.

## Verify when done

- [ ] Typing `install` shows the word in green; `xyz` shows it in red;
      `ins` (prefix of `install`) stays the default color, not red.
- [ ] After the first space, arguments (`install sale --with-demo`) keep
      the default color — only `install` is green.
- [ ] `db-` stays neutral (prefix of `db-backup`/`db-restore`/…); `db-list`
      turns green.
- [ ] `exit` and `quit` highlight green.
- [ ] The cursor still blinks and sits at the correct position, including
      at end-of-line and after history (↑/↓) recall.
- [ ] Tab completion (Unit 13) and history navigation still work
      unchanged; the match list still renders.
- [ ] Highlighting is correct under all four themes (colors come from
      `palette.Success` / `palette.Error`, never hardcoded).
- [ ] `classifyCommand` has table tests (valid / typing / invalid /
      empty / `exit`).
- [ ] `go build ./...`, `go vet ./...`, and `go test ./internal/repl/...`
      pass.
