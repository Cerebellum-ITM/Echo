# Changelog

All notable changes to Echo are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `shell` now also restyles the Odoo Python shell's startup block (not just
  the Odoo logs): the injected namespace globals (`env:`, `odoo:`,
  `openerp:`, `self:`) render as Echo structured fields — accent key + dim
  value — and the stock Python/IPython banner lines (`Python …`, `Type '…`,
  `IPython …`, `Tip: …`) are faded so the noise recedes and the prompt
  stands out. New `styleShellBanner` plugged into the shell `LineTransform`
  after the log-line match.

### Fixed
- `shell` log colorization now also catches Odoo's *own* colored logs. Under
  `shell` (`docker compose exec -t`) Odoo's stdout is a TTY, so its
  `ColoredFormatter` wraps the level/logger in ANSI SGR codes — which broke
  the plain log-line regex, so each line slipped through wearing Odoo's
  coloring instead of Echo's. The `shell` transform now strips ANSI
  (`stripANSISeq`) before matching, so the lines re-render in Echo's style.
  (`update`/`install` use `exec -T`, no TTY, so their logs were already
  plain and unaffected.)

### Changed
- `shell` now colorizes Odoo's startup logs to match the rest of Echo: the
  Odoo log lines the interactive Python shell prints raw through the PTY
  (`… INFO ? odoo: …`, `odoo.modules.loading: …`, `odoo.modules.registry:
  …`) are restyled per-segment with the same renderer used for streamed
  `update`/`install` output (level chip, pastel logger, accent db). The
  interactive parts (IPython banner, prompt, eval output) pass through
  verbatim, and the auto-copy capture keeps the raw ANSI-free text.
  Implemented as an opt-in `LineTransform` on `docker.ExecInteractive`
  (`bash`/`psql` keep the plain passthrough); a 30 ms partial-flush keyed on
  a leading digit means keystroke echo never lags.

### Added
- Migration detection on `install`/`update`/`uninstall`: Echo now watches the
  streamed Odoo log for `odoo.modules.migration` lines (`module <mod>: Running
  migration [<version>] <phase>-migration`) and, after the success/failure
  recap, closes the run with one `echo.<cmd>.migration` INFO line per migrated
  module — `migration detected module=<mod> version=<ver> phases=pre,post`.
  The per-phase lines (pre/post/end) collapse into a single record keyed by
  module + version, and the trailing range marker (`18.0.0.6>`) is trimmed.
  `report` mirrors this: it scans the whole last run (every step, regardless
  of the step/level filter) and appends the same `echo.report.migration`
  summary lines so a migration that happened inside `echo run` is surfaced.
- New `modinfo [<mod>]` command (Unit 42): compare the version Odoo
  recorded as installed in the database (`ir_module_module.latest_version`
  + `state`) against the version declared in the module's
  `__manifest__.py`, printing a one-line verdict as an `echo.modinfo` log
  line — `in sync`, `update pending` (code newer than the DB), `db ahead`,
  or `not installed`. The manifest version is normalized the way Odoo's
  `adapt_version` does (prepends the `17.0` series to a short version)
  before comparing, so `1.3.0` matches the DB's stored `17.0.1.3.0`. With
  no module a single-select picker chooses one; `--copy` copies the report;
  reads the manifest from the host (host mode) or the container (conf
  mode). One-shot eligible (`echo modinfo sale_goals_management`).
  `--last` re-shows the session's last `modinfo` target without the picker
  (in-memory only, per session) — so a result first reached via the picker
  can be copied with `modinfo --last --copy`.
- New `view [<mod>]` command (Unit 43): open a fuzzy picker of a module's
  files and display the chosen one through `bat`/`batcat` (syntax
  highlight + paging) when it's on `PATH`, falling back to a themed
  internal print otherwise. `--copy` copies the file's contents to the
  clipboard instead. Reads files from the host (host mode) or inside the
  Odoo container (conf mode). With no module a module picker runs first.
  `--last` re-displays the session's last viewed file without the pickers
  (in-memory only, per session) — handy to copy a file first reached
  interactively with `view --last --copy`.

## [0.7.0] — 2026-06-09

