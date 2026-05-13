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
| 03 | init-command                | `init` interactive flow via Charm `huh`: Odoo version, odoo container, db container, db name, stage — picked from live docker introspection | 02         |
| 04 | docker-commands             | `up`, `down`, `restart`, `ps`, `logs` — streaming subprocess output            | 03         |
| 05 | module-commands             | `install`, `update`, `uninstall`, `modules` (with `--config` form for addons paths) | 04         |
| 06 | fuzzy-picker                | fzf-style multi-select Bubble Tea model; replaces huh for module selection (covers the `modules` half of original Unit 10) | 05         |
| 07 | action-result               | `✓ / ✗` finalization line after every long-running command, detects silent failures via ERROR/CRITICAL count | 04, 05, 08 |
| 08 | log-level-coloring          | Color Odoo log lines (DEBUG/INFO/WARNING/ERROR/CRITICAL) using the active theme; traceback inheritance | 04, 05     |
| 09 | db-commands                 | `db-backup`, `db-restore`, `db-drop --force`, `db-list`                        | 04         |
| 10 | shell-commands              | `shell`, `bash`, `psql`                                                        | 04         |
| 11 | test-command                | `test <mod>...` with version-specific flags                                    | 05         |
| 12 | i18n-commands               | `i18n-export`, `i18n-update`                                                   | 05         |
| 13 | history-autocomplete        | ↑↓ history ring (done) + Tab autocomplete from command registry (pending)      | 01         |
| 14 | meta-commands               | `theme`, `logo`, `version`, `stage`, `help`, `clear`                           | 02         |
| 15 | banner-ascii                | All 4 ASCII logos with per-segment token coloring                              | 01         |

## Notes

- Unit 01 is the first deliverable — must produce a working binary with visible header.
- Unit 02 (config) must land before Unit 03 (init) — init writes into the config layer.
- Units 04, 05 are now done; 06/07/08 are the next polish set.
- Unit 06 (fuzzy-picker) covers the `modules` half of the originally-planned filterable-list (Unit 10). The `db-list` half lands together with Unit 09 (db-commands).
- Units 07 and 08 are cross-cutting polish over the streaming output. 08 should land before 07 so the action-result code can reuse the classifier.
- Unit 13 (history-autocomplete) complete: ↑/↓ persistence + bash-style Tab completion against Registry, with consistency tests across Registry/help/dispatch.
