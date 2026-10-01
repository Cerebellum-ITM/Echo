# Unit 58: restyle fixes — logs colorize + modules color/icon

## Goal

Fix the two outputs from Unit 57 (`consistency-restyle`) that did not actually
meet that spec's own "Verify when done" checklist:

1. `logs --follow` is **not** Odoo-colorized like `up`/`update` — it renders as
   flat, uncolored text.
2. `modules` lists names in a single flat color with no per-item affordance.

Unit 57 documented these as done without verifying them live. This unit corrects
both, on branch `feat/58-restyle-logs-modules`.

## Diagnosis (why logs was broken)

`update` runs Odoo through `docker exec`, so it receives **raw** Odoo log lines
(`2026-… PID LEVEL logger: msg`) that `emitStreamLine` parses and colors.

`logs` runs `docker compose logs -f`, which prepends a per-line gutter
`<service>  | ` to every line. `emitStreamLine` expects each line to **start**
with the Odoo timestamp, so the gutter breaks every parser
(`parseComposeProgress`, `parseLooseSeverity`, `logColorer.classify`) and each
line falls through to the uncolored default. `up`/`down` are unaffected because
they run `up -d`/`down` (detached) and only emit compose control-plane lines,
which `parseComposeProgress` already handles.

## Design

### `logs` — make it render byte-identical to `update`

Two independent reasons `logs` did not match `update`:

**(1) Compose gutter.** `docker compose logs -f` prefixes every line with
`<service>  | `, which breaks the Odoo prefix regex. Fix: pass `--no-log-prefix`
to both compose log invocations (`Logs` and `LogsFollow`) so the container's
stdout comes through raw.

**(2) Embedded ANSI.** `update`/`install` run Odoo under `exec -T` (no TTY →
plain logs). `docker compose logs` instead replays whatever the container
*stored*, which carries Odoo's ColoredFormatter SGR codes when Odoo ran attached
to a TTY. Those codes make `formatOdooLine`/`classifyOdooLog` miss, so the line
falls through to a verbatim print showing docker's native colors (logger not
pastel-colored, timestamp not dimmed) — visibly different from `update`. Fix:
`emitStreamLine` strips ANSI (`stripANSISeq`) before parsing, the same treatment
the `shell` transform already applies. For `update` (no ANSI) it is a no-op, so
both paths now go through the identical per-segment formatter.

With both fixes, `logs` and `update` share one normalized path
(`emitStreamLine → stripANSISeq → renderLogLine`) and render the same input
identically.

- Single Odoo container (the default, `services = [OdooContainer]`): clean,
  fully colorized stream.
- `logs --all` (multiple services): lines interleave without per-service
  attribution — an accepted trade-off, consistent with Unit 57's stated
  "Echo's colors replace docker's native ANSI".
- Ctrl+C behaviour is unchanged (SIGINT → context cancel → clean exit).
- `--no-follow` and `--copy` go through the same `Logs`/stream path and also
  benefit; their existing behaviour is otherwise unchanged.

Assumption: Docker Compose v2 (`--no-log-prefix` available), already implied by
Unit 57.

### `modules` — per-item icon + color

Replace the single-color `renderMatchList(found, styles.Out)` render with a
dedicated module renderer that prefixes each module with the nerd-font glyph
`cod-package` () and colors each item, while keeping the terminal-width
wrapping and the closing `echo.modules: modules listed count=N` line.

- Glyph: `` (`cod-package`, U+EB29), one space before the name.
- The icon carries the accent/ok style; the name stays readable in the list
  style — a per-item style, so the wrapping cannot be a single outer
  `.Render` over the whole block (that resets per-item ANSI).
- `renderMatchList` is **not** modified (the fuzzy picker shares it). A new
  `renderModuleList` in `modules_list.go` owns the icon+color+wrap logic;
  width math uses `lipgloss.Width` on the visible (icon+name) cell.
- Empty / `--config` paths unchanged (`no modules` hint, addons-path picker).

## Implementation

- `internal/docker/compose.go`: add `--no-log-prefix` to the arg lists in
  `Logs` and `LogsFollow`.
- `internal/repl/repl.go`: `emitStreamLine` strips ANSI (`stripANSISeq`) on
  entry, before the compose/loose-severity/classify parsing, so ANSI-laden
  `logs` lines route through `formatOdooLine` like `update`.
- `internal/repl/modules_list.go`: add `renderModuleList(found, …)` (icon +
  per-item color + width wrap); `emitModulesList` calls it instead of
  `renderMatchList`. Define the package glyph as a named const.
- `internal/repl/lineinput.go`: `renderMatchList` untouched (picker keeps it).

## Dependencies

- Docker Compose v2 (`--no-log-prefix`).
- Reuses `emitStreamLine`, `emitOdooLog`, `lipgloss.Width`, the palette styles.

## Verify when done

- [ ] `logs` (follow) lines are Odoo-colorized exactly like `up`/`update`
      (timestamp/level/logger styled, levels colored); verified live against a
      running container, not just by reading code.
- [ ] Ctrl+C stops the follow cleanly with no error frame.
- [ ] `logs --no-follow` / `--copy` still work and are colorized.
- [ ] `modules` shows each name prefixed by the `` glyph and colored; the
      list still wraps to terminal width and closes with `modules listed
      count=N`; `modules --config` still opens the addons-path picker.
- [ ] `go build/vet/test ./...` pass; `CHANGELOG.md` `[Unreleased]` gets a
      `Fixed` entry (logs) and a `Changed` entry (modules).