### Added
- Per-step `--silent` in `echo run` (Unit 41): append `--silent` to a
  recipe step to suppress its output on screen **and** in the `--log`
  transcript, or `--silent=<lvl>` to drop that level and below while still
  showing more severe lines (`stop --silent=info` hides DEBUG/INFO, keeps
  WARNING/ERROR). The runner's `step N/M →` line and the recap stay visible
  (the recap shows `silent=<all|lvl>`), and silenced lines are still
  captured for `report`, so `report --step=N` can pull them up. `--silent`
  is recipe-only — intercepted by the runner, never passed to the command —
  so it works on any non-interactive step.
- New `report` command (Unit 40) inspects or copies the **last run's** logs
  by step and level, across process boundaries: every `echo run` now
  persists a structured `~/.config/echo/run-logs/last-run.json` (per step:
  command, status, and its captured lines tagged with a log level), and
  `report` queries it. `report --step=<N>` selects a step (default: all);
  `--level=<lvl>` matches that level exactly, `--min-level=<lvl>` matches
  it and more severe (`ERROR` and `CRITICAL` stay distinct); `--copy` puts
  the matched lines on the clipboard (OSC 52-aware), otherwise they print
  to stdout colored by level. Works one-shot (`echo report …`) and in the
  REPL (`report …`). Example: `echo report --step=1 --level=warn --copy`.
- `echo run --pick` (Unit 39) opens a single-select picker of the `*.echo`
  recipe files in the current directory and runs the chosen one — so you
  can launch a recipe without typing its path. Top-level only (no
  recursion); composes with `--continue-on-error` and `--log`. With no
  matches it prints `no .echo recipes found in <dir>`; `--pick` plus a path
  is a usage error; a non-TTY invocation fails closed (exit 2).
- `echo run <file>` now ends with a per-step run summary (Unit 37): one
  `echo.run` line per step with its status (`ok` / `failed` / `cancelled`
  / `skipped`), warning count, and duration (`took`), plus a final
  `run summary` totals line (`steps` / `ok` / `failed` / `skipped` /
  `errors` / `warnings` / `took`; `errors` and `warnings` are always
  reported, even at zero). Under fail-fast the steps after the failure are
  reported as `skipped`. The recap is captured by `--log` like the rest of
  the run. Process exit codes are unchanged.
- Loose-severity stderr lines now reformat into Echo's Odoo log style
  (Unit 36). A line whose first token is a bare severity keyword + `:` —
  e.g. wkhtmltopdf's `Warn: Can't find .pfb for face 'Courier'` or
  `Error: Failed loading page` — is rendered with a timestamp, level chip
  and the synthetic `report.wkhtmltopdf` logger instead of leaking through
  as raw, unstyled text. A loose `Warn:` counts toward the run's warning
  total; a loose `Error:`/`Critical:` is colored but **not** counted as a
  failure, so a noisy tool's stderr can't flip a finished update to ✗.
  Lines inside an active traceback (err/warn inheritance) are left grouped,
  not hijacked. Applies to module (`update`/…) and `logs` output.
- `update --last` repeats the last `update` for the current project and
  database (Unit 35) — the resolved module list, or `--all` if that was
  last — bypassing the picker and running directly. The target is
  persisted on disk (`~/.config/echo/last-updates/<key>.toml`, one record
  per database), so it survives a REPL restart, and is recorded even when
  the update fails, so re-running after a fix just works. The previous
  `--level` is inherited unless overridden on the repeat.
- In the interactive REPL, the `update` fuzzy picker now highlights the
  previous run's modules (Unit 35), and confirming the picker with nothing
  selected offers a brief confirmation to repeat that last update —
  listing the modules — so the empty picker and `--last` are two routes to
  the same "repeat last". Explicit `update <mods>` and `update --all` run
  directly with no confirmation, and script mode (`echo run <file>`,
  `echo update …`) never prompts.
- `echo run <file> --log[=<path>]` writes the whole recipe run to a
  plain-text transcript (Unit 34) — every step's streamed output plus the
  `echo.run` step/summary lines, ANSI-stripped — so an update routine
  leaves an auditable record. Opt-in: bare `--log` writes a timestamped
  file under `~/.config/echo/run-logs/`; `--log=<path>` writes to an
  explicit path; and `--log=<dir>` (e.g. `--log=.` for the current
  directory) writes a `<recipe>.log` named after the recipe into that
  directory. Without the flag, nothing is written. A log-file error warns
  but never aborts the run, and the final line reports the path.
- `--level <lvl>` flag on `update` / `install` / `uninstall` (Unit 33),
  mapping to Odoo's native `--log-level` so a developer can raise or lower
  the verbosity of a module operation (e.g. `update sale --level debug_sql`
  to see the SQL, `--level warn` to quiet it). Both `--level <lvl>` and
  `--level=<lvl>` forms work. The value is validated against Odoo's level
  set (`debug_rpc_answer` … `critical`, `test`, `notset`) — an invalid
  level is rejected with the list of valid ones before Odoo is invoked.
  Without the flag, behavior is unchanged (Odoo's `info` default).
- `echo run <file>` **recipe runner** (Unit 32). Runs a whole update
  routine from a single file — one Echo command per line — instead of N
  separate invocations. Blank lines and `#` comments are ignored; the
  recipe can also be read from stdin (`echo run -` or piped input).
  Comments are stripped both as full lines (`# …`) and inline after a
  command (`update sale  # fix`), so an annotated table pastes in as-is.
  Each
  step streams through the same one-shot path script mode added, and the
  run **stops at the first step that exits non-zero** (fail-fast),
  exiting with that step's code; `--continue-on-error` runs every step
  and exits non-zero if any failed. Progress and the final summary are
  emitted as `echo.run` log lines in Echo's Odoo style. Because steps go
  through the one-shot dispatch, any step that would prompt fails closed
  without a TTY — a recipe must be explicit (module names, `--force`).
