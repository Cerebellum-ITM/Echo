# Changelog

All notable changes to Echo are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `test <mod...> [--tags <spec>]` command — runs the Odoo test suite
  for one or more modules via `odoo -u <mod> --test-enable
  --stop-after-init` (or `--test-tags <spec>` when `--tags` is given,
  which already implies `--test-enable`). Fourth sibling of
  `install` / `update` / `uninstall`: same picker fallback when no
  module is given, same streaming + finalize frame, same auto-copy on
  failure (logger `echo.test.module.<mod>.error`). CLI flags are
  identical across Odoo 17, 18 and 19, so no version branching is
  required. Implements Unit 11.

### Changed
- The Echo binary version shown in the header now includes a
  build-metadata suffix (`+<shortsha>.dirty`) whenever the working
  tree had uncommitted or untracked changes at build time. Clean
  builds keep the bare semver (e.g. `0.4.0`). The version constant
  in `internal/repl/repl.go` remains the single source of truth — it
  is bumped together with the `[Unreleased]` → `[X.Y.Z]` promotion
  in the same release commit — and the Makefile decorates it via
  `-ldflags` from `git status --porcelain` + `git rev-parse --short
  HEAD`. Makes it obvious that a locally moved binary is ahead of
  the last release commit.

### Fixed
- `test` now passes both `--no-http` and `--http-port=8189` so the
  test process does not clash with the live Odoo server already
  bound to 8069 inside the same container. `--no-http` alone is the
  documented fix but was observed to be silently ignored on Odoo 19
  Enterprise; the explicit `--http-port` redirect guarantees the
  bind goes to an uncommon high port even on that distribution.
  Without these flags the run aborted with `Address already in use`
  before any test could execute. HttpCase suites are unaffected —
  they spin up their own ephemeral server regardless.

## [0.4.0] — 2026-05-19

### Added
- `stop [service]` command — wraps `docker compose stop` to halt the
  Odoo stack without removing the containers, complementing the
  destructive `down`. Hooks into the prompt health cache invalidation
  alongside `up` / `down` / `restart`.

### Changed
- Every command now closes with an Odoo-style end-log line. `finalize`
  was rewritten to emit `INFO echo.<cmd>: <name> completed` on success,
  `WARNING echo.<cmd>.cancelled` when the user aborts a confirmation /
  picker, and `ERROR echo.<cmd>.error` on residual errors — replacing
  the legacy `✓ / ✗ <summary>` print. `up` / `down` / `stop` / `restart`,
  `i18n-export` / `i18n-update`, and `db-backup` / `db-restore` /
  `db-drop` now share the exact start/end frame already used by
  `install` / `update` / `uninstall` and the shell sessions.
- `down` now asks for a red `huh.Confirm` when `stage=prod` before
  tearing down the stack, mirroring the prod-confirm guard already
  applied to `bash` / `psql` / `shell` and `db-drop`. The `--force` flag
  bypasses the prompt and is stripped from the arguments forwarded to
  `docker compose down`. Behavior in `dev` / `staging` is unchanged.
- Read-only commands (`ps`, `logs`, `modules`, `db-list`) now close with
  an Odoo-style end-log line — `INFO echo.<cmd>: <name> completed` on
  success, `ERROR echo.<cmd>.error: <name> failed` on failure — matching
  the start/end pair already emitted by `shell`, `bash`, and `psql`.
  Failures of these commands do not auto-copy to the clipboard since
  they do not change state.

### Added
- Odoo-aware REPL prompt: shows compose project name, Odoo version,
  database, a colored stage chip, and live container health (Odoo +
  Postgres) using Nerd Font glyphs. Segments are configurable via the
  new `[prompt]` block in `~/.config/echo/global.toml`
  (`segments`, `name_max`, `health_ttl`). Container health reads
  through a TTL-cached `docker inspect` and refreshes immediately
  after `up` / `down` / `restart`.
- Per-project `compose_project` override in the project TOML for
  cases where the docker-compose project name does not match the
  folder name (e.g. when set via `COMPOSE_PROJECT_NAME`).

### Changed
- The REPL prompt no longer renders the per-session id. Project
  identity now comes from the docker-compose project name resolved
  from `COMPOSE_PROJECT_NAME`, the per-project `compose_project`
  field, or the normalized project directory basename. The version
  bracket no longer inherits the stage color — the stage is shown as
  an independent colored chip after the bracket.

## [0.3.1] — 2026-05-18

### Fixed
- Ctrl+C during interactive shells (`bash` / `psql` / `shell`) is now
  detected by scanning the stdin byte stream for `0x03` (ETX), since
  raw mode disables the kernel's SIGINT translation and `signal.Notify`
  never fires while the host terminal is raw. The shell session now
  correctly reports `echo.<cmd>.cancelled` (WARN) instead of falling
  through to the ERROR auto-copy path.
- The stdin-reader goroutine spawned by `ExecInteractive` no longer
  leaks after the subprocess exits. It now reads from a `syscall.Dup`
  of stdin that is closed on the way out, unblocking the otherwise
  permanent `Read` with `EBADF` and freeing keystrokes for the REPL
  prompt — fixes the visible REPL "hang" after multiple `shell`
  sessions.

