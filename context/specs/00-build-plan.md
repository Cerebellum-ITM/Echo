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
| 26 | addons-paths-from-conf      | Discover modules from the instance's `odoo.conf` (`addons_path`, read inside the container) when the host scan finds none: parse the conf, `ls` each container path, persist `addons_mode=conf` + paths, auto-refresh live. Falls back automatically; `modules --config` still pins host mode | 05         |
| 31 | script-mode                 | Non-interactive one-shot dispatch: `echo <cmd> [args]` runs a single command and exits (bare `echo` still opens the REPL), reusing the REPL render/finalize frame headlessly. TTY-based guard fails closed (exit≠0) when a picker/confirm would block without a terminal; meaningful exit codes; `-C/--project-dir` to run from anywhere | 04, 05, 09 |
| 32 | recipe-runner               | `echo run <file>` (or stdin): execute a sequence of Echo commands as an update script, one per line, fail-fast on the first non-zero exit. Builds on the script-mode dispatch + exit codes | 31         |
| 33 | module-log-level            | `--level <lvl>` on `update`/`install`/`uninstall` maps to Odoo's native `--log-level`; validated against Odoo's level set, appended via `odoo.WithLogLevel` | 05         |
| 34 | recipe-run-log              | `echo run … --log[=<path>]` captures the full run transcript (plain) + summary to a `.log`; opt-in, default under `~/.config/echo/run-logs/`. Tees `print`/`emitOdooLog` to a sink | 32         |
| 41 | recipe-step-silent          | Per-step `--silent` / `--silent=<lvl>` in `echo run` suppresses a step's output (screen + `--log`) keyed by level rank; runner-intercepted (any command), recap shows `silent=`, lines still captured for `report` | 32, 34, 40 |
| 40 | run-report                  | `report [--step=N] [--level=lvl|--min-level=lvl] [--copy]` queries the last run's logs by step+level; every `echo run` persists `last-run.json` (per-step lines tagged with level). One-shot + REPL; reuses lastOutputBuffer + clipboard | 32, 34, 37 |
| 39 | recipe-pick-file            | `echo run --pick` opens a single-select picker of `*.echo` recipes in the current dir (top-level) and runs the chosen one; new `cmd.PickOne` export + `echoRecipesIn` scan; mutually exclusive with a path, fails closed without a TTY | 32 |
| 38 | module-start-line-resolved  | `update`/`install`/`uninstall`/`test` start line names the resolved modules (picker / `--last` / `--all`), not just the end line; new `ModulesOpts.OnResolve` hook fires before the Odoo run, REPL emits `startResolved` with a `modules=` field | 05, 35 |
| 37 | recipe-run-summary          | `echo run <file>` emits a per-step recap (status/warnings/`took`) + a `run summary` totals line as `echo.run` log lines; per-command warning/error counts surfaced on the session (`lastErrors`/`lastWarnings`), fail-fast marks unrun steps `skipped`; exit codes unchanged | 32 |
| 36 | loose-severity-log-fallback | Reformat loose-severity stderr (`Warn:`/`Error:` from wkhtmltopdf et al.) into Echo's Odoo log style via a synthetic `report.wkhtmltopdf` logger, instead of leaking as raw text; loose `Warn:` counts as a warning, loose `Error:` doesn't fail the run; traceback lines stay grouped. Shared `emitStreamLine` for module + docker output | 08, 20 |
| 35 | update-last-recall          | `update --last` repeats the last update per (project, db) — persisted at `~/.config/echo/last-updates/<key>.toml`, repeats `--all` too. Without `--last`, an interactive confirmation lists the modules to update with previous-run modules highlighted (picker + confirm). REPL-only; never fires under `echo run`/script mode | 05, 06, 33 |
| 42 | modinfo-version-check       | `modinfo [<mod>]` compares the DB-installed version (`ir_module_module.latest_version` + `state`) against the manifest version (normalized via Odoo's `adapt_version`), printing a verdict (`in sync` / `update pending` / `not installed` / `db ahead`) as `echo.modinfo` log lines; single-select picker when no module given; `--copy`; one-shot eligible | 05, 06, 09 |
| 43 | view-module-file            | `view [<mod>]` opens a fuzzy picker of a module's files and displays the chosen one through `bat`/`batcat` (syntax highlight) with a plain-print fallback; `--copy` to clipboard; reads host files or container files per addons mode | 05, 06, 16 |
| 44 | migration-detection         | Detect Odoo migration runs in the streamed log (`odoo.modules.migration: module <mod>: Running migration [<ver>] <phase>-migration`) during `install`/`update`/`uninstall` and close the command with one `echo.<cmd>.migration` line per migrated module (module + version + phases); `report` mirrors the summary by scanning the whole last run | 05, 08, 40 |
| 45 | shell-log-colorize          | Colorize Odoo's raw startup logs in the interactive `shell` to match Echo's Odoo-styled output: an opt-in `LineTransform` on `docker.ExecInteractive` restyles each recognized log line (via `renderLogLine`) on the way to the terminal while the capture buffer keeps raw text; interactive content passes through verbatim, partials flushed on a leading-digit + 30 ms heuristic so keystroke echo never lags | 08, 10, 36 |
| 46 | shell-banner-style          | Extend the `shell` `LineTransform` to the Odoo Python shell's startup block: the injected namespace globals (`env`/`odoo`/`openerp`/`self`) render as Echo key=value (accent key, dim value) and the Python/IPython banner lines are faded; non-matching lines still pass through verbatim | 45 |
| 48 | project-aliases             | `-C` accepts a short alias instead of a directory: a user-level `name → local-path` registry in `global.toml` (`[project_aliases]`). New `alias` command (`alias <name>`/`--list`/`--rm`/`--migrate`), optional alias step in `init`, and `-C` resolution that falls back to a connect target's `remote_path` when it's local. A real directory always wins; nothing about `-C <dir>` changes | 02, 03, 18 |
| 49 | update-i18n-overwrite       | `update --i18n` adds Odoo's `--i18n-overwrite` to the `-u` run so the modules' shipped `.po` translations overwrite the DB terms (all active languages; `-l` only scopes export/import so per-language stays in `i18n-update`). Boolean flag, not persisted in `--last`; composes with `--all`/`--last`/`--level`; `odoo.WithI18nOverwrite` builder | 05, 12 |
| 50 | i18n-pull-remote            | `i18n-pull [<mod>] [lang] [--from <target>] [--all]` exports a module's translations from a REMOTE Odoo instance over SSH (project `[connect]` or a named connect target) and writes the `.po` into the local repo at `<addons>/<mod>/i18n/<lang>.po`. Reuses connect's remote-exec (`resolveConnectTarget`/`runSSH`) + i18n-export's dest logic; remote DB read-only; `--all` sweeps the repo's modules | 12, 18 |
| 51 | build-mode                  | Universal `--build`/`-b` flag intercepted in `dispatchParsed`: walks the command's positional picker(s) (modules/db/backup/service via a `buildPositionals` registry), multi-selects its known flags (`commandFlags`), prompts a value per valued flag (picker if options known — LogLevels, connect targets — else input), then shows the composed line and offers Run / Copy / Cancel. TTY-guarded (recipes fail closed); `--build` highlights/Tab-completes on every command via a `universalFlags` set | 06, 13, 24 |
| 52 | run-recent-ordering         | `echo run --pick` lists `.echo` recipes sorted by creation time, newest first (Darwin birthtime via a build-tagged `fileCreated`, ModTime fallback elsewhere; ties alphabetical). New `echo run --last` runs the most recently created recipe directly — no picker, no TTY needed; mutually exclusive with `--pick`/path/stdin, composes with `--continue-on-error`/`--log`; logs `latest recipe → <name>` | 32, 39 |

## Notes

- Unit 01 is the first deliverable — must produce a working binary with visible header.
- Unit 02 (config) must land before Unit 03 (init) — init writes into the config layer.
- Units 04, 05 are now done; 06/07/08 are the next polish set.
- Unit 06 (fuzzy-picker) covers the `modules` half of the originally-planned filterable-list (Unit 10). The `db-list` half lands together with Unit 09 (db-commands).
- Units 07 and 08 are cross-cutting polish over the streaming output. 08 should land before 07 so the action-result code can reuse the classifier.
- Unit 12 (i18n-commands) complete: `i18n-export` / `i18n-update` wrap Odoo CLI via `/tmp` + `docker cp` shuffle; default lang `es_MX`, prod confirm on update.
- Unit 13 (history-autocomplete) complete: ↑/↓ persistence + bash-style Tab completion against Registry, with consistency tests across Registry/help/dispatch.