- Non-interactive **script mode** (Unit 31). `echo <command> [args]` now
  runs a single command and exits, so Echo can be driven from shell
  scripts and CI (e.g. an update routine that chains `echo stop`,
  `echo up`, `echo update ventas`, `echo restart`). Bare `echo` still
  opens the interactive REPL. One-shot output streams through the exact
  same Odoo-style render and start/finalize frame the REPL uses. The
  process exits with a meaningful code: `0` success, `1` execution error
  (or ERROR/CRITICAL lines counted), `2` usage error (unknown command,
  bad args, or a command that would need a prompt), `3` cancelled. Any
  command that would otherwise block on a confirmation or a fuzzy picker
  **fails closed** when stdin is not a TTY — it returns a clear error and
  a non-zero exit instead of hanging a script, naming the escape hatch
  (pass the missing argument, or `--force`). A human running the same
  command at a real terminal still gets the prompt. New `-C` /
  `--project-dir <dir>` flag runs a one-shot command from outside the
  project directory (like `git -C`).

### Changed
- The `echo run` per-step recap is now fully structured and color-cued:
  `step` and `status` are key=value fields (`step=1/4 status=ok`), the
  status value is colored by outcome (ok green, failed red,
  cancelled/skipped amber), and the `cmd` value is tinted by its action
  (`up`/`stop`/`update`… each a stable color). `report --copy` collapses to
  a single Odoo-style line (`echo.report: copied N lines to clipboard …`)
  instead of a log line plus a separate plain confirmation. Structured log
  lines with an empty message no longer render a stray double space.
- The `update` / `install` / `uninstall` / `test` **start line** now names
  the resolved module(s) — including picker selections and `update --last`,
  which previously logged a generic `echo.update.start`. The line is
  emitted once the module set is known (after the picker / `--last` disk
  read), with the modules in both the logger (`echo.update.module.<mod>` /
  `.modules` / `.all`) and a `modules=` field, so you can tell what's
  running from the start, not only from the end-of-run line.

## [0.6.0] — 2026-06-09

### Added
- `db-neutralize [name]` command and a `--neutralize` flag on `db-restore`
  (Unit 30). Both run Odoo's native `odoo neutralize` CLI inside the Odoo
  container, applying each installed module's `data/neutralize.sql` to
  deactivate production-only parameters (outgoing mail / fetchmail servers,
  cron jobs, payment providers, the environment ribbon, …). `db-neutralize`
  targets the configured DB by default, a positional name, or a picker when
  neither is set, and shows a red confirmation when the target is the active
  DB or `stage=prod` (skippable with `--force`). `db-restore --neutralize`
  neutralizes the freshly restored copy in one step — the prod→test flow.
