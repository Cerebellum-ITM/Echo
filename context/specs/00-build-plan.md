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
| 02 | config-detect               | `.echo.toml` read/write, Odoo version detection, stage detection               | 01         |
| 03 | docker-commands             | `up`, `down`, `restart`, `ps`, `logs` — streaming subprocess output            | 02         |
| 04 | module-commands             | `install`, `update`, `uninstall`, `modules` (static list first)                | 03         |
| 05 | db-commands                 | `db-backup`, `db-restore`, `db-drop --force`, `db-list`                        | 03         |
| 06 | shell-commands              | `shell`, `bash`, `psql`                                                        | 03         |
| 07 | test-command                | `test <mod>...` with version-specific flags                                    | 04         |
| 08 | i18n-commands               | `i18n-export`, `i18n-update`                                                   | 04         |
| 09 | filterable-list             | bubbles/list integration for `modules` and `db-list`                           | 04, 05     |
| 10 | history-autocomplete        | ↑↓ history ring, Tab autocomplete from command registry                        | 01         |
| 11 | meta-commands               | `theme`, `logo`, `version`, `help`, `clear`                                    | 02         |
| 12 | banner-ascii                | All 4 ASCII logos with per-segment token coloring                              | 01         |

## Notes

- Unit 01 is the first deliverable — must produce a working binary with visible header.
- Units 03–08 can be built in any order after 02 is done.
- Unit 09 can be layered on top of 04 and 05 without changing their API.
- Unit 10 can be parallelized with 03–08 since it only touches the REPL layer.
