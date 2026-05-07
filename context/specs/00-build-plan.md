# Build Plan

Decomposition of Echo into ordered, scoped, verifiable units.

## Ordering Rules

1. Dependencies first — theme before anything that renders
2. Foundation before features — REPL before commands
3. Core Docker commands before module/db/i18n commands
4. Filterable list UI only when commands need it

## Units

| #  | Name                        | What it builds                                                                 | Depends on |
|----|-----------------------------|--------------------------------------------------------------------------------|------------|
| 01 | scaffold-theme-header-prompt| Go module, theme system (4 palettes + Styles), header render, basic REPL + `ls`| none       |
| 02 | config-global               | `~/.config/echo/` layout, global.toml (theme/logo), per-project toml (keyed by path SHA-256), `internal/config/` package | 01         |
| 03 | init-command                | `init` interactive flow via Charm `huh`: Odoo version, odoo container, db container, db name, stage — auto-detect from docker-compose.yml as defaults | 02         |
| 04 | docker-commands             | `up`, `down`, `restart`, `ps`, `logs` — streaming subprocess output            | 03         |
| 05 | module-commands             | `install`, `update`, `uninstall`, `modules` (static list first)                | 04         |
| 06 | db-commands                 | `db-backup`, `db-restore`, `db-drop --force`, `db-list`                        | 04         |
| 07 | shell-commands              | `shell`, `bash`, `psql`                                                        | 04         |
| 08 | test-command                | `test <mod>...` with version-specific flags                                    | 05         |
| 09 | i18n-commands               | `i18n-export`, `i18n-update`                                                   | 05         |
| 10 | filterable-list             | bubbles/list integration for `modules` and `db-list`                           | 05, 06     |
| 11 | history-autocomplete        | ↑↓ history ring, Tab autocomplete from command registry                        | 01         |
| 12 | meta-commands               | `theme`, `logo`, `version`, `stage`, `help`, `clear`                           | 02         |
| 13 | banner-ascii                | All 4 ASCII logos with per-segment token coloring                              | 01         |

## Notes

- Unit 01 is the first deliverable — must produce a working binary with visible header.
- Unit 02 (config) must land before Unit 03 (init) — init writes into the config layer.
- Units 04–09 can be built in any order after 03 is done.
- Unit 10 can be layered on top of 05 and 06 without changing their API.
- Unit 11 can be parallelized with 04–09 since it only touches the REPL layer.
