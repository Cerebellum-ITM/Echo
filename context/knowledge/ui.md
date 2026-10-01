# UI and Output

> Owns how Echo looks and what its output contract is: theme, palettes, stage colors, Odoo-style log rendering and logger vocabulary, ANSI and gutter traps, pickers, prompt, banner, tables, the `--json` stdout rule, command highlight and completion, icons, copy and clipboard. Last verified: 2026-09-30.

Stack: Go 1.25, lipgloss v1.1.0, bubbletea v1.3.6, bubbles `textinput`, huh v1.0.0 (`go.mod`). Dark themes only. There are no `theme`, `logo`, `version` or `stage` meta-commands (Unit 14 closed unimplemented, decision 2026-09-30); the theme is set by editing `global.toml`.

## Guiding principle: Odoo cohesion

- Echo's own output imitates Odoo's log format so its lines sit next to the container's stream without standing out: same timestamp/pid/level/db/logger shape, dotted lowercase loggers, `key=value` fields. Avoid generic CLI decorations (checkmarks, boxes, spinners). Source: Unit 07, Unit 16, `internal/repl/logemit.go:emitOdooLog`.
- `charmbracelet/log` is used only in `main.go` for boot fatals; everything else is rendered by hand because it cannot reproduce Odoo's shape (Unit 16).
- Recaps and summaries are log lines, never ASCII tables, so `--log` and `copy-last` capture them for free (Unit 37). Listings the user reads directly (`ps`, `db-list`, `modstate`) are the exception and use `Line{Kind:"table"}`, which bypasses the log-line parser (Units 47, 56).

## Theme, palettes, stage colors

- Theme = `theme` key of `~/.config/echo/global.toml`; `theme.PaletteByName` returns `charm` (default), `hacker`, `odoo`, `tokyo` (`internal/theme/theme.go`). Unknown name falls back to `charm`. Charm Warning is `#fde047` (commit 697c84d).
- Palette slots: Bg, Fg, Dim, Faint, Accent, Accent2, Success, Warning, Error, Info. `Bg` is only used by the huh form theme (`internal/cmd/theme.go`); Echo never paints the terminal background. `Accent2` is used by huh selectors and some commit-tag colors.
- `theme.Styles` (built by `theme.New`) carries the line styles; `Line.Kind` values are `out dim faint info ok warn err accent label` (`internal/repl/repl.go:Line`). `sess.print` first tries `renderLogLine` on every line regardless of Kind, then falls back to the Kind style.
- **Stage colors**: `Palette.PromptColor(stage)` maps dev to Success (green), staging to Warning (yellow), prod to Error (red); unknown stage is dev. The same color drives the banner, the prompt stage chip, the picker bar and filter prompt, the logview and help-pager bar, and the sequence review bar (Units 15, 17, 55, 82). Unit 15 rationale: each environment looks distinct and consistent everywhere instead of following the theme accent.
- Stage is only known for a resolved context (`cfg.Stage`, `prof.Stage`, target stage). Pickers that choose a *target* (connect, i18n-pull target picker) keep the default accent because the target's stage lives in the remote profile and reading it costs an SSH round-trip (Unit 55). The sequence review uses Warning (amber) for any remote run for the same reason (`internal/repl/sequence.go:renderSequenceReview`).
- `theme.Lighten`/`Darken` derive shades by channel blend toward white/black; non-`#rrggbb` input is returned unchanged. The banner gradient uses them so it works in all four themes.
- **Styling rule is a goal, not an invariant**: new code should take colors from `theme.Palette` and not hardcode hex. Reality: 31 non-test files outside `internal/theme` call `lipgloss.NewStyle()`, and the 8-color pastel rotation is hardcoded twice (`internal/repl/logrender.go:loggerPalette`, `internal/cmd/tagcolor.go:tagFallbackPalette`). Do not "fix" this opportunistically, but do not add new hex.

## Log line anatomy

Odoo line: `YYYY-MM-DD HH:MM:SS,mmm PID LEVEL db logger: message key=val ...` (comma before milliseconds).

