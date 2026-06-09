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
| 16 | copy-output                 | `copy-last` + auto-copy on module failure (charm/log line); OSC 52 priority when remote | 04, 05     |
| 17 | cli-prompt-odoo-info        | Odoo-aware REPL prompt: compose project name + version/db bracket + colored stage chip + live container health (configurable via `[prompt]` in global.toml, TTL-cached) | 02, 04     |
| 18 | connect-command             | `connect [<login>] [--all] [--force]` + projectless `echo connect <name>` — mint Odoo web session for any user without password (Python in container, local or SSH) and land the cookie in local Chrome via CDP | 04, 06, 10 |
| 20 | docker-container-log-style   | Reformat `docker compose` lifecycle progress lines (`Container … Restarting`) into Odoo-style `docker.<resource>` log lines with `name=` field; closes the compose-output gap deferred in Unit 08 | 04, 08     |
| 21 | command-highlight           | Live fish-style highlight of the first REPL token: green when it's an exact command, red when it can't become one, neutral while it's still a valid prefix (reuses Registry/matchPrefix; custom `lineModel.View()`) | 01, 13     |
| 22 | odoo-native-restore         | `db-restore` also accepts a standard Odoo backup `.zip` (`dump.sql` + `filestore/<XX>/…` + `manifest.json`): auto-detects `dump.sql` vs `dump.backup` (psql vs pg_restore), handles both filestore layouts, and strips the Odoo `_YYYY-MM-DD_HH-MM-SS` timestamp | 09         |
| 23 | db-drop-force-connections   | `--force` on `db-drop` (and `db-restore --force`'s replace) terminates active connections (`pg_terminate_backend`) before dropping, so an orphaned/busy DB can be removed without a manual `down odoo` | 09         |
| 24 | flag-highlight-complete     | Extend the live REPL editing to flags: known flags of the current command render in an accent color (unknown ones faint, never red), and Tab autocompletes flags via a new per-command flag registry | 13, 21     |
| 25 | filestore-in-container      | Read/write the filestore inside the Odoo container (`/var/lib/odoo/filestore/<db>`, configurable) via `docker cp` for both `db-restore` and `db-backup --with-filestore`, fixing the host-path mismatch that left restored attachments invisible to Odoo | 09, 22     |
| 28 | connect-session-cache       | Cache the minted Odoo session locally (`~/.config/echo/connect-sessions/<key>.toml`) and reuse the cookie on a repeat `connect <login>` — validated by one HTTP probe, re-minted only when stale/invalid — skipping the user query and the mint; interactive `connect` offers recent logins first; `--fresh` forces a re-mint | 18         |
| 29 | connect-chrome-window-modes | Reuse a persistent Echo-dedicated Chrome instead of spawning a new window + temp profile every connect: browser-level CDP opens a new tab by default; `--new-window` opens an isolated incognito window (own cookie jar → multiple users at once) | 18         |

## Notes

- Unit 01 is the first deliverable — must produce a working binary with visible header.
- Unit 02 (config) must land before Unit 03 (init) — init writes into the config layer.
- Units 04, 05 are now done; 06/07/08 are the next polish set.
- Unit 06 (fuzzy-picker) covers the `modules` half of the originally-planned filterable-list (Unit 10). The `db-list` half lands together with Unit 09 (db-commands).
- Units 07 and 08 are cross-cutting polish over the streaming output. 08 should land before 07 so the action-result code can reuse the classifier.
- Unit 12 (i18n-commands) complete: `i18n-export` / `i18n-update` wrap Odoo CLI via `/tmp` + `docker cp` shuffle; default lang `es_MX`, prod confirm on update.
- Unit 13 (history-autocomplete) complete: ↑/↓ persistence + bash-style Tab completion against Registry, with consistency tests across Registry/help/dispatch.