## [0.3.0] — 2026-05-18

### Added
- `db-backup`, `db-restore`, `db-drop`, `db-list` — full database lifecycle
  against the configured Postgres container, with `huh.Confirm` on destructive
  operations and the fzf-style fuzzy picker over `*.dump` / `*.zip` backups.
- `bash`, `psql`, `shell` — interactive sessions inside the running
  containers. The Odoo Python shell bypasses the entrypoint via explicit
  `--db_host` / `--db_port` / `--db_user` / `--db_password` / `--no-http`.
- `i18n-export` / `i18n-update` — translation lifecycle on top of Odoo's
  CLI, with a `/tmp/echo-i18n-*.po` shuffle inside the container plus
  `docker cp` to/from the host. Default language `es_MX`; prod-confirm on
  update.
- Tab autocomplete on the command registry (bash-style: LCP on first Tab,
  match listing on second consecutive Tab).
- `copy-last` and `copy-last --errors` — copy the previous command's
  output to the clipboard, optionally filtered to `err` / `warn` lines.
- Auto-copy on failure for every subprocess-backed command
  (`install` / `update` / `uninstall`, `bash` / `psql` / `shell`,
  `i18n-export` / `i18n-update`, `db-backup` / `db-restore` / `db-drop`,
  `up` / `down` / `restart`). The clipboard payload starts with an Odoo
  log-style header.
- 8-pastel rotation for Odoo logger names (FNV-1a hash so each logger
  keeps the same colour across runs).
- Hierarchical loggers for echo's own events: `echo.<cmd>.start`,
  `echo.<cmd>` (completed), `echo.<cmd>.error`, `echo.<cmd>.cancelled`.
  For module commands the path embeds the resolved target
  (`echo.update.module.<mod>`, `.modules`, `.all`).
- OSC 52 priority for the clipboard package when running under SSH or
  tmux (`$SSH_TTY` / `$SSH_CONNECTION` / `$TMUX`).
- Warning count exposed alongside error count on the post-command status
  line and on the structured ERROR field.

### Changed
- Post-command status lines (✓ / ✗) replaced by manually-rendered Odoo
  log lines so they sit next to the container's own log stream.
  `charmbracelet/log` is no longer used inside the REPL.
- Odoo log stream now renders per-segment: timestamp dim, PID faint,
  4-char level chip (`DEBU` / `INFO` / `WARN` / `ERRO` / `CRIT`) coloured
  per level, `db` in `palette.Accent`, logger via the pastel rotation,
  message in default foreground.
- Charm palette `Warning` switched from orange (`#f6ad55`) to pastel
  yellow (`#fde047`).
- Traceback continuation kind-inheritance no longer requires line
  indentation, so the full `Traceback (most recent call last):` block
  plus the `ExceptionType: message` tail is captured by auto-copy.
- `RunInstall` / `RunUpdate` / `RunUninstall` return the resolved
  modules so the REPL labels its report with real targets even after
  the fuzzy picker runs.
- Odoo log classifier anchors on the full prefix (`^ts pid LEVEL `) —
  stray `DEBUG` / `INFO` keywords inside traceback comments no longer
  break err-kind inheritance.
- Interactive shells go through a host-side PTY (`github.com/creack/pty`)
  so the combined container output can be tee'd into the auto-copy
  buffer without breaking interactivity.

### Fixed
- `RunOdooShell` no longer crashes Odoo with `ValueError: int('')` when
  `POSTGRES_PORT` is missing from `.env`; the missing flag is now
  skipped via `odoo.Conn.Flags()`, with a defensive default of `5432`.
- `ErrCancelled` text generalised from `"init cancelled"` to
  `"cancelled by user"` — the error is reused by every picker and
  prod-confirm, the old wording was misleading outside `init`.
- Ctrl+C during an interactive shell is now reported as a WARNING
  (`echo.<cmd>.cancelled`) instead of triggering an ERROR auto-copy of
  the `KeyboardInterrupt` traceback the user just produced.

## [0.2.0] — 2026-05-12

### Added
- `init` command (v2): interactive `huh` form with live docker
  introspection (`compose ps`, `psql -lqt`) and `.env` parsing.
- `install` / `update` / `uninstall` / `modules` — Odoo module
  lifecycle via `compose exec -T`.
- `up` / `down` / `restart` / `ps` / `logs` — Docker compose lifecycle
  with streamed output and a `--copy` flag on `logs`.
- Fuzzy picker (fzf-style, Bubble Tea) for module selection.
- Odoo log-level colouring with traceback inheritance.
- Action-result lines (`✓` / `✗`) after every long-running command.
- Persistent command history at `~/.config/echo/history`.

### Changed
- Theme and stage are now loaded from `~/.config/echo/` instead of
  being hardcoded.

## [0.1.0] — 2026-05-07

### Added
- Initial scaffold with theme system (4 palettes), two-column header,
  REPL prompt, and the `ls` command.

[Unreleased]: #unreleased
[0.3.1]: #031--2026-05-18
[0.3.0]: #030--2026-05-18
[0.2.0]: #020--2026-05-12
[0.1.0]: #010--2026-05-07