| Segment | Style |
|---|---|
| timestamp | Dim |
| pid | Faint |
| level chip `DEBU INFO WARN ERRO CRIT` | bold, Faint / Info / Warning / Error / Error (`shortLevel`) |
| db | Accent, middle-truncated to `log_db_max` |
| logger | pastel rotation, FNV-1a of the name mod 8, stable across runs |
| message | Fg |
| field key | by name: `module(s)` Accent bold, `err(s)` Error bold, `warnings` Warning bold, `copied` Info bold, else Dim |
| field value | `status` ok green / failed red / cancelled+skipped amber; `cmd` tinted by its first token (same rotation); `age` red bold from one hour on (a `humanAge` value with `h` or `d`, e.g. `deploy --apply`'s `plan matches`); else Fg |

- Values with whitespace or quotes are Go-quoted (`quoteIfNeeded`); empty is `""`. A db of `""` renders as `-`.
- **Two renderers exist**: `repl.renderOdooLog` (full styling) and `cmd.renderOdooLogLine` (`internal/cmd/connect_log.go`, used by the projectless `connect <name>` path because `internal/cmd` cannot import `repl`). The cmd copy is simpler: logger in Info instead of the pastel rotation, no per-key/value styling, unstyled message. A style change to log lines must touch both. Source: `connect_log.go:directConnectLogger`.
- **DB name truncation is display-only** (Unit 71): `theme.MiddleTruncate(db, log_db_max)`, default 20, global config read once at session start into `logDBMax`. It applies to streamed Odoo lines, Echo's own lines and the cmd renderer. Plain twins (`plainOdooLog`, `plainOdooLogFields`, run-log tee, clipboard header, `copy-last`) keep the full name so they stay greppable. Any new styled render path must call `MiddleTruncate` on the db column or long names wrap the line.
- `plainOdooLog` is the ANSI-free twin used as the first clipboard line on auto-copy.

## Logger vocabulary

- Start: `echo.<cmd>.start` INFO, message = command, positional args in `args=`. Module commands (install/update/uninstall/test) emit it from `OnResolve` once the final module set is known: `echo.<cmd>.module.<mod>.start`, `.modules.start`, `.all.start`, always with `modules=` (and `flags=` when flags were passed). If the command fails before resolving (no DB, `ErrNoLastUpdate`, picker cancelled) no start line is emitted; the failure line frames it. Rationale: the picker's alt-screen wipes a pre-picker line anyway (Unit 38, `copylast.go:startResolved`).
- End (`finalize`): success INFO `echo.<cmd>` message `<cmd> completed` (adds `warnings=N` when above zero); failure ERROR `echo.<cmd>.error` (`err=` or `errors=N`); user cancel WARNING `echo.<cmd>.cancelled`. `failureLogger` appends `.error` so the logger path encodes severity. Logger naming for module commands: one module `echo.<cmd>.module.<mod>`, `--all` `echo.<cmd>.all`, several `echo.<cmd>.modules` (`echoCommandLogger`).
- A cancelled picker or confirm is a WARNING, never a failure. Outcome rule: failure = non-zero exit OR any ERROR/CRITICAL line counted during the stream, because Odoo logs ERROR and can still exit 0 (Unit 07, `scriptExitCode`). Exit codes themselves: [architecture-and-traps.md](architecture-and-traps.md).
- System status: one `echo.system.status` line (`cli`, `odoo`, `env`, `project`, `db`) at the start of `connect`, `run`, `i18n-pull`, `link --show` and remote shell-run, not per sub-command. `odoo=unknown` flags a target without `odoo_version`. It never carries the DB password. `cmd.EchoVersion` is a package var set from `main.go` because `cmd` cannot import `repl` where `FullVersion()` lives (Unit 54).
- Docker compose progress: `Container|Network|Volume|Image <name> <State>` lines are re-emitted with logger `docker.<resource>`, message = lowercased state, field `name=`. Level: terminal states INFO, transitional (Creating, Starting, Restarting, Stopping, Removing, Recreate, Pulling, Building, Waiting) DEBUG, Warning WARNING, Error ERROR. Resource in the logger, verb in the message, because one command emits several verbs (Unit 20, `dockerlog.go`). These lines go straight to stdout and are not part of the copy-on-failure buffer.
- Loose stderr severity (`Warn:`, `Error:`, `Crit:`, `Fatal:`, `Info:`, `Debug:` as the first token, case-insensitive, e.g. wkhtmltopdf/Qt) is reformatted under logger `report.wkhtmltopdf` (`looselog.go`, Unit 36).
- Recipe recap: `echo.run` lines with an empty message and fields `step=i/n status=… cmd=… took=…`; the final `run summary` always carries `errors` and `warnings` even at zero (Unit 37). Details: [scripting.md](scripting.md).

## Streamed-line pipeline

One shared path, `sess.emitStreamLine` (used by up/down/update/install/logs, actions, checkpoint, deploy, link): strip ANSI, then compose progress, then loose severity, then the kind classifier, then verbatim. The `shell` PTY transform mirrors it with `renderLogLine` and `styleShellBanner` in front. Source: `internal/repl/repl.go:emitStreamLine`, Units 36, 45, 46, 58.

- Classifier (`classifyOdooLog`): DEBUG faint, INFO info, WARNING warn, ERROR and CRITICAL err (CRITICAL has no kind of its own). An unmatched line inherits the previous kind only when that was err or warn, so tracebacks stay grouped and enter the copy-on-failure payload. `lineLevel` is the context-free variant (keeps ERROR and CRITICAL apart) used by `report`.
- **Anchor the level regex to the full prefix** (`^ts pid LEVEL `), not the bare keyword. A traceback frame containing `--log-handler X:DEBUG` was once able to break inheritance (Unit 16, `loglevel.go:odooLogPrefix`).
- Two formats are understood: standard Odoo and loguru from custom modules (`ts.mmm | LEVEL | module:func:line - msg`; `:func:line` renders Faint). Both feed level coloring, error/warning counting and inheritance. Without the loguru branch a loguru ERROR during tests was invisible to failure detection (Unit 19). A loguru-looking line that fails the full regex falls back to kind styling.
- Counting (`runStats`): only level-prefixed lines count (Odoo, loguru, compose Error/Warning), so traceback continuations do not inflate totals. **A loose `Warn:` counts as a warning but a loose `Error:` does not count as an error**, so a noisy tool's stderr cannot fail a healthy run. The loose reformat is suppressed while the previous classified line was err/warn so a traceback tail `SomeError: ...` stays with its frames (Unit 36).
- **ANSI traps** (Units 45, 58). Odoo's ColoredFormatter emits SGR codes when its stdout is a TTY: under `shell` (`compose exec -t`) and when `docker compose logs` replays a container that ran attached to a TTY. `update`/`install` use `exec -T` and are plain. The SGR codes break the log regexes, so `stripANSISeq` must run first. Echo's colors replace docker's native ANSI on `logs` (accepted trade-off).
- **Gutter trap** (Unit 58): `docker compose logs` prepends `<service>  | `, which breaks every parser. Both `Logs` and `LogsFollow` pass `--no-log-prefix` (needs Compose v2). Consequence: `logs --all` interleaves services without attribution (accepted).
- Shell PTY transform (`internal/docker/shell.go`): complete lines are transformed; a partial line is flushed raw at once unless it starts with a digit (the only start of a forming Odoo log line), which waits `partialFlushDelay` = 30 ms for its newline, so key echo never lags. The capture buffer keeps raw ANSI-free text. Only `shell` sets a transform; `bash` and `psql` are raw passthrough. Styling lives in `repl`, which `docker` cannot import, so it is passed down as the opaque closure `docker.LineTransform`.
- Shell startup block (`shellbanner.go`): `env/odoo/openerp/self:` globals render Accent key plus Dim value; lines starting `Python `, `IPython `, `Type '`, `Tip: ` render Faint; anything else verbatim.
- `--silent[=level]` on a recipe step drops screen and tee in both `sess.print` and `emitOdooLog` through `outputSuppressed`, but never the `lastOutput` buffer, so `report` still sees the lines (`silence.go`).

## stdout contract and `--json`

- Default: everything prints to stdout through `sess.print` / `emitOdooLog`.
- **Machine-readable commands keep stdout clean**: with `--json` stdout carries only the marshaled value, written straight to `os.Stdout` (no ANSI, no log line, no start/finalize line on success), and diagnostics go to stderr through `emitOdooLogTo(os.Stderr, ...)`. Exit codes: 0 ok, 2 usage or `ErrNonInteractive`, 3 cancelled, otherwise 1. Users: `modstate`, `lint`, `link --list`, `doctor`, `compare --targets`, `deploy`, `checkpoint`, `actions`, `logview` (flags in `commandFlags`). Source: Unit 47 (first), `internal/repl/modstate.go`, `deploy.go:finishDeployJSON`.
- `deploy --json` additionally routes progress and streamed remote lines to stderr and suppresses the `--push` change tree. A new `--json` command must do the same: nothing but the JSON on stdout.
- JSON shape conventions seen in code: nullable fields serialize as `null` (modstate `version`), empty collections as `[]` not `null` (deploy `modules`, lint `skipped_passes`). `modstate` defaults to installed-only in both modes and `--all` is the single widening flag (Unit 47).
- Human tables (`ps`, `db-list`, `modstate`) use `Line{Kind:"table"}` and close with an Odoo-style count line (`echo.ps: containers listed count=N`). `ps` never regresses: if `ps --format json` fails or does not parse it streams raw `docker compose ps`. Status color: health wins over lifecycle (healthy ok, unhealthy err, starting warn; running ok, restarting warn, exited/dead/removing err, paused/created dim). Ports show `pub->target[/proto]`, unpublished omitted. `db-list` marks the active DB with a green `●`. (Units 47, 56, 57; `internal/repl/ps.go`, `dblist.go`.)

## Pickers

- One custom Bubble Tea model, `fuzzyPicker` (`internal/cmd/picker.go`), serves every list picker (multi and single). Chosen over `huh.MultiSelect` for an always-on filter, Tab toggle and full layout control (Unit 06). `huh` is still used for confirms and forms (`BuildHuhTheme` maps the palette; `modules --config` is a huh form).
- **The filter is case-insensitive substring, not fuzzy** despite the name (`recompute`). No ranking. Fine for datasets in the tens (Unit 06).
- Keys: filter is always active (no `/`); `tab` toggles (disabled in single mode); `enter` confirms; `esc`/`ctrl+c` cancels; `up`/`ctrl+p`, `down`/`ctrl+n`, `pgup`/`pgdn` move; `ctrl+d` toggles the deployed mark of the highlighted commit row and `ctrl+a` marks or clears all visible markable rows (deploy picker only, [deploy.md](deploy.md)).
- **`ctrl+x` quits Echo from inside any picker** (returns `cmd.ErrQuit`, handled by `session.handleQuit`, exit code 0), mirroring `ctrl+x` at the prompt and in logview. It is not a cancel.
- Cancel vs empty confirm are distinct: `runFuzzyPickerCore` returns `canceled`; Enter with nothing selected is not a cancel there. `runFuzzyPicker` collapses both into `ErrCancelled`. The `update` "repeat last" confirm on an empty confirm is gated on `ModulesOpts.Interactive`, true only in the live REPL ([modules-and-odoo.md](modules-and-odoo.md)). `runFuzzyPickerWithSelected` starts with a set checked and treats an empty confirm as "cleared".
- **Pickers fail closed without a TTY**: `requireTTY(hint)` returns `ErrNonInteractive` (exit 2) with a hint to pass the selection as an argument or `--force`. Every new interactive picker or confirm must call it first.
- Look ("log-framed", Unit 55): a left `│ ` bar in the stage color, a Dim title with a Faint `(visible/total)` counter, a `filter › ` line (the stage color, Faint placeholder), `❯` cursor, `[ ]`/`[×]` checkboxes in multi mode, `↑ N more` / `↓ N more` scroll hints, a Faint help line. No title box or divider. `chromeLines` = 4 non-list lines; list window = terminal height minus 4, minimum 3, 15 before the first size message.
- Row semantics: the first run of 2+ spaces in a label splits name from a secondary tail (`splitLabel`), rendered one step lighter than Dim (`Lighten(Dim, 0.3)`). Cursor row Accent/stage bold; deployed rows Faint ("muted = already deployed"); previous-run rows Info ("highlighted = last update"). A leading `[ADD]`-style tag in the tail is colored by type (ADD/FEAT/NEW green, FIX/BUG/HOTFIX/REM red, IMP/PERF Info, REF/MERGE Accent2, DOC Warning, WIP Faint, unknown tags hashed into the pastel rotation); a `wt: <path>` path is Info-tinted (`internal/cmd/tagcolor.go`). Legend hints appear only when the corresponding marks exist.
- **`filterWidth = 48` is load-bearing**: bubbles' `textinput` truncates its placeholder to one rune when `Width` is 0, so the placeholder rendered as "t".
- `PickOne` is the exported single-select for callers outside `cmd` (recipe `--pick`).

## Prompt

- Built by `promptBuilder` (`internal/repl/prompt.go`): head glyph (Accent) from `banner.LogoIcon(cfg.Logo)`, then the configured segments, then `:~$ `. Segments (`[prompt] segments`, default `name`, `version_db`, `stage`, `health`; unknown names warn once at startup and are dropped, duplicates collapsed):
  - `name`: `echo-<compose project>` in Accent. Name resolution: `COMPOSE_PROJECT_NAME` env, then per-project `compose_project`, then the normalized base of the project path; right-cut with `…` at `name_max` (default 18, rune-safe, clamped to at least 4).
  - `version_db`: `[<odoo version>.0 · <db>]`, brackets Dim, db Dim.
  - `stage`: the stage word in the stage color, bold. This is the only place the stage colors the prompt; the bracket stays neutral so there is one clear "which environment" signal (Unit 17).
  - `health`: Nerd Font docker and postgres glyphs (`nf-md-docker`, `nf-md-postgresql`) colored by state: running Success, restarting Warning, stopped Error, otherwise Faint. Omitted when `OdooContainer` or `DBContainer` is empty.
- Health is read with `docker inspect` through a TTL cache (`health_ttl`, default 5 s; 500 ms per-call timeout; any failure becomes `unknown`). It is synchronous, so a cache miss can block the prompt up to 500 ms; async refresh was deliberately deferred. The cache is invalidated after up/down/restart.
- The `logo` config key only selects the prompt glyph (`banner.LogoIcon`, default plus `planet`/`python`/`anchor` cases left in code). No command changes it and no alternative ASCII logos exist (Unit 14 closed).

## Banner

- Startup header in `internal/banner/header.go`: a rounded two-column box (`╭─── Echo v<version> ───╮`), left = greeting, wordmark, `<theme> · <stage>`, shortened path; right = tips and "What's new". Width from the terminal, 80 if unknown or under 40; left column is 2/5 of the width, minimum 28.
- Wordmark color is the stage color, not the theme accent (Unit 15). Two figlet styles chosen at startup: `soundwave` (Calvin S plus a wave line) and `shadow` (ANSI Shadow with a vertical gradient: Lighten .45, .22, base, Darken .12, .24, .36; plus a `)))` ripple). `banner = auto|soundwave|shadow` in `global.toml`, default `auto` (random). Widths are computed from the art (`shadowWidth`, `shadowRippleWidth`, about 85 and 95 columns): below the first, shadow falls back to soundwave so it never breaks the box border. `ECHO_BANNER=soundwave|shadow` forces a style and bypasses the width guard (previews, VHS recordings; it may overflow). `resolveBannerStyle` takes an injectable coin for tests.
- The "What's new" and tips text in `buildRight` is hardcoded copy; update it when shipping a headline feature.

## Command highlight and completion (REPL input line)

- Rendered by `lineModel.View()` (`internal/repl/lineinput.go`), not by `textinput`, which supports only one uniform text style; the textinput's own cursor is spliced in so blink still works. Echo never sets `textinput.Width`, so there is no horizontal scroll window to reproduce.
- First token, three states (Unit 21): exact command Success bold, valid prefix neutral, impossible Error. Prefixes stay neutral so the line does not flash red mid-word. Validity derives from `Registry` plus `exit`/`quit` (handled in `Start`, not in `Registry`) so it cannot drift.
- Flags (Unit 24): a known flag of that command is Accent bold, an unknown one is Faint and **never red**, because passthrough commands (`up`, `down`, `logs`, `connect`) forward arbitrary flags. `--flag=value` is validated on the part before `=`. `universalFlags` (`--build`, `-b`) are known everywhere and kept out of `commandFlags` so help and the cross-check tests stay clean.
- `commandFlags` lists only user-facing flags; flags Echo builds itself (`-e`, `--no-http`, chrome flags) are excluded. It is the single source for highlight, Tab completion and the help cross-checks; registering a command: [code-standards.md](code-standards.md).
- Tab (bash-style, strict case-sensitive prefix, not fuzzy): empty buffer ignored; first token completes commands; a last token starting with `-` after a command completes that command's flags; values are never completed (`install sa<Tab>` is a no-op). One match completes with a trailing space, several extend to the longest common prefix, a second consecutive Tab prints the list above the prompt.
- Keys at the prompt: `ctrl+d` quits only on an empty line, `ctrl+x` always quits (nano-style), `ctrl+c` aborts the line. History: `~/.config/echo/history`, capped at 1000, consecutive duplicates dropped.

## Icons

- Nerd Font glyphs are assumed in the prompt, `modules` (`cod-package`, U+EB29, Accent), the push change tree and the sequence builder. File-type glyphs in rich output are gated by `icons` (`auto` default, `on`, `off`) and the `ECHO_ICONS` env var, which wins. `auto` is on only for an interactive stdout on a non-plain terminal (`TERM` not empty, `dumb` or `linux`), off when piped, so recipes, `--log` and CI stay glyph-free (`internal/repl/icons.go:resolveIcons`). The prompt glyphs are not gated.
- `modules` uses its own `renderModuleList` because a single outer `.Render` over a block would reset per-item ANSI (Unit 58).

## Copy and clipboard

- `sess.lastOutput` buffers the last command's lines (cap 5000, oldest dropped, with a truncation marker); the dispatcher resets it for every non-meta command. `copy-last [--errors]` copies it; `copy-last`, `help`, `clear` and `logview` are meta and do not reset it. The buffer stores plain `Line.Text`, never ANSI.
- Auto-copy on failure applies to every subprocess command (install/update/uninstall, bash/psql/shell, i18n-*, db-backup/restore/drop, up/down/restart, connect) and not to read-only ones (`ps`, `logs`, `modules`, `db-list`). Payload: the plain Odoo-style header line, then everything from the FIRST err/warn line to the end (`FromFirstError`), or the whole buffer when none. A cancel never auto-copies.
- Interactive shells run under a host PTY (`creack/pty`) because `compose exec` allocates a PTY inside the container and fuses stdout and stderr; the capture tee must sit on the host side. **SIGINT is detected through an `atomic.Bool` set in the parent's handler, not through the exit code**: Odoo's shell traps SIGINT, prints `KeyboardInterrupt` and exits 1, not 130. Interrupted becomes WARNING `echo.<cmd>.cancelled` with no auto-copy. Non-TTY stdin uses a plain pipe tee. Source: Unit 16.
- `clipboard.WriteAll` order depends on `isRemote()` (`SSH_TTY`, `SSH_CONNECTION` or `TMUX` set): remote tries OSC 52 first (native helpers would hit the remote host's clipboard, and tmux needs OSC 52 to reach the terminal), local tries native first (`pbcopy`, `wl-copy`/`xclip`/`xsel`, `clip`) and OSC 52 last. OSC 52 writes to `/dev/tty`, falling back to stderr. No route returns `ErrUnavailable`. Source: `internal/clipboard/clipboard.go`.
- Live verification of the tmux/OSC 52 path and of most visual units (pickers, banner, `ps`, `logs`, shell banner) is not recorded; see the ledger in [operations.md](operations.md#live-verification-ledger).

## Traps checklist for UI changes

- A restyle must be verified against the real stream, not by reading code: Unit 57 declared `logs` restyled and was wrong for two independent reasons (gutter and ANSI) until Unit 58.
- Change the log shape in both renderers (`repl` and `cmd/connect_log.go`) and the plain twin.
- New streamed path: route lines through `emitStreamLine` (strip ANSI first) or they print with foreign colors and are invisible to error counting.
- New interactive screen: `requireTTY` first, honor `ctrl+x` as `ErrQuit`, take the bar color from `PromptColor(stage)` when a stage is known.
- New `--json`: stdout only the JSON, everything else to stderr (see above).
