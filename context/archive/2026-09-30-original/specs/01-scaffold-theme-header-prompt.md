# Unit 01: Scaffold + Theme + Header + Prompt

## Goal

Produce a working `echo` binary that: initializes a Go module with Charm v2
dependencies, renders the two-column startup header using the `charm` theme,
and drops into an interactive prompt that accepts the `ls` command (printing
the current directory listing) and `exit`/`Ctrl+D` to quit.

## Design

### Header

Two-column layout. Full terminal width. ~8 lines tall. Rendered once at startup
and after `clear`.

```
─── Echo v0.1.0 ──────────────────────────── (accent color, bold)
│                          │                                      │
│  Welcome back {user}!    │  Tips for getting started            │
│                          │  Run help to see all commands        │
│    [ASCII placeholder]   │  ────────────────────────────        │
│                          │  What's new                          │
│  charm · dev             │  · First release — header + prompt   │
│  ~/path/to/project       │                                      │
─────────────────────────────────────────────────────────────────

```

Styling per token:
- Top/bottom border: `accent`
- Left/right outer border: `faint`
- Column separator `│`: `faint`
- "Echo v0.1.0": `accent` bold
- "Welcome back {user}!": `fg` bold
- ASCII logo placeholder: `accent` (simple box or `[echo]` text for unit 01)
- Context line (`charm · dev`): `dim`
- Path line: `dim`
- "Tips for getting started": `warning` bold (label style)
- Tip content: `fg`
- Divider between tips sections: `faint`
- "What's new": `warning` bold (label style)
- Changelog bullet: `dim`

### Prompt

```
echo-01 [dev/17.0]:~$ 
```

- `echo-01`: `success` bold (dev stage)
- ` [dev/17.0]`: `success` bold
- `:`: `fg`
- `~`: `info`
- `$ `: `fg`

For unit 01, project name = `echo`, id = `01`, stage = `dev`, version = `17` (hardcoded defaults).

### Output Lines

Each command output line is a `Line{Kind, Text}` rendered with the matching style:
- `ls` output: `out` kind (fg color)
- Unknown command: `warn` kind — `unknown command: foo — try help`
- `$ ls`: `info` kind (echo the command before running)

## Implementation

### `go.mod` and dependencies

```
module github.com/yourusername/echo

go 1.22

require (
    github.com/charmbracelet/lipgloss v1.1.0   // v2 API
    github.com/charmbracelet/bubbles v0.20.0
    github.com/charmbracelet/bubbletea v1.3.4
)
```

Run `go mod tidy` after writing `go.mod`.

### `internal/theme/theme.go`

Define `Palette` struct with all 10 token fields (`Bg`, `Fg`, `Dim`, `Faint`,
`Accent`, `Accent2`, `Success`, `Warning`, `Error`, `Info`) as `lipgloss.Color`.

Define `var Charm`, `var Hacker`, `var Odoo`, `var Tokyo` — all four palettes
with the hex values from `ui-context.md`.

Define `Stage` type and constants `StageDev`, `StageStaging`, `StageProd`.

Define `Styles` struct:
```go
type Styles struct {
    Out, Dim, Faint, Info, Ok, Warn, Err, Accent, Label lipgloss.Style
    Project, Bracket, Tilde, Dollar                     lipgloss.Style
}
```

Define `func New(p Palette, stage Stage) Styles` — builds all styles from the palette.
Project/Bracket color comes from `p.PromptColor(stage)`.

Define `func (p Palette) PromptColor(s Stage) lipgloss.Color` — returns `p.Error`
for prod, `p.Warning` for staging, `p.Success` for dev.

### `internal/banner/header.go`

Define `func Render(s theme.Styles, version, user, themeName, stage, path string) string`
that returns the full header string ready to print.

Use `lipgloss.JoinHorizontal` to create the two-column layout.
Use `lipgloss.NewStyle().Border(lipgloss.NormalBorder())` or manual border
characters for the outer frame.

Left column content (each a `lipgloss.Style.Render(text)` call):
- Greeting: `s.Out.Bold(true).Render("Welcome back " + user + "!")`
- Logo placeholder: `s.Accent.Render("[echo]")` (3–4 lines tall)
- Context: `s.Dim.Render(themeName + " · " + stage)`
- Path: `s.Dim.Render(path)`

Right column content:
- "Tips for getting started": `s.Label.Render("Tips for getting started")`
- Tip text: `s.Out.Render("Run help to see all commands")`
- Divider: `s.Faint.Render(strings.Repeat("─", colWidth))`
- "What's new": `s.Label.Render("What's new")`
- Bullet: `s.Dim.Render("· First release — header + prompt")`

Top bar: `s.Accent.Bold(true).Render("─── Echo v" + version + " " + strings.Repeat("─", ...))`

### `internal/repl/repl.go`

Define `type Line struct { Kind, Text string }`.

Define `func Start(s theme.Styles, project, stage, version string)` — the REPL loop:

1. Print the header (call `banner.Render`).
2. Loop:
   a. Print the prompt via `RenderPrompt(s, project, stage, version)`.
   b. Read a line from stdin using `bufio.NewReader(os.Stdin).ReadString('\n')`.
   c. Trim whitespace. If empty, loop.
   d. If `exit` or EOF: break.
   e. If `ls`: run `exec.CommandContext(ctx, "ls", "-la")`, capture output, print each line as `out` kind.
   f. Else: print `Line{Kind:"warn", Text:"unknown command: " + input + " — try help"}`.
3. Print a `dim` goodbye line and exit.

Use `bufio.Scanner` or `bufio.Reader` for input — not a full bubbletea model.
This is a plain interactive CLI loop for unit 01.

Define `func RenderPrompt(s theme.Styles, project, stage, version string) string`:
```
s.Project.Render(project) +
s.Out.Render(" [") +
s.Bracket.Render(stage + "/" + version + ".0") +
s.Out.Render("]:") +
s.Tilde.Render("~") +
s.Dollar.Render("$ ")
```

### `main.go`

```go
package main

import (
    "os"
    "os/user"
    "github.com/yourusername/echo/internal/theme"
    "github.com/yourusername/echo/internal/repl"
)

func main() {
    palette := theme.Charm
    stage   := theme.StageDev
    styles  := theme.New(palette, stage)

    u, _ := user.Current()
    username := u.Username
    cwd, _ := os.Getwd()

    repl.Start(styles, "echo", "01", string(stage), "17", username, cwd)
}
```

Pass `username` and `cwd` through to `banner.Render` inside `repl.Start`.

## Dependencies

- `github.com/charmbracelet/lipgloss` v1.1.0 — all styling
- `github.com/charmbracelet/bubbles` v0.20.0 — pulled in for unit 02+, add now to avoid module churn
- `github.com/charmbracelet/bubbletea` v1.3.4 — same reason

## Verify when done

- [ ] `go build ./...` produces no errors
- [ ] `go vet ./...` produces no warnings
- [ ] Running `./echo` renders the two-column header without layout breakage at 120-col terminal
- [ ] The prompt appears below the header with correct stage color (green for dev)
- [ ] Typing `ls` prints the current directory listing in `fg` color
- [ ] Typing an unknown command prints a yellow warning line
- [ ] Typing `exit` quits cleanly with a goodbye line
- [ ] `Ctrl+D` (EOF) also quits cleanly
- [ ] No hardcoded hex strings outside of `internal/theme/theme.go`