- `connect` no longer spawns a fresh Chrome window (and a throwaway temp
  profile) on every run (Unit 29). It now reuses a persistent,
  Echo-dedicated Chrome instance (`~/.local/share/echo/connect-chrome`,
  override `$ECHO_CONNECT_CHROME_PROFILE`) and opens the session in a new
  **tab** by default — driving Chrome at the browser level over CDP so it
  never hijacks a tab you already had open. New `--new-window` flag opens
  the session in an isolated **incognito** window instead (its own cookie
  jar), so multiple users can be impersonated at the same time. The
  projectless `echo connect <name>` also honors `--new-window` and
  `--fresh`. The `opening chrome` log line shows `window=tab|incognito`.
- `connect` now caches the minted session locally and reuses it instead of
  re-querying users and re-minting on every run (Unit 28). On a repeated
  `connect <login>`, Echo loads the cached cookie, validates it with a
  single HTTP probe against `<base>/odoo` (a logged-out session redirects to
  the login page), and — if still valid — lands it straight into Chrome,
  skipping both the `res.users` query and the session mint. A stale or
  invalid cookie (past the 5-day TTL or rejected by the probe) is
  transparently re-minted. The interactive `connect` (no login) now offers
  the recently used logins first, with a "↻ Fetch all users…" row to fall
  back to the full list. New `--fresh` flag forces a re-mint, ignoring the
  cache. Sessions are stored per target+db at
  `~/.config/echo/connect-sessions/<key>.toml`.
- `connect` now narrates each step in Echo's Odoo-style log format
  (Unit 28), instead of running silently and printing a couple of plain
  lines at the end. Target resolution, the user query (with count), cache
  hit / validation / reuse / re-mint, the mint, and opening Chrome each
  emit a structured `echo.connect[.cache|.mint]` line — matching the rest
  of the CLI's log stream — closed by the usual `connect completed`.
- Module discovery now falls back to the instance's `odoo.conf` (Unit 26).
  When the host-side addons scan finds no modules — e.g. an instance whose
  addons live only inside the container, declared via `addons_path` in
  `/etc/odoo/odoo.conf` — `modules` / `install` / `update` / `uninstall` /
  `test` no longer fail with `no modules found`. Echo reads the conf inside
  the Odoo container (`conf_path`, default `/etc/odoo/odoo.conf`), parses
  `addons_path`, lists the modules in those container directories, and
  persists `addons_mode = conf` plus the discovered paths to the project
  config. In conf mode the conf is re-read live on each run, so edits to
  `addons_path` are picked up automatically. `modules --config` (the host
  folder picker) is unchanged and always pins `addons_mode = host`, so it
  remains the explicit escape hatch back to host scanning.
- The fuzzy picker now scrolls: long lists (e.g. a full module catalog)
  render in a viewport sized to the terminal height instead of spilling
  past the screen and hiding rows. The window follows the cursor, `pgup` /
  `pgdn` page through it, and `↑ N more` / `↓ N more` hints show how much
  is off-screen. Applies to every picker (modules, db-restore, connect,
  i18n).
- Flag highlighting and flag autocomplete in the REPL (Unit 24), building
  on the command highlighting. Flag tokens are now colored too: a known
  flag of the typed command shows in the accent color (bold), an unknown
  or forwarded flag shows faint — never red, so passthrough commands like
  `down`/`logs`/`connect` don't get falsely flagged. Tab now also completes
  flags: when the token under the cursor starts with `-` and a command
  precedes it, Tab fills the command's flags (single match completes,
  several share a common prefix then list on a second Tab), exactly like
  command completion. Backed by a new per-command flag registry
  (`commandFlags`) kept consistent with `Registry` by an init guard.

### Fixed
- The filestore is now read from and written to the **Odoo container**,
  not the host (Unit 25). Echo previously used the native install path
  `~/.local/share/Odoo/filestore/<db>`, so a restored filestore landed on
  the host where the containerized Odoo couldn't see it — every attachment
  then raised `FileNotFoundError`. `db-restore` now `docker cp`s the
  filestore into `<filestore_path>/<target>/` inside the Odoo container
  (best-effort `chown` so Odoo can also write), and `db-backup
  --with-filestore` pulls the filestore back out of the container. New
  per-project `filestore_path` config (default `/var/lib/odoo/filestore`).

