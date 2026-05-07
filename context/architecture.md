# Architecture Context

## Stack

| Layer      | Technology                          | Role                                              |
| ---------- | ----------------------------------- | ------------------------------------------------- |
| Language   | Go                                  | Binary CLI, single static executable              |
| CLI/REPL   | Charm `huh` + `lipgloss` + `glamour`| Prompt loop, styled output rendering              |
| Input      | Charm `bubbles/textinput`           | Readline-style input with history and autocomplete|
| Lists      | Charm `bubbles/list`                | Filterable lists (modules, db-list, etc.)         |
| Theming    | `lipgloss` v2                       | Semantic color tokens, all terminal output styling|
| Config     | `~/.config/echo/` (TOML)            | Global prefs + per-project container/db/version   |
| Docker     | `os/exec` → `docker compose`        | Lifecycle commands, streaming stdout/stderr       |
| Odoo CLI   | `os/exec` inside container          | Module install/update/test/shell                  |
| Database   | `os/exec` → `psql`, `pg_dump`       | Backup, restore, list, drop                       |

## System Boundaries

- `internal/theme/` — palette definitions (charm/hacker/odoo/tokyo), `Styles` struct, prompt color logic
- `internal/detect/` — reads `docker-compose.yml` to auto-detect Odoo version and container names
- `internal/cmd/` — one file per command group (`docker.go`, `modules.go`, `db.go`, `i18n.go`, `shells.go`, `tests.go`); each exposes `Run(ctx, args) (<-chan Line, error)`
- `internal/repl/` — the interactive prompt loop: reads input, dispatches to cmd/, streams Line output, manages history
- `internal/banner/` — ASCII art logos (echo, planet, python, anchor) with per-segment color tokens
- `internal/config/` — load/save global and per-project config under `~/.config/echo/`
- `main.go` — entry point: detect project, load config, render header, start REPL

## Storage Model

- **`~/.config/echo/global.toml`**: user-level preferences shared across all projects — active theme, logo. Written by `theme` and `logo` commands.
- **`~/.config/echo/projects/<sha256-of-abs-path>.toml`**: per-project config, keyed by the SHA-256 of the project's absolute path. Contains:
  - `odoo_version` — e.g. `"17"`, `"18"`, `"19"`
  - `odoo_container` — Docker container name for Odoo
  - `db_container` — Docker container name for PostgreSQL
  - `db_name` — database name inside PostgreSQL
  - `stage` — `"dev"`, `"staging"`, or `"prod"`
  Written by `echo init` and the `version` / `stage` meta-commands.
- **`./backups/`**: db dumps produced by `db-backup`. Never read at startup; only written on demand.
- **No files in the user's project repos**: Echo writes nothing to the project directory — all state lives in `~/.config/echo/`.

## Auth and Access Model

- No authentication. Echo is a local developer tool that runs with the user's shell permissions.
- Docker socket access is assumed (user is in the `docker` group or runs with sudo).
- Destructive commands (`db-drop`) require an explicit `--force` flag — enforced in the cmd layer, not the REPL.

## Invariants

1. All terminal output must go through `lipgloss` styles derived from the active theme — never raw ANSI codes or hardcoded hex in rendering code.
2. Commands that run subprocesses must stream output line by line via a `<-chan Line` channel; they must never buffer and dump at the end.
3. The REPL loop must never block the UI while a subprocess is running — streaming happens in a goroutine, the prompt stays responsive.
4. Version-specific Odoo CLI differences (flag names, init paths) must be handled in `internal/cmd/` — the REPL layer must be version-agnostic.
5. `db-drop` must refuse to run without `--force`; this check is in the cmd layer and cannot be bypassed by the REPL.
6. Theme switching must take effect immediately on the next rendered line — no restart required.
