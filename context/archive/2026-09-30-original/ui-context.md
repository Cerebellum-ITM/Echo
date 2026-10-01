# UI Context

## Theme

Dark only. No light mode. Four named themes — `charm` (default), `hacker`, `odoo`, `tokyo`.
Each theme defines the same semantic token slots so all rendering code is theme-agnostic.
Colors are truecolor hex strings passed to `lipgloss.Color("#…")`.
The active theme is stored in `.echo.toml` and loaded at startup.

## Color Tokens (per theme)

All themes expose the same `Palette` struct. Rendering code references tokens by name, never by hex.

### `charm` — purple/pink (default)

| Token     | Hex       | Semantic use                                  |
|-----------|-----------|-----------------------------------------------|
| `bg`      | `#13111c` | Terminal background                           |
| `fg`      | `#e8e3f5` | Primary text                                  |
| `dim`     | `#8b80a8` | Secondary text, metadata, paths               |
| `faint`   | `#5a5074` | Subtle borders, separators                    |
| `accent`  | `#b794f6` | Brand identity, banner, headlines             |
| `accent2` | `#f687b3` | Secondary accent, banner segments             |
| `success` | `#68d391` | OK, dev stage, checkmarks                     |
| `warning` | `#f6ad55` | Warnings, staging stage, label titles         |
| `error`   | `#fc8181` | Errors, prod stage, destructive actions       |
| `info`    | `#63b3ed` | Executed commands (`$ …`), tilde in prompt    |

### `hacker` — classic CRT green

| Token     | Hex       |
|-----------|-----------|
| `bg`      | `#0a0e0a` |
| `fg`      | `#d4f4d4` |
| `dim`     | `#7a9a7a` |
| `faint`   | `#4a5a4a` |
| `accent`  | `#39ff14` |
| `accent2` | `#00d9ff` |
| `success` | `#39ff14` |
| `warning` | `#ffd700` |
| `error`   | `#ff4444` |
| `info`    | `#00d9ff` |

### `odoo` — official Odoo purple

| Token     | Hex       |
|-----------|-----------|
| `bg`      | `#1a1322` |
| `fg`      | `#f0e9f5` |
| `dim`     | `#a094b3` |
| `faint`   | `#6b5e7d` |
| `accent`  | `#a47bc4` |
| `accent2` | `#e8a87c` |
| `success` | `#7bcf9f` |
| `warning` | `#e8a87c` |
| `error`   | `#e87878` |
| `info`    | `#7ba3c4` |

### `tokyo` — Tokyo Night

| Token     | Hex       |
|-----------|-----------|
| `bg`      | `#1a1b26` |
| `fg`      | `#c0caf5` |
| `dim`     | `#7982a9` |
| `faint`   | `#565f89` |
| `accent`  | `#7aa2f7` |
| `accent2` | `#bb9af7` |
| `success` | `#9ece6a` |
| `warning` | `#e0af68` |
| `error`   | `#f7768e` |
| `info`    | `#7dcfff` |

## Output Line Kinds → Styles

Every printed line is one of these kinds, mapped to a `lipgloss.Style`:

| Kind     | Token used  | Example                              |
|----------|-------------|--------------------------------------|
| `out`    | `fg`        | Neutral command output               |
| `dim`    | `dim`       | Paths, metadata, hints               |
| `faint`  | `faint`     | Separators, decorative lines         |
| `info`   | `info`      | `$ docker compose up -d`             |
| `ok`     | `success`   | `✔ container started`                |
| `warn`   | `warning`   | `WARNING cron took 11.4s`            |
| `err`    | `error`     | `ERROR column does not exist`        |
| `accent` | `accent`    | Identity, section headlines          |
| `label`  | `warning`   | Block titles ("Overview of commands")|
| `banner` | multi-token | ASCII logo with gradient segments    |

## Prompt Structure

```
{project}-{id} [{stage}/{version}.0]:~$ 
^^^^^^^^^^^^^  ^^^^^^^^^^^^^^^^^^^^  ^  ^
stageColor     stageColor            info  fg
```

Stage → token mapping:
- `dev` → `success` (green — safe)
- `staging` → `warning` (yellow — shared env)
- `prod` → `error` (red — double-check)

The project name and bracket use the same stage color and are bold.
The tilde uses `info`. The `$` uses `fg`.

## Header Layout

Two-column layout inspired by Claude Code's startup header.
Rendered once at startup (and after `clear`). Compact — fits in ~8 terminal lines.

```
┌─ Echo v{version} ──────────────────────────────────────────────────────┐
│                            │                                           │
│   Welcome back {user}!     │  Tips for getting started                 │
│                            │  Run help to see all commands             │
│   {ASCII logo}             │  ──────────────────────────               │
│                            │  What's new                               │
│   {theme} · {stage}        │  ...changelog bullet 1                    │
│   ~/path/to/project        │  ...changelog bullet 2                    │
└────────────────────────────────────────────────────────────────────────┘
```

- Top border: `accent` color, dashed style (`─`)
- Left column: welcome greeting, ASCII logo, context line (theme · stage · path)
- Right column: "Tips" header in `warning`/`label` style, divider in `faint`, "What's new" in `label`
- Both columns separated by a `faint` vertical rule `│`

## ASCII Logos

Four logos selectable via `logo` command: `echo`, `planet`, `python`, `anchor`.
Each logo is `[]struct{ Text, Token string }` — segments rendered with the token's style.
Token values: `accent`, `accent2`, `info`, `success`, `warning`, `error`, `fg`, `dim`.

## Typography

Terminal monospace only — no font loading. All sizing is via terminal lines/columns.
No custom fonts. Use lipgloss `Bold(true)` and `Faint(true)` for weight variation.

## Component Library

Charm suite v2 exclusively:
- `lipgloss` — all styling, borders, layout (columns, padding, margin)
- `bubbles/textinput` — REPL prompt input
- `bubbles/list` — filterable lists (`modules`, `db-list`)
- No web framework, no Tailwind, no shadcn

## Layout Patterns

- **Header**: rendered as a lipgloss two-column join, full terminal width, fixed at top
- **Output buffer**: scrolling text below the header, each line a typed `Line{Kind, Text}`
- **Prompt**: last line of output, re-rendered after each command completes
- **Lists**: full-screen takeover via bubbles/list when a filterable list command runs; `q`/`Esc` returns to prompt
- **No split panes, no sidebars, no modals**: linear terminal output only