### Changed
- `--force` on `db-drop` (and on `db-restore --force`'s replace step) now
  terminates the target DB's active connections (`pg_terminate_backend`)
  before dropping, instead of aborting (Unit 23). This makes an orphaned
  or busy database — e.g. one left behind by a failed restore — removable
  without manually running `down odoo` first. Without `--force`, `db-drop`
  still guards against active connections (now pointing at `--force` in
  the error) so a live DB isn't dropped by accident.
- `addons_path` entries whose base name starts with `enterprise` (e.g.
  `enterprise`, `enterprise-addons`) are now skipped by default when
  discovering modules from `odoo.conf`, keeping the Enterprise addons out
  of the update/install picker.
- Live command highlighting in the REPL (Unit 21). As you type, the first
  token (the command) is colored fish-style: green/bold when it's an exact
  command, red when it can no longer become one, and the default color
  while it's still a valid prefix (e.g. `ins` toward `install`). Only the
  command word is recolored — arguments keep the default style. Validity
  is driven by the existing command `Registry` (plus `exit`/`quit`), so it
  stays in sync automatically; `lineModel.View()` now renders the line
  itself while the embedded `textinput` keeps owning the (still-blinking)
  cursor. Colors come from `palette.Success` / `palette.Error`, so all four
  themes are covered.
- `db-restore` now also accepts a **standard Odoo backup `.zip`** (the kind
  downloaded from Odoo's database manager / odoo.sh), not just Echo's own
  archives (Unit 22). The restore auto-detects the archive flavor: a
  `dump.sql` (plain SQL) is loaded with `psql` while a `dump.backup`
  (pg_dump custom) keeps using `pg_restore`, and the filestore is copied
  correctly whether it's sharded directly under `filestore/<XX>/…` (Odoo)
  or nested under `filestore/<db>/…` (Echo). The Odoo download timestamp
  `_YYYY-MM-DD_HH-MM-SS` is now stripped when deriving the target db name,
  so `habitta_prod_2026-06-08_23-42-53.zip` restores into `habitta_prod`
  instead of the full timestamped name.

## [0.5.0] — 2026-06-08

### Added
- Docker container log alignment (Unit 20). The per-resource progress
  lines `docker compose` prints during `up` / `down` / `restart` /
  `stop` (e.g. `Container dvz_ny_odoo_19-db-1  Restarting`) are now
  reformatted into Echo's Odoo-style log line — `… INFO <db>
  docker.container: started name=dvz_ny_odoo_19-db-1` — instead of
  passing through raw and standing out as the only unaligned output.
  The logger is `docker.<resource>` (`container` / `network` /
  `volume` / `image`), the compose state becomes the message verb, and
  the resource name rides along as a `name=` field. Transitional states
  (`restarting`, `creating`, …) render faint (DEBUG) so the eye lands
  on the terminal state; compose `Error` / `Warning` states map to
  ERROR / WARNING and feed the run-stats counters so a failed container
  surfaces in the finalize summary. Closes the compose-output gap that
  Unit 08 explicitly deferred. Implements Unit 20.
- Loguru log format support (Unit 19). Lines emitted by `loguru`
  (`YYYY-MM-DD HH:MM:SS.mmm | LEVEL | module:func:line - msg`) are now
  classified, colored, and rendered with the same per-segment styling as
  standard Odoo `logging` lines. `| WARNING |` and `| ERROR |` lines
  increment the run stats counters and trigger auto-copy on failure
  exactly like their `logging` counterparts — closes the gap where a
  loguru ERROR during a test run was invisible to the failure detector.
  Traceback lines following a loguru error inherit the `err` kind for
  copy-on-failure grouping. Implements Unit 19.
- `test <mod...> [--update] [--tags <spec>]` command — runs the Odoo
  test suite for one or more modules. Default mode targets the
  already-installed modules and filters execution to just their tests
  via auto-built `--test-tags /<mod1>,/<mod2>` (no `-u`, fastest loop
  for iterating on Python test code since `--stop-after-init` spawns
  a fresh process that imports the latest disk state). `--update`
  opts into the `-u <mods>` reload for when views / model schema
  changed. `--tags <spec>` overrides the auto filter with a
  user-supplied spec (e.g. `:TestClass.test_method`). Always emits
  `--no-http` and `--http-port=8189` so the test process does not
  clash with the live Odoo bound to 8069 inside the same container
  (the explicit port is a safety net for Odoo 19 Enterprise where
  `--no-http` alone was observed to be ignored). Always emits
  `--log-level=test` (legacy but accepted in 17 / 18 / 19) for
  focused output. Fourth sibling of `install` / `update` / `uninstall`:
  same picker fallback when no module is given, same streaming +
  finalize frame, same auto-copy on failure (logger
  `echo.test.module.<mod>.error`). CLI flag set is identical across
  Odoo 17, 18 and 19. Implements Unit 11.
- `connect [<login>] [--all] [--force]` command — opens Chrome already
  logged in as any user of the configured DB, without their password,
  without opening any port, and without installing anything into Odoo.
  Mints a valid web session by running two embedded Python scripts inside
  the Odoo container (list users + mint via `root.session_store.new()` and
  `_compute_session_token`), then lands the `session_id` cookie into a
  throwaway-profile Chrome through the DevTools Protocol (`Network.setCookie`
  + `Page.navigate` to `<web.base.url>/odoo`) — CDP can set the HttpOnly
  cookie that JavaScript cannot. Minting runs locally via
  `docker compose exec` or, when `[connect].ssh_host` is configured, over
  SSH against the remote host, so the same command works for local and
  public-domain deployments. In remote mode the container/db mapping is
  **read from the server's own Echo profile** over SSH (located by hashing
  `remote_path` with the same key function Echo uses locally) — nothing is
  re-declared on the laptop; only `ssh_host` + `remote_path` are needed.
  When `web.base.url` is `http://` but the same host also serves HTTPS,
  connect probes and upgrades to `https://` (secure cookie + navigation),
  falling back to the original scheme for hosts without HTTPS (e.g. a
  local `http://localhost:8069`). Reuses `runSingleFuzzyPicker` and the
  standard `startLog` / `finalize` / `connectFailureLog` frame. New
  per-project `[connect]` config section (`ssh_host`, `remote_path`,
  `chrome_path`). Implements Unit 18.
- `echo connect [<name>] [<login>] [--add] [--all] [--force]` — projectless
  direct mode that runs from anywhere (no local `docker-compose.yml`),
  using **named remote targets** stored in global config. Registering a
  target picks an SSH host from the user's `~/.ssh/config` (Echo only
  references the alias, never edits the file), then lists that server's
  own Echo projects over SSH and lets you choose one and name it; next
  time `echo connect <name>` (or a picker of registered targets) connects
  straight away. Project profiles now persist `project_path`, and existing
  profiles self-migrate on next launch (`BackfillProjectPath`) so they
  become discoverable as targets — no manual re-init needed.

### Changed
- The Echo binary version shown in the header now carries a build
  metadata suffix: always the build's commit (`+<shortsha>`), plus a
  `.dirty` marker when the working tree had uncommitted or untracked
  changes at build time (e.g. `0.5.0+abc1234` or `0.5.0+abc1234.dirty`).
  Showing the commit even on a clean build pins exactly which revision
  a moved binary came from. The version constant in
  `internal/repl/repl.go` remains the single source of truth, bumped
  together with the `[Unreleased]` → `[X.Y.Z]` promotion in the same
  release commit; the Makefile decorates it via `-ldflags` from
  `git rev-parse --short HEAD` + `git status --porcelain`.
- `make build` now installs the binary straight to `~/.local/bin/echo_cli`
  (commonly on `PATH`) instead of leaving it under `./bin`. `make
  build_release` still emits the multi-platform binaries under `./bin`.

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
[0.6.0]: #060--2026-06-09
[0.3.1]: #031--2026-05-18
[0.3.0]: #030--2026-05-18
[0.2.0]: #020--2026-05-12
[0.1.0]: #010--2026-05-07
