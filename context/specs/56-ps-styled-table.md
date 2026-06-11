# Unit 56: styled `ps` table

## Goal

Replace `ps`'s raw `docker compose ps` passthrough with an Echo-styled,
aligned table (service / image / status / ports) that matches the app's
Odoo-log aesthetic — the same theme + column pattern as `modstate`.

## Design

Read the containers structurally via `docker compose ps --format json` and
render them with the session theme:

- Header row in bold accent (`palette.Accent`).
- Columns: `service` (fg), `image` (fg), `status` (semantic color), `ports`
  (dim). Column widths computed from the data (the `pad` helper, shared with
  `modstate`); `ports` is the free last column.
- **Status color** (`psStatusStyle`): health wins when present —
  `healthy`→ok (green), `unhealthy`→err (red), `starting`→warn (yellow);
  otherwise the lifecycle state — `running`→ok, `restarting`→warn,
  `exited`/`dead`/`removing`→err, `paused`/`created`→dim.
- **Ports** rendered compactly as `pub→target[/proto]`, comma-separated,
  skipping internal-only (unpublished) ports; `-` when none.
- Rows printed via `sess.print(Line{Kind:"table"})` (verbatim, bypassing the
  Odoo-log line parser), then a closing
  `echo.ps: containers listed count=N` log line — same shape as `modstate`.
- Empty result → `echo.ps: no containers running`.

**Never regress.** `docker compose ps --format json` exists on modern
compose, but if the command errors or the JSON can't be parsed, `ps` falls
back to streaming the raw `docker compose ps` output (the previous
behavior), so the command always works.

## Implementation

- `internal/docker/ps.go`: `PSContainer` / `PSPublisher` structs, `PSList`
  (runs `ps --format json`, captures output + stderr), `parsePSJSON`
  (handles both the JSON-array and newline-delimited-objects forms),
  `PSContainer.Ports()`.
- `internal/cmd/docker.go`: `PSList(opts)` wrapper for the styled path;
  `RunPS` (raw streaming) kept as the fallback.
- `internal/repl/ps.go`: `runPSTable` (fetch → render, fallback on error),
  `emitPSTable` (the table), `psStatusStyle`.
- `internal/repl/repl.go`: split the `case "ps", "logs"` dispatch — `ps`
  now calls `runPSTable`; `logs` keeps `readonlyFinalize`.

## Dependencies

- none (reuses `pad`, the session theme, `emitOdooLog`).

## Verify when done

- [ ] `ps` shows an aligned `service · image · status · ports` table with an
      accent header and a closing `containers listed count=N` line.
- [ ] A healthy container's status reads green, a stopped/exited one red, a
      starting one yellow.
- [ ] Published ports show as `pub→target`; internal-only ports are omitted
      (`-` when none).
- [ ] No containers up → `no containers running`.
- [ ] If `--format json` fails, `ps` falls back to raw streaming (no crash).
- [ ] `parsePSJSON` handles both the array and NDJSON forms (unit-tested).
- [ ] `go build/vet/test ./...` pass; registry cross-checks stay green;
      `CHANGELOG.md` `[Unreleased]` gets a `Changed` entry.
