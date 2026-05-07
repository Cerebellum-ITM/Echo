# Echo — Odoo Dev CLI

## Overview

`echo` is an interactive CLI tool for managing Odoo development environments.
It gives developers a single command interface — styled like Claude Code's REPL —
to run Docker lifecycle commands, manage modules, handle databases, export
translations, and run tests across Odoo versions 17, 18, and 19. The tool
displays a compact branded header on startup, then drops into an interactive
prompt that accepts commands and streams their output in real time.

## Goals

1. A developer can start, stop, inspect, and debug an Odoo stack with short
   memorable commands without needing to remember `docker compose` flags.
2. Module install/update/test cycles are reduced to a single command with
   streaming log output.
3. The tool auto-detects the Odoo version from project config and adapts
   command arguments accordingly (17/18/19 differences).
4. The header, prompt colors, and output theming match the active theme
   (charm, hacker, odoo, tokyo) and respect the dev/staging/prod stage.

## Core User Flow

1. Developer runs `odev` in a project directory.
2. The header renders: branded top bar + two-column body (welcome + tips/news).
3. The prompt appears: `{project}-{id} [{stage}/{version}.0]:~$ `
4. Developer types a command (e.g. `up`, `install sale`, `logs`, `db-backup`).
5. The command runs; output streams line by line with semantic colors.
6. Prompt returns for the next command.
7. `exit` or `Ctrl+D` quits cleanly.

## Features

### Docker
- `up` — start stack (`docker compose up -d`)
- `down` — stop stack
- `restart [svc]` — restart odoo or named service
- `ps` — show running containers
- `logs [svc]` — follow logs (`--tail 30`)

### Modules
- `install <mod>...` — install one or more modules and stop
- `update <mod>...` — update one or more modules and stop
- `uninstall <mod>...` — uninstall via `module.button_immediate_uninstall()`
- `modules` — list installed modules (filterable list)

### i18n
- `i18n-export <mod> [lang]` — export `.po` file (default `es_MX`)
- `i18n-update <mod> [lang]` — overwrite translations

### Database
- `db-backup [db]` — pg_dump + filestore → `./backups/<db>-<date>.zip`
- `db-restore <file> [db]` — decompress, neutralize crons/emails
- `db-drop <db> --force` — requires `--force` flag
- `db-list` — list available databases

### Shells
- `shell` — `odoo shell -d <db>`
- `bash` — exec into odoo container
- `psql` — `psql -U odoo <db>`

### Tests
- `test <mod>...` — run module tests with `--test-enable --stop-after-init`

### Meta / Config
- `version [17|18|19]` — query or switch active Odoo version
- `theme [charm|hacker|odoo|tokyo]` — switch color theme
- `logo [odev|planet|python|anchor]` — switch ASCII banner logo
- `help` — show command reference
- `clear` / `Ctrl+L` — clear screen
- `exit` / `Ctrl+D` — quit

## Scope

### In Scope

- Interactive REPL with streaming command output
- Branded compact header (two-column: welcome left, tips right)
- 4 color themes with semantic token system (charm, hacker, odoo, tokyo)
- Stage-aware prompt coloring (dev=green, staging=yellow, prod=red)
- Odoo version detection from `.odev.toml` or `docker-compose.yml`
- Keyboard shortcuts: ↑↓ history, Tab autocomplete, Ctrl+L clear
- Filterable list UI for `modules` command (via Charm bubbles)

### Out of Scope

- GUI / web UI
- Multi-project management (one project per shell session)
- Remote server management (SSH tunnels, cloud deploys)
- CI/CD pipeline integration
- Plugin system

## Success Criteria

1. `odev` starts and renders the header without errors in any of the 4 themes.
2. `up`, `down`, `ps`, `logs` stream Docker output line by line to the terminal.
3. `install`, `update`, `test` correctly compose the `odoo` CLI invocation for
   versions 17, 18, and 19 (including any version-specific flag differences).
4. The prompt color changes correctly based on the detected stage.
5. Tab completes any registered command name.
6. `db-backup` produces a valid zip in `./backups/`.
