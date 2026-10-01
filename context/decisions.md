# Decisions

> Every decision taken in this project, with its reason and source. Newest first within each area. Never deleted: a reversed decision is marked superseded.

## Architecture

### 2026-09-30 · A config file that does not parse stops Echo; a corrupt state file is kept aside, not fatal
- **Why:** Loading defaults over a hand-edited file that has a typo let the next save overwrite the user's config. Config is the user's, so Echo refuses to start (exit 2) or to write; state is Echo's own convenience, so it stays best-effort and only the overwrite is made safe by renaming the corrupt copy.
- **Rejected:** Keeping the silent fallback to defaults; failing commands on a corrupt state file (a corrupt deploy history would block every deploy).
- **Source:** Unit 127; `internal/config/config.go` (`ParseError`, `loadTOMLFile`, `preserveCorrupt`), `main.go` (`exitOnConfigError`).
- **Status:** active

### 2026-09-09 · Config saves are load-modify-write and always assign the fields a path owns
- **Why:** Rebuilding the file from a literal dropped every field the saving path did not know; assigning owned fields even when nil keeps clear-on-default working while untouched fields survive.
- **Rejected:** Rebuilding the config from a literal; writing only non-default fields (cannot clear a value back to default).
- **Source:** Unit 118; `internal/config/` save paths.
- **Status:** active

### 2026-07-06 · Prefer concrete structs and pure functions over interfaces; test through named transport seams
- **Why:** Matches the rest of the codebase and keeps logic testable without a container or server: pure parsers (`parseTestArgs`, Unit 75), a round-trip guard tying parser to composer (Unit 77), a pure `rollbackDecision(p, tty)` (Unit 101), SSH seams such as `actionsRunSSH` (Unit 105).
- **Rejected:** A `viewSource` interface for `remoteView` (Unit 79); logic inlined in the REPL layer where only a real container can exercise it.
- **Source:** Units 75, 77, 79, 101, 105.
- **Status:** active

### 2026-06-12 · Git is driven through `os/exec`, not a Go git library
- **Why:** Hypothesis: the tracker records the choice but not the reason; most likely it avoids a dependency and uses the user's own git for `reset --keep`, `archive` and pushes to a holding ref.
- **Rejected:** A Go git library (not adopted).
- **Source:** Units 60, 61 (tracker).
- **Status:** active

### 2026-06-10 · Project aliases are a separate registry in `global.toml`; backfill is explicit
- **Why:** Same architecture as `[connect_targets]` but a different meaning (local checkout, not a server). A real directory always beats an alias; resolution falls back to a connect target whose `remote_path` is a local directory.
- **Rejected:** Reusing connect targets as the alias store; automatic migration of existing targets (`alias --migrate` is explicit).
- **Source:** Unit 48; `config.ResolveProjectAlias` (`internal/config/project_alias.go`), `main.go`.
- **Status:** active

### 2026-06-09 · Non-interactive safety is a TTY check at the picker/confirm layer; projectless one-shots are the general rule
- **Why:** One guard (`requireTTY` -> `ErrNonInteractive`, exit 2) covers every picker, confirm and init/reset without threading a `batch` boolean through each `Opts` struct. The former projectless `connect` branch became the rule for every Registry command that needs no project (`projectlessOneShot`).
- **Rejected:** A `batch` flag on every `Opts`; a separate script binary or mode.
- **Source:** Unit 31; `internal/cmd/interactive.go`, `main.go` `projectlessOneShot`.
- **Status:** active

### 2026-06-08 · The binary version always carries the commit (`+<shortsha>`, `.dirty` on a dirty tree)
- **Why:** Identifies exactly which build is running, not only when there are uncommitted changes. Set through Makefile ldflags and `FullVersion()`.
- **Rejected:** Adding the suffix only when there are uncommitted changes.
- **Source:** Tracker Architecture Decisions (Unit 18 notes); `internal/repl/repl.go` `FullVersion`.
- **Status:** active

### 2026-05-13 · Echo never writes into the user's repository and never creates a `.gitignore`
- **Why:** Project-wide decision of the owner: config and state live under `~/.config/echo` (atomic tmp+rename writes), keyed per project, so a user's repo is never polluted.
- **Rejected:** Per-project files inside the repo; creating or editing `.gitignore` on `init`.
- **Source:** Units 02, 09 (specs).
- **Status:** active

### 2026-05-11 · Streaming commands pipe and scan; interactive ones hand the TTY straight through
- **Why:** Piping lets Echo style, classify and capture batch output; follow and shell sessions keep native TTY behaviour.
- **Rejected:** Always piping (breaks follow/shell); always passing the TTY through (no styling or capture).
- **Source:** Unit 04.
- **Status:** active

### 2026-05-07 · Container and database lists come from live docker calls, not from parsing the compose file
- **Why:** Avoids a YAML dependency and compose variable-interpolation edge cases; the old detection package was deleted. `FindRoot` recognises only `docker-compose.yml` as the project marker.
- **Rejected:** Parsing `docker-compose.yml`.
- **Source:** Unit 03.
- **Status:** active

## UI

### 2026-06-19 · The header banner uses two figlet styles of the wordmark, picked at random and coloured by stage
- **Why:** Each environment looks distinct and consistent with the prompt and pickers; shades are derived in code with `theme.Lighten`. The two-column header stays.
- **Rejected:** A full-width banner above the header; hardcoded hex colours. The planet/python/anchor logos were never built (see [Unit 14 (`theme`, `logo`, `version`, `stage` meta-commands) is closed without implementation](#2026-09-30--unit-14-theme-logo-version-stage-meta-commands-is-closed-without-implementation)).
- **Source:** Unit 15.
- **Status:** active

### 2026-06-11 · Pickers use the log-framed style: a stage-coloured left bar, no title or divider
- **Why:** Integrates the picker into the log stream; the stage colour tells which environment the action will hit. Chosen by the owner among several styles.
- **Rejected:** The bold-title and `----` divider layout.
- **Source:** Unit 55.
- **Status:** active

### 2026-06-11 · `ps` renders a styled table from structured docker output with a raw fallback
- **Why:** `docker compose ps --format json` is stable to parse; the text table is not. On any JSON failure the command streams the raw output, so there is no regression.
- **Rejected:** Parsing the text table.
- **Source:** Unit 56; `docker.PSList`.
- **Status:** active

### 2026-06-11 · Echo colours replace docker's native ANSI on `logs`
- **Why:** Consistent output across all commands. `--no-log-prefix` removes the service gutter and `stripANSISeq` removes the colours Odoo stored under a TTY before the line is parsed.
- **Rejected:** Passing docker's ANSI through (accepted trade-off: users lose Odoo's native colours).
- **Source:** Units 57, 58.
- **Status:** active

### 2026-06-10 · Odoo shell output is restyled in Echo's style, not silenced
- **Why:** Startup logs, globals and the Python banner are shown with accent keys and faded text; the transform is opt-in per call and the capture stays raw.
- **Rejected:** Silencing with `--log-level=warn`; silencing plus a summary; muting the whole startup block.
- **Source:** Units 45, 46 (owner's form choices).
- **Status:** active

### 2026-06-09 · Loose `Warn:`/`Error:` lines from tools are reformatted to Odoo style; only `Warn:` counts
- **Why:** Tools such as wkhtmltopdf emit unstructured severity lines; reformatting keeps the stream uniform and a loose `Error:` must not fail a run.
- **Rejected:** Only colouring them; hiding them; counting both severities.
- **Source:** Unit 36 (owner's choices in a form).
- **Status:** active

### 2026-06-08 · Flag highlighting is accent for known flags and faint for unknown ones, never red
- **Why:** Passthrough commands (`up`, `down`, `logs`, `connect`) forward arbitrary flags, so red would be a false alarm. Odoo-style framing applies to every command.
- **Rejected:** Red for unknown flags.
- **Source:** Unit 24 (`commandFlags` registry).
- **Status:** active

### 2026-06-08 · Command highlighting covers the first token only, with three states
- **Why:** Exact match green bold, valid prefix neutral, impossible red; neutral for prefixes so the line does not flash red mid-word. Scope chosen by the owner (fish style).
- **Rejected:** Colouring arguments and flags in the same unit (done later in Unit 24).
- **Source:** Unit 21.
- **Status:** active

### 2026-06-02 · Docker lines use one logger per resource with the verb in the message
- **Why:** One command emits several verbs; the dotted lowercase resource logger (`docker.container`) mirrors Odoo's own logger naming. The compose output classifier is separate from the Odoo classifier to avoid false positives on messages that contain "Error" (`parseComposeProgress`).
- **Rejected:** One logger per verb; one shared classifier.
- **Source:** Units 08, 20.
- **Status:** active

### 2026-05-28 · Loguru lines get a second regex next to the Odoo one
- **Why:** The two formats differ in separator, pid, db and millisecond separator; one combined regex would be fragile.
- **Rejected:** A single combined regex.
- **Source:** Unit 19.
- **Status:** active

### 2026-05-18 · The prompt health cache is synchronous and the stage colour lives on the stage chip only
- **Why:** Avoids premature optimisation (no refresh goroutine) and gives one clear signal for "which environment am I on".
- **Rejected:** A background refresh; colouring the project name or brackets by stage.
- **Source:** Unit 17.
- **Status:** active

### 2026-05-15 · Log lines are rendered by hand in Odoo's exact shape; copy goes through every subprocess command
- **Why:** `charmbracelet/log` cannot reproduce Odoo's line shape. Auto-copy was extended from module commands to all subprocess commands; the clipboard prefers OSC-52 when running remotely.
- **Rejected:** `charmbracelet/log`.
- **Source:** Unit 16; `internal/clipboard/clipboard.go`.
- **Status:** active

### 2026-05-13 · Command history is a file with a 1000 cap; command completion is strict prefix
- **Why:** History persists in `~/.config/echo/history` with consecutive dedupe. The command set is small and curated, so fuzzy matching is reserved for pickers; Registry stays flat, enforced by tests instead of reflection.
- **Rejected:** In-memory history; fuzzy command matching.
- **Source:** Unit 13; tracker Resolved Questions.
- **Status:** active

### 2026-05-12 · Pickers are a custom Bubble Tea model with substring matching
- **Why:** Always-on filter, Tab toggle and full layout control; datasets are in the tens so ranking is unnecessary. `modules --config` keeps a `huh` form.
- **Rejected:** `huh.MultiSelect`; fuzzy ranking in v1.
- **Source:** Unit 06.
- **Status:** active

## Remote targets

### 2026-09-30 · An undeclared remote stage is treated as prod; a broken server profile fails the command
- **Why:** The server profile is the only source of a remote stage, and an empty or misspelled `stage` silently disabled every remote gate. Failing closed costs a confirm on a dev box with a sloppy profile; failing open can cost production. A profile that does not parse used to read as empty (no containers, no stage, no actions), which is the same hole.
- **Rejected:** Defaulting a remote to `dev` like the local rule (the laptop is not the risk); refusing to run on an undeclared stage (blocks every read-only verb).
- **Source:** Unit 127; `internal/cmd/db_remote.go` (`remoteConnectTarget`, `warnUndeclaredStage`), `internal/cmd/connect.go` (`fetchRemoteProfile`).
- **Status:** active

### 2026-09-30 · Unit 114 (`connect --serve`) is archived as a discarded proposal
- **Why:** Owner decision; the proposal was never implemented. Its premise, kept as rationale only: a minted session id must not reach stdout, logs or `--json`, so it would have been served as a loopback proxy URL instead of printing the cookie.
- **Rejected:** `connect --json` printing the session id (puts the secret in the agent transcript).
- **Source:** Unit 114 (spec dated 2026-08-27, never committed); owner, 2026-09-30.
- **Status:** active

### 2026-09-09 · Reverb behaviours switch on `rsc.reverb != nil`, filled only from the server marker plus local credentials
- **Why:** The classic path stays identical for anyone without credentials, and paths are derived from the profile instead of a resolve call (zero HTTP for what does not need it).
- **Rejected:** A mode flag; a resolve call for every path.
- **Source:** Unit 120; `internal/cmd/reverb.go`.
- **Status:** active

### 2026-09-09 · `update` is delegated to Reverb only with marker plus credentials, never for `--i18n` or `--all`
- **Why:** Same switch as Unit 120; Reverb's update endpoint cannot express either option.
- **Rejected:** Delegating unconditionally.
- **Source:** Unit 123.
- **Status:** active

### 2026-09-09 · `-E` is deprecated now and deleted later in Unit 122
- **Why:** Deleting it in the same unit would have left no way to point at a Reverb environment until Reverb unit 30 (server-side profile writing) shipped. That unit is deployed and verified since 2026-09-09, so Unit 122 is unblocked.
- **Rejected:** Deleting `-E` in the same unit (the original E4).
- **Source:** Units 120, 121; spec 121.
- **Status:** active

### 2026-09-03 · Reverb SSH identity comes from a local `[reverb] ssh_host` alias that wins over the payload; the payload `ssh_port` is only a warning
- **Why:** Echo has never had a port field; a literal `user@ip` loses user, key and ProxyJump; plumbing `ssh -p` would open a second configuration channel that disagrees with ssh_config. Cost accepted: one ssh_config block per Reverb host (not per environment). The reverb-side brief asked for `-p`; this was reported back as a deliberate deviation.
- **Rejected:** `ssh -p <ssh_port>` plumbing.
- **Source:** Unit 108; `internal/reverb/`.
- **Status:** active

### 2026-09-03 · `-E <spec>` is sugar for `--from env:<spec>`; deferred commands refuse loudly
- **Why:** `env:` is a reserved prefix of the target namespace, so the one existing `from` string carries it through 22 call sites with no signature change. A `reverbDeferred` map (absent = supported) replaced a default-refuse switch; refusal is a usage error naming the reason rather than running a path that fights Reverb.
- **Rejected:** A new parameter or field threaded through every call site; a default-refuse switch.
- **Source:** Units 107, 108; `reverbDeferred` in `internal/cmd/reverb.go`.
- **Status:** active

### 2026-07-23 · `link --list` and `--next` stay offline
- **Why:** An instant toggle must not stall on SSH, so no stage or health per row.
- **Rejected:** Probing each target for stage/health in the switcher.
- **Source:** Unit 106.
- **Status:** active

### 2026-07-13 · Deploy-related policy resolves server-first with a local fallback
- **Why:** The policy protects the server's state and the stage already comes from the server profile; a client-only policy split the brain. Applies to push destination, checkpoint policy (field by field), `[deploy] push` and actions (wholesale). The server has no defaults so local config can apply.
- **Rejected:** Client-only policy.
- **Source:** Units 90, 91, 95; specifics in [Checkpoint policy is resolved server-first, field by field, with a local fallback](#2026-07-13--checkpoint-policy-is-resolved-server-first-field-by-field-with-a-local-fallback) and [Deploy actions resolve wholesale in fixed phases, fail-fast; `post_deploy` never rolls back a verified deploy](#2026-07-13--deploy-actions-resolve-wholesale-in-fixed-phases-fail-fast-post_deploy-never-rolls-back-a-verified-deploy).
- **Status:** active

### 2026-06-24 · Remote `restart` gates on prod; remote `logs` follows by default
- **Why:** A remote prod restart is an outage, so it is stricter than local; follow stays the default for consistency with local.
- **Rejected:** Bounded-by-default remote logs.
- **Source:** Unit 72.
- **Status:** active

### 2026-06-12 · `link` reuses the target resolution of Unit 50 and the `[connect]` section; no new storage format
- **Why:** One model of remote binding for `i18n-pull`, `deploy` and `link`.
- **Rejected:** A dedicated link config.
- **Source:** Unit 60.
- **Status:** active

### 2026-06-09 · `connect` uses a dedicated persistent Chrome profile; a new tab by default, `--new-window` for incognito
- **Why:** `session_id` is HttpOnly so it can only be injected over CDP, which needs `--remote-debugging-port`: not viable on the user's daily Chrome. A temp profile per run littered `$TMPDIR`. Incognito windows have their own cookie jar, so several users can be logged in at once.
- **Rejected:** Driving the user's daily Chrome; a temp profile per run.
- **Source:** Unit 29.
- **Status:** active

### 2026-06-09 · The `connect` session cache always probes before reuse
- **Why:** One cheap request is more reliable than trusting the TTL alone; the picker offers recent logins first with a "fetch all users" sentinel to skip the `res.users` query.
- **Rejected:** Trusting the TTL.
- **Source:** Unit 28.
- **Status:** active

### 2026-06-08 · `connect` forges a session; it never authenticates with a password
- **Why:** The mint reproduces `Session.finalize` (`root.session_store.new()` + `_compute_session_token`), so no password travels and 2FA is bypassed on purpose (development use); the only brake is the prod confirm.
- **Rejected:** Authenticating with credentials.
- **Source:** Unit 18; tracker Architecture Decisions.
- **Status:** active

### 2026-06-08 · The cookie is landed over CDP, not by bookmarklet or helper server
- **Why:** Odoo's `session_id` is HttpOnly so JS cannot overwrite it; a helper server needs an inbound port and `--tunnel` never landed the hit. CDP (`Network.setCookie` + `Page.navigate`) needs zero ports and no Odoo module. Implemented with `github.com/coder/websocket` and three hand-issued commands.
- **Rejected:** Bookmarklet; helper HTTP server on :8003; SSH `-L` tunnel; `chromedp` (heavy cdproto tree and process manager); installing an Odoo module.
- **Source:** Unit 18; paused helper-server work on 2026-05-25.
- **Status:** active

### 2026-06-08 · `connect` runs on the laptop; the server is touched only over SSH to mint
- **Why:** The browser is always local so the cookie lands local; only the Python mint script runs in the remote container, over SSH with `BatchMode=yes` so it fails fast instead of hanging on a password prompt and leaves keys to the user's ssh config or agent.
- **Rejected:** Nothing recorded.
- **Source:** Unit 18.
- **Status:** active

### 2026-06-08 · A remote target inherits the server's Echo profile; it is never redeclared locally
- **Why:** Single source of truth and the basis of link mode: `fetchRemoteProfile` reads `global.toml` and `projects/<sha256(remote_path)>.toml` over SSH (the same hash local Echo uses), which required persisting `project_path` and a startup migration (`BackfillProjectPath`).
- **Rejected:** Per-target configuration redeclared locally.
- **Source:** Unit 18; `internal/cmd/connect.go`.
- **Status:** active

### 2026-06-08 · Direct targets are built on `~/.ssh/config`; Echo does not manage SSH
- **Why:** Targets reference the Host alias; `connect <name>` stores named targets in `global.toml` and discovers projects by reading the server's Echo config, never by scanning dockers. Consequence: there is no port field, non-standard ports need an ssh alias.
- **Rejected:** Echo-managed SSH settings; scanning the server's containers.
- **Source:** Unit 18; tracker Architecture Decisions.
- **Status:** active

## Deploy

### 2026-09-30 · Declined: an exclusive push destination per target
- **Why:** Owner's reason: it solved one project's configuration (a destination shared between targets) rather than improving Echo for everyone. The shared-destination case is handled generally instead: the lock lives under `remote_path`, not in the destination, and per-module source plus the code snapshot make a shared destination recoverable.
- **Rejected:** Forcing one destination per target.
- **Source:** Units 124-126 plan, 2026-09-30.
- **Status:** active

### 2026-09-30 · Each module resolves a source first; the target's transport then ships it (`mod@ref`, `--at`)
- **Why:** "What to ship" is separate from "how". On rsync targets a module chosen from commits now ships the tree at its newest selected commit from `git archive`, never the disk (which leaked later commits and uncommitted edits); `mod@ref` ships a module as it is at any ref, located in the ref's tree. Uncommitted changes in a committed or ref source are ignored with a WARNING. Last send wins: a ref-pinned module re-sent from another source is cleaned first (overlay reverted on git targets, `--delete` on rsync). Deferred, not decided: `push mod@ref`, `deploy --patch`, and a dependency check for partial deploys (warn in the dry-run when what ships removes methods or fields used by modules that stay).
- **Rejected:** Shipping the working tree for commit selections (the old behaviour); a separate command per source.
- **Source:** Unit 125; `locateModuleAt` in `internal/cmd/deploy_source.go`.
- **Status:** active

### 2026-09-30 · Content shipped from `git archive` is synced with `--delete`; working-tree pushes keep `--delete` opt-in
- **Why:** A file deleted at the shipped commit must not survive in the running code or built image; the deletion is scoped per module directory by rsync's per-module trailing slash. Deleting server files based on whatever is on the local disk is a separate decision.
- **Rejected:** `--delete` always; never for archives.
- **Source:** Unit 124 section C, Unit 125.
- **Status:** active

### 2026-09-03 · `deploy --set-code` cleans the overlay by default; `--keep-overlay` opts out; the local reset uses `reset --keep`
- **Why:** Leftovers from modules the new base may not contain make a rebase unsafe; locally `reset --keep` refuses on collision instead of destroying work, and `--discard` is explicit. Promote stays local, so a one-gesture rebase lives on deploy, which owns SSH, target resolution and the prod gate.
- **Rejected:** Always-incremental semantics; `reset --hard`; a `promote --reset --push`.
- **Source:** Unit 112 (locked with the owner in the spec).
- **Status:** active

### 2026-09-03 · `--set-code` does not create a server branch named after the ref; the deploy line stays `echo/deploy`
- **Why:** A real branch name implies it syncs with `origin`, but Echo never pulls, so it would diverge on every deploy (the `git_branch = "staging"` incident); tags and SHAs have no branch to create; and a content command must not become a config writer (`--set-*` stay config-only).
- **Rejected:** Creating or checking out `deploy/dev` on the server.
- **Source:** Unit 113.
- **Status:** active

### 2026-07-23 · `deploy actions` are scoped by `--from`/`--remote`; the wholesale upload prompt is gone
- **Why:** The scope selector picks which target's actions are being authored; uploading a local list wholesale was surprising. Unit 92's wholesale resolution is unchanged.
- **Rejected:** A wholesale upload prompt.
- **Source:** Unit 105; seam `actionsRunSSH`.
- **Status:** active

### 2026-07-20 · `promote --show-branch` exits 1 with a WARNING when nothing is configured
- **Why:** Scripts and the odoo-probe skill test it with a plain `if`; exit 0 means a destination exists. Documented deviation from the usual usage-error exit.
- **Rejected:** `ErrUsage` / exit 2; listing it in `link --show`; a generic config show.
- **Source:** Unit 103.
- **Status:** active

### 2026-07-20 · Git deploy keeps identical hashes by pushing to a holding ref, and is opt-in per target
- **Why:** `git push --force` to `refs/echo/incoming` (never checked out, so `receive.denyCurrentBranch` cannot reject it) then `reset --keep` preserves the dirty overlay, colliding paths are discarded with a WARNING; cleanup is a `push --clean` flag; rollback restores code with the DB. Out of scope: auto-clone, submodules, lfs, multi-repo layouts.
- **Rejected:** Cherry-pick or re-commit on the server (new SHAs, history no longer matches); an always-on mode.
- **Source:** Unit 102.
- **Status:** active

### 2026-07-17 · `deploy --test` is a single toggle that prints the result, with a persistent module list
- **Why:** Owner's choice: one flag flips and reports the final value; the list is managed with add/remove (headless and picker) instead of raw `--test-tags`.
- **Rejected:** Separate on/off flags; raw `--test-tags`.
- **Source:** Unit 100.
- **Status:** active

### 2026-07-17 · `promote --dirty` copies files (last write wins) instead of `git apply`
- **Why:** `git apply`, even `--3way`, validates against the destination index: a new module fails "does not exist in index" and a dirty destination fails "does not match index". The destination is dirty by design (it accumulates), so there is no clean-destination guard, only a WARNING listing the destination files that had uncommitted changes. The branch lives in `[promote] branch` (global by default, project wins). `promote --deploy` and shared config across worktrees are out of v1.
- **Rejected:** `git apply`; a clean-destination guard; a hardcoded branch.
- **Source:** Unit 97 and the 2026-07-17 fix; `destDirtyPaths` in `internal/cmd/promote_git.go`.
- **Status:** active

### 2026-07-13 · `deploy` and `watch` push by default, honour `[push]`, and never open a picker
- **Why:** Closes the premise "code is synced by another tool" of Unit 69. `watch` runs its inner deploy with `--push` and does no push of its own, so nothing is pushed twice (Unit 125). `push --set-dest` is a separate config-only flag (no `push --all`/`deploy --push-all`, the owner did not ask for them). An explicit `--dest` is authoritative (a probe cannot learn a build dir); a picked path is persisted relative when it is under `remote_path`.
- **Rejected:** A manual push step; a module-picked folder to configure the destination.
- **Source:** Units 91, 94, 95.
- **Status:** active

### 2026-07-07 · `watch` polls the ref and ships committed content; prod needs an explicit `--force`
- **Why:** Polling is worktree-proof, cheap and dependency-free; the watcher's worktree may be on another branch, so it ships the content of the new SHA (`deploy --commits <shas> --force --push`, shipped from `git archive`, see [Each module resolves a source first; the target's transport then ships it (`mod@ref`, `--at`)](#2026-09-30--each-module-resolves-a-source-first-the-targets-transport-then-ships-it-modref---at)). After a restart a fresh log stream is opened and retried after one poll interval with `--tail 0`, because a restart kills the SSH stream anyway.
- **Rejected:** A filesystem watcher; shipping the working tree; a one-time startup confirmation for prod.
- **Source:** Units 84, 87.
- **Status:** active

### 2026-07-07 · `push` uses rsync over SSH with `--delete` opt-in; the destination never mirrors the local subpath
- **Why:** Mirroring deletions automatically is destructive; mirroring the local subpath made the destination depend on the cwd. Container-internal sync (a docker cp chain over SSH) is out of scope.
- **Rejected:** Automatic `--delete`; mirroring the local subpath.
- **Source:** Unit 83.
- **Status:** active

### 2026-07-05 · Headless `deploy` needs explicit selection flags (`--auto`, `--modules`); a non-TTY run does not pick
- **Why:** Fail closed: a pipe must never silently choose commits to ship.
- **Rejected:** Auto-picking when there is no TTY.
- **Source:** Unit 78.
- **Status:** active

### 2026-06-29 · Manual "deployed" marks persist on confirm, not on each toggle
- **Why:** A toggle mid-picker stays reversible; build mode never persists marks.
- **Rejected:** Writing on every toggle.
- **Source:** Unit 74.
- **Status:** active

### 2026-06-23 · Dirty modules appear in the same picker as commits
- **Why:** One gesture. The Unit 69 premise that deploy does not push is gone ([`deploy` and `watch` push by default, honour `[push]`, and never open a picker](#2026-07-13--deploy-and-watch-push-by-default-honour-push-and-never-open-a-picker)).
- **Rejected:** A separate flag or step for dirty modules.
- **Source:** Unit 69.
- **Status:** active

### 2026-06-19 · Deploy history is local and per target; it records only resolved commits after a full success
- **Why:** "Shipped" is an operator fact and the commit list and picker are local; a commit shipped to staging is not shipped to prod; unresolved or failed work never reached the server. The server-side [The deploy lock lives on the target under `.echo/lock.json` and ignores itself](#2026-09-30--the-deploy-lock-lives-on-the-target-under-echolockjson-and-ignores-itself) answers a different question (what code runs) and does not replace it.
- **Rejected:** Storing history on the server; one global history.
- **Source:** Unit 65.
- **Status:** active

### 2026-06-12 · `deploy` detects translation files with one global flag, not per module
- **Why:** Deployed modules are first-party and their `.po` files are the source of truth (same rationale as `update --i18n`); `-l` is not an alternative because it never scopes `-u`.
- **Rejected:** Per-module handling.
- **Source:** Unit 64.
- **Status:** active

### 2026-06-12 · Unresolved commits are excluded and reported, not an abort
- **Why:** One unknown SHA should not block shipping the rest.
- **Rejected:** Aborting the deploy.
- **Source:** Unit 61.
- **Status:** active

## Deploy safety

### 2026-09-30 · The partial-deploy dependency check is a regex warning that asks on staging/prod, not a parser that refuses
- **Why:** The 2026-09-30 incident was a method dropped by a shipped module and still called by one left on the server. A regex over the server's old tree and the new one, plus a grep of the sibling modules, catches that shape with no Python on either end; a word match can be a comment, so a finding warns on `dev` and asks (fails closed without a TTY, `--force` passes) on staging, prod and undeclared stages instead of refusing. `--no-dep-check` is per run and logged, never a config key, for the same reason as `--no-lint`. A check that cannot run warns and lets the deploy go on: it must not become a new way for deploys to fail.
- **Rejected:** A Python AST or registry-backed analysis (needs an interpreter or a running Odoo per target); blocking outright on findings (false positives would teach `--no-dep-check`); a config opt-out; counting a moved xml id as kept (its qualified name changes, so callers still break).
- **Source:** Unit 128; `internal/depcheck/depcheck.go`, `internal/cmd/deploy_depcheck.go`.
- **Status:** active

### 2026-09-30 · Every failure after the first code write restores the code, with or without a DB checkpoint
- **Why:** A rollback that restored only the database left rsync targets running new code over the old schema. The decision order stays `rollbackDecision`; scope widens: DB and code with a checkpoint, code only without one (the confirm and log line say the DB is not restored). A rollback the user declines is kept as a `code` checkpoint entry so `deploy --rollback` can apply it later. `--no-rollback-on-fail` takes no snapshot. `post_deploy` failures never roll back.
- **Rejected:** Restoring code only on git-deploy targets, and only the deploy branch (the old behaviour).
- **Source:** Unit 126; `handleDeployFailure`, `rollbackDecision` in `internal/cmd/deploy.go`.
- **Status:** active

### 2026-09-30 · The code snapshot is a server-side tar of exactly the directories the run writes, taken before the first write
- **Why:** It restores whatever was there whatever its origin (a `worktree` lock entry is content that only existed on someone's disk, and a module never shipped by Echo has no entry) and needs nothing from the local checkout; the lock is saved with it. A module being installed is recorded as absent so the restore deletes it. If the snapshot cannot be created the deploy aborts before any write.
- **Rejected:** Reconstructing the previous code from the lock (re-shipping SHAs); snapshotting the whole addons tree.
- **Source:** Unit 126; `createCodeSnapshot` in `internal/cmd/deploy_codesnap.go`.
- **Status:** active

### 2026-09-30 · The lock is metadata: a read or write failure is a WARNING and never fails a deploy
- **Why:** A deploy that worked must not be reported as failed because a JSON file did not land (same rule as the Unit 113 provenance). `verified` is false between the push and a green `-u`.
- **Rejected:** Failing the deploy on a lock error.
- **Source:** Unit 124.
- **Status:** active

### 2026-09-30 · The deploy lock lives on the target under `.echo/lock.json` and ignores itself
- **Why:** The lock describes the target, so anyone who reaches it (another machine, agent or person over SSH) must read it; `remote_path` is the one directory every target owns, while a push destination can be shared between targets. JSON because it is machine-written and read over SSH. `.echo/.gitignore` containing `*` keeps it out of any repo that contains it without touching the repo (no `.gitignore` edit, no `.git/info/exclude`), so `push --clean` never mistakes it for overlay. A repo that already tracks `.echo/` gets a WARNING with the fix instead of Echo modifying the repo. The local deploy history is not migrated into it.
- **Rejected:** Local config; a file inside the push destination (destinations can be shared between targets); content hashes; migrating the local history.
- **Source:** Unit 124; `internal/cmd/deploy_lock.go`.
- **Status:** active

### 2026-09-30 · Declined: a drift check before the build, and with it content hashes in the lock
- **Why:** Owner's reason: it solved one project's configuration problem rather than improving Echo generally. Committed content already has a free identity (the git tree id of the module at its commit), so hashes would only have served the check.
- **Rejected:** Comparing the destination against the lock before a build; per-module content hashes.
- **Source:** Units 124-126 plan, 2026-09-30; spec 124 "Why this shape".
- **Status:** active

### 2026-07-23 · `deploy --rollback` preserves the DB checkpoint; `--consume-checkpoint` opts into the rename
- **Why:** The default drops the broken DB and copies back with `CREATE DATABASE ... TEMPLATE` (a file copy on PG 15+), so the checkpoint stays intact and hidden at the price of about one extra copy of disk. The automatic on-failure rollback still consumes. Owner's decision, scoped to the explicit `--rollback`; the closing log line says `disposition=preserved|consumed`.
- **Rejected:** Consuming the checkpoint on every rollback.
- **Source:** 2026-07-23 session note; `internal/cmd/deploy.go` (`--consume-checkpoint`).
- **Status:** active

### 2026-07-17 · The on-failure choice is an explicit `--rollback-on-fail` / `--no-rollback-on-fail` pair, with no config default
- **Why:** An agent running Echo in a pty looks like a TTY and hung on the prompt; order is explicit flag, then `--force` (roll back), then TTY asks, then headless rolls back. `watch` always reverts: an unattended monitor must never leave a broken database.
- **Rejected:** A persisted `[deploy] rollback_on_fail`; threading the flag through `watch`.
- **Source:** Unit 101; `rollbackDecision`.
- **Status:** active

### 2026-07-17 · The rollback flags are inert when no checkpoint exists
- **Why:** With no checkpoint there was nothing to roll back, so the flags did nothing.
- **Rejected:** Nothing recorded.
- **Source:** Unit 101.
- **Status:** superseded by [Every failure after the first code write restores the code, with or without a DB checkpoint](#2026-09-30--every-failure-after-the-first-code-write-restores-the-code-with-or-without-a-db-checkpoint)

### 2026-07-13 · Checkpoint policy is resolved server-first, field by field, with a local fallback
- **Why:** The policy protects the server's DB and the stage already came from the server (fixed a split-brain in Unit 89); field-by-field merging lets the server set one field and local fill the rest, in contrast to actions (wholesale). `--set-checkpoint` is project-only by construction; a machine-wide default stays a hand edit of `global.toml` (Unit 104).
- **Rejected:** A client-only policy; a machine-wide `--set-checkpoint`.
- **Source:** Units 90, 104.
- **Status:** active

### 2026-07-13 · Checkpoints are `db` (TEMPLATE copy) or `dump`; on by default for staging and prod, off for dev; failure to create one aborts
- **Why:** The checkpoint wraps only the `-i`/`-u` step and fails closed (nothing is touched if it cannot be taken). Metadata is local, objects are remote. DB checkpoints are hidden from Odoo with `ALLOW_CONNECTIONS false`, automatic and with no per-instance config; only the app container is stopped when a checkpoint is in play because the db container is needed. A filestore snapshot was deferred, not rejected, and has no unit.
- **Rejected:** Stopping every container (the db container is needed).
- **Source:** Units 89, 90; `ALLOW_CONNECTIONS` and `remoteStopApp` in `internal/cmd/`.
- **Status:** active

### 2026-07-13 · A DB rollback renames the checkpoint over the live database, consuming it
- **Why:** Near-instant (`ALTER DATABASE ... RENAME TO`) and needs no extra disk.
- **Rejected:** Nothing recorded.
- **Source:** Unit 89.
- **Status:** superseded by [`deploy --rollback` preserves the DB checkpoint; `--consume-checkpoint` opts into the rename](#2026-07-23--deploy---rollback-preserves-the-db-checkpoint---consume-checkpoint-opts-into-the-rename)

### 2026-07-13 · Deploy actions resolve wholesale in fixed phases, fail-fast; `post_deploy` never rolls back a verified deploy
- **Why:** Merged lists give an unpredictable order across machines, so the server's list (or the local one) is used whole. A `post_deploy` failure comes after a green verify, so rolling back would undo a good deploy. The interactive authoring wizard is a form per field (technical constraint), with a remote picker for `where=remote` and a local one for `local`.
- **Rejected:** Merging lists; a single form for all fields; rolling back on `post_deploy` failure.
- **Source:** Units 92, 93; per-target authoring [`deploy actions` are scoped by `--from`/`--remote`; the wholesale upload prompt is gone](#2026-07-23--deploy-actions-are-scoped-by---from--remote-the-wholesale-upload-prompt-is-gone).
- **Status:** active

## Modules and Odoo

### 2026-09-03 · Addons paths are discovered as a fallback and never persisted
- **Why:** The first command in a fresh repo must work without a setup step, as Odoo itself does; explicit configuration still wins.
- **Rejected:** Requiring configuration first; persisting discovered paths.
- **Source:** Unit 115.
- **Status:** active

### 2026-07-29 · `lint` uses xmllint as the authority with an embedded Go validator as the floor
- **Why:** A Go-only validator gave false negatives ("lint clean, deploy aborted"). The grammar is embedded (one RNG per major version) so it works offline and never degrades on a machine that never downloaded Odoo. A `[lint] grammar_source = "instance"` refinement was noted, not built.
- **Rejected:** Go as the authority; requiring a local Odoo tree; fetching the grammar from the instance.
- **Source:** Units 109-111; `internal/odoolint`.
- **Status:** active

### 2026-07-29 · Manifest-aware severity is part of `lint`, not a later unit
- **Why:** Default-on pre-flight in `deploy` is only safe if the checker can tell a hard error from a warning for files the manifest does not load.
- **Rejected:** Deferring it (the draft left it out of scope).
- **Source:** Units 109, 110.
- **Status:** active

### 2026-07-29 · The deploy lint pre-flight is default-on; the only opt-out is `--no-lint` per run
- **Why:** A per-project opt-out gets set once and then the check stays off forever on the machine that most needs it; `--no-lint` is visible as a WARNING in the transcript. If false positives appear, fix the rule.
- **Rejected:** A `[lint] preflight = false` config key.
- **Source:** Unit 110; `internal/cmd/deploy_lint.go`.
- **Status:** active

### 2026-07-17 · `i18n-pull --to-worktree` redirects the destination instead of an inverse `promote` flag
- **Why:** Keeps promote strictly about moving code to the deploy branch.
- **Rejected:** A `promote` flag that pulls translations.
- **Source:** Unit 99.
- **Status:** active

### 2026-07-08 · `compare` uses go-difflib and checksums
- **Why:** go-difflib is already in the tree and avoids requiring `diff` on the remote; checksums need one exec or SSH round trip per side instead of one per file.
- **Rejected:** Shelling out to `diff`; `cat` per file.
- **Source:** Units 80, 86.
- **Status:** active

### 2026-07-03 · `i18n-pull` guesses module versus language by shape, with an explicit `--lang` escape hatch
- **Why:** Keeps `i18n-pull sale es_MX` working; a round-trip test ties the parser (Unit 76) to the build-mode composer (Unit 77).
- **Rejected:** Requiring `--lang` always.
- **Source:** Units 76, 77.
- **Status:** active

### 2026-06-11 · Odoo 19 i18n credentials travel in an ephemeral conf inside the container; the version is configured, not probed
- **Why:** Keeps Echo's "pass the connection deliberately" model (one explicit place per call site) and avoids an `odoo --version` round trip. The conf includes the real `addons_path`, since `-c` replaces the real conf.
- **Rejected:** `-e PGHOST/PGUSER/...` libpq env on `compose exec`; probing the version at runtime.
- **Source:** Unit 53 and the i18n19 fix.
- **Status:** active

### 2026-06-10 · `update --i18n` overwrites translations for all active languages; per language is `i18n-update`
- **Why:** Odoo's `-l/--language` only scopes `--i18n-export/--i18n-import`, never `-u`. The flag is not persisted in `update --last`, so a bare repeat never silently overwrites.
- **Rejected:** A per-language option on `update`.
- **Source:** Unit 49.
- **Status:** active

### 2026-06-10 · `i18n-pull` lists the project's remote modules from its `addons_path`; the DB list sits behind `--installed`
- **Why:** The `ir_module_module` query returned every stock Odoo module (owner feedback). Destination is the host directory if present, else relative to the cwd.
- **Rejected:** Using the DB or the local module resolution as the list source.
- **Source:** Unit 50.
- **Status:** active

### 2026-06-10 · `modstate` is installed-only by default in both modes, `--all` is the single widening flag, and JSON success is silent
- **Why:** Keeps the JSON contract independent of the output flag and free of start or finalize lines.
- **Rejected:** A separate `--installed` flag.
- **Source:** Unit 47.
- **Status:** active

### 2026-06-09 · `view` renders with `bat`, then an internal printer; there is no `cat` tier
- **Why:** `cat` renders worse than the internal printer and bypasses Echo's output capture.
- **Rejected:** `cat` fallback.
- **Source:** Unit 43.
- **Status:** active

### 2026-06-09 · `modinfo` reads the manifest version with a regex and prints one Odoo-style line, never a table
- **Why:** No Python parser dependency; one-shot eligible so CI and recipes can assert "in sync". The migration summary of Unit 44 is likewise printed at the very end and mirrored in `report`.
- **Rejected:** A Python parser; a table.
- **Source:** Units 42, 44.
- **Status:** active

### 2026-06-09 · `update --last` is persisted on disk, repeats `--all` too, and only asks for confirmation on an empty picker
- **Why:** Scoped to `update` only; all four choices were closed in a form (tracker 2026-06-09).
- **Rejected:** Confirming every repeat; session-only memory; recall for install and uninstall.
- **Source:** Unit 35.
- **Status:** active

### 2026-06-09 · The module start line is emitted at resolution time and names the resolved modules
- **Why:** The picker's alternate screen would wipe a line printed before it.
- **Rejected:** An early generic line plus the resolved set at the end.
- **Source:** Unit 38.
- **Status:** active

### 2026-06-08 · When the host scan finds no modules, addons are read from the container's `odoo.conf`, live; enterprise paths are excluded by default
- **Why:** Trigger is an automatic fallback (not a command); the container is the source of truth; paths and mode are persisted so read-only `modules` knows the mode; `modules --config` still pins host mode. A future opt-in may allow updating Enterprise modules.
- **Rejected:** A separate command; a host-only config.
- **Source:** Unit 26 (form, 2026-06-08).
- **Status:** active

### 2026-05-25 · No version branching for `install`/`update`/`test` flags
- **Why:** Official docs confirm the same flag set in 17, 18 and 19 (`--test-enable`, `--test-tags`, `-i`/`-u`, `--stop-after-init`). i18n export and import do branch at major 19.
- **Rejected:** Per-version flag tables.
- **Source:** Unit 11; tracker Resolved Questions; Unit 53.
- **Status:** active

### 2026-05-13 · `test` does not force `-u`, has no prod gate, and exposes both `--no-http` and `--http-port`
- **Why:** A forced update on every run would slow the test loop; the missing gate matches `update`.
- **Rejected:** Forcing `-u` each run.
- **Source:** Unit 11.
- **Status:** active

### 2026-05-13 · i18n commands write through a temp file plus `docker cp`; one module per invocation; default language `es_MX`
- **Why:** Independent of the project's mount layout; `i18n-update` is an import with `--i18n-overwrite`. Closed via a mockup form.
- **Rejected:** Writing into a bind mount.
- **Source:** Unit 12.
- **Status:** active

### 2026-05-12 · A run is judged by log severity as well as exit code
- **Why:** Odoo logs ERROR and still exits 0, so exit code alone reports false success. Connection flags to the container are explicit (`buildConn`), not inherited from the image env, so shell, test and i18n can bypass the image entrypoint.
- **Rejected:** Exit code only; relying on `HOST/USER/PASSWORD` in the image env.
- **Source:** Units 05, 07, 10.
- **Status:** active

## Database

### 2026-09-11 · `db-admin` confirms by risk, not by stage
- **Why:** Always on `prod` (a reset removes access from whoever had the real password) and whenever the credential is known (`--insecure`) on any stage; `--force` skips.
- **Rejected:** A stage-only guard; guarding the active DB (an earlier unit kept it unguarded because it is the normal target).
- **Source:** Unit 116.
- **Status:** active

### 2026-09-11 · `db-admin` generates a password, stores it hashed, and prints it once; `--insecure` keeps `admin/admin`
- **Why:** A plaintext password stays readable in the DB until first login even when strong, so it is stored as passlib pbkdf2_sha512. The login stays `admin` (generating it too would be a second thing to copy). Default changed, old behaviour kept behind the flag.
- **Rejected:** Plaintext storage; keeping `admin/admin` as the default; generating the login.
- **Source:** Unit 116.
- **Status:** active

### 2026-09-11 · `--save` writes the credential to 1Password only on request, updating the item instead of duplicating it
- **Why:** Writing a vault is a persistent effect and must not happen just because `op` exists. One item titled `<project> (<db>)` (what you type to search) tagged with the server and the union of tags; the full item JSON is patched so manual notes survive, and a lookup error other than "isn't an item" propagates so a network failure never creates a duplicate. Local and remote use the same machinery.
- **Rejected:** Always creating a new item (three homonyms for three resets); auto-save; a title prefixed `Odoo`.
- **Source:** Unit 117.
- **Status:** active

### 2026-09-09 · `db exec` transport is decided by the profile marker, with a generic `docker exec -i` fallback and no INFO line
- **Why:** `internal/cmd` has no ambient logger; wiring one cost more than the clarity gained. A deliberate deviation from the Reverb plan.
- **Rejected:** Wiring a logger down to emit an INFO on fallback.
- **Source:** Unit 119.
- **Status:** active

### 2026-07-16 · `db-pull` downloads by default; restoring locally is opt-in (`--restore`)
- **Why:** A linked repo such as `all_odoo` is only the module source: it has no local compose, so a forced restore targeted a nonexistent stack. `db-pull` is projectless. Owner decision.
- **Rejected:** Always restoring into the local stack.
- **Source:** Unit 98 (supersedes the restore-first behaviour of Unit 85).
- **Status:** active

### 2026-07-08 · `db-pull` neutralizes by default only when the source is prod, and never switches the active DB
- **Why:** Staging is usually already neutered; switching silently would surprise more than help, so a `db-use` hint is printed. The remote is only read, so there is no remote prod gate.
- **Rejected:** Always neutralizing.
- **Source:** Unit 85, narrowed by Unit 98.
- **Status:** active

### 2026-06-22 · `db-restore --as` is a pre-filled prompt; progress has two layers
- **Why:** Enter accepts a derived name so users need not know it up front; INFO milestones mark phases while DEBUG shows the raw tool stream.
- **Rejected:** Requiring the name up front; a single layer.
- **Source:** Units 67, 68.
- **Status:** active

### 2026-06-09 · `db-neutralize` exists as a command and as `db-restore --neutralize`; the red guard fires only for the active DB or prod
- **Why:** Closed in a form (2026-06-09).
- **Rejected:** Blocking on active connections (copied from `db-drop`).
- **Source:** Unit 30.
- **Status:** active

### 2026-06-08 · The filestore is read and written inside the Odoo container with `docker cp`; `--force` terminates connections
- **Why:** Fixes both restore and backup without assuming the host or bind-mount layout (`filestore_path` configurable). `--force` on `db-drop` and `db-restore` terminates connections instead of keeping the guard, per the owner. `db-restore` accepts both native Odoo and Echo flavours transparently, with no new flag.
- **Rejected:** A host path (native-Odoo assumption); keeping the guard under `--force`; a restore-flavour flag.
- **Source:** Units 22, 23, 25.
- **Status:** active

### 2026-05-13 · Destructive DB commands abort rather than auto-stopping Odoo; shells use one DB with no bash-to-sh fallback
- **Why:** The user keeps explicit control. A single DB (`cfg.DBName`, switched via `init`) is enough for v1; failing visibly shows which image was used.
- **Rejected:** Auto-stopping Odoo; multi-DB shells; silent `sh` fallback.
- **Source:** Units 09, 10.
- **Status:** active

## Scripting

### 2026-07-13 · `logview --json` reads the watch-deploy record from a local file under a distinct `watch-deploy` verb
- **Why:** A local file read over SSH is cheaper than probing, and a separate verb keeps watch records distinguishable.
- **Rejected:** An SSH probe; reusing `deploy`.
- **Source:** Unit 96.
- **Status:** active

### 2026-07-09 · `update --build` and `i18n-pull --build` use dedicated builders; `connect` stays flags-only
- **Why:** Generic `RunBuild` flags cannot govern a picker; the `i18n-pull` builder is remote-aware and bakes `--from=<target>`, with no `--all`/`--installed` (they would ignore the chosen module); `connect` needs a remote login at run time. Build mode is `--build`/`-b`, ends with Run/Copy/Cancel and prompts for flag values.
- **Rejected:** A generic builder for every command; offering `--all` in the `i18n-pull` builder.
- **Source:** Units 51, 88.
- **Status:** active

### 2026-07-08 · `logview` uses a minimum-level threshold and `report` lines are shared through `config.ReportLine`
- **Why:** The filename sorts chronologically (millis prefix), so no index file is needed; a store per project, `0` disables each prune pass.
- **Rejected:** Exact-level match; an index file.
- **Source:** Units 81, 82.
- **Status:** active

### 2026-06-26 · `sequence` has its own run loop and a single Tab tri-state per item
- **Why:** The owner decided via form and mockup: order is selection order (badge), no reorder screen, `logs` forced last, `--from/--remote` applies to the whole sequence with one target, and `--last` (not `-r`, which clashes with `--remote`). A dedicated loop controls wording (`sequence complete` before the follow). A flag-less remote sequence does not offer the target picker (explicit `--remote`/`--from` needed), a deviation from the spec body.
- **Rejected:** Reusing `runRecipeSteps`; a reorder screen; `-r`.
- **Source:** Unit 73.
- **Status:** active

### 2026-06-12 · Piped `shell` is detected from a non-TTY; no auto-copy for it but `shell-run` keeps it
- **Why:** Auto-detection avoids a flag; the user invoked the script runner explicitly. `shell-run` is a new command so the interactive PTY `shell` stays intact.
- **Rejected:** A pipe flag; overloading `shell`.
- **Source:** Units 59, 63.
- **Status:** active

### 2026-06-10 · `run --last` orders newest-first by birthtime with a ModTime fallback
- **Why:** The owner's choice after clarification.
- **Rejected:** Nothing recorded.
- **Source:** Unit 52.
- **Status:** active

### 2026-06-09 · Recipes are fail-fast, with an opt-in `--continue-on-error`; their log is opt-in under the config dir
- **Why:** Teardown recipes are best-effort and the rest should stop at the first failure. Echo does not manage where recipes live, except the `--pick` selector (named `--pick`, not `--file`; searches only the current directory). `--log` defaults to the config dir, never the project.
- **Rejected:** Always-on logging; writing into the project; best-effort by default.
- **Source:** Units 32, 34, 39.
- **Status:** active

### 2026-06-09 · `report` persists every run, filters with `--level` (exact) plus `--min-level` (threshold), and `--silent` still captures
- **Why:** `report` must work without remembering a flag on `run`; suppression is about live noise, not data loss. Binary and per-level silencing are both supported. The recap is Odoo-style log lines (status, warnings, errors, duration, totals), not a table.
- **Rejected:** Opt-in persistence; one level semantics; a table recap.
- **Source:** Units 37, 40, 41.
- **Status:** active

## Process

### 2026-09-30 · The project context is a ctx store (repo storage), replacing the six legacy files and the single progress tracker
- **Why:** Handoffs had piled up: a 246 KB tracker with stale sections and specs for units already shipped. The store keeps STATE, decisions, knowledge and work small and true.
- **Rejected:** Keeping the legacy files and tracker as the working context.
- **Source:** `context/ctx.toml`; legacy files kept under `archive/2026-09-30-original/` as provenance.
- **Status:** active

### 2026-09-30 · Unit 14 (`theme`, `logo`, `version`, `stage` meta-commands) is closed without implementation
- **Why:** Owner decision. Those commands do not exist and the planet/python/anchor logos were never built; the open question about a default logo was answered by Unit 15.
- **Rejected:** Implementing the unit.
- **Source:** Unit 14; owner, 2026-09-30.
- **Status:** active

### 2026-09-30 · E5 (`link --reverb`), E6 (`refresh --remote`) and E8 (delegated `update` verdict from the job status) become open Echo units
- **Why:** They only existed in the Reverb plan document. E8 is a bug: a delegated update that succeeded was closed with `update failed errors=9 warnings=83` because the stream scanner counted `ERROR` lines, whereas the job status must be the verdict. Unit 122 (remove `-E`), a dependency check for partial deploys, and the i18n follow-ups (live-stream i18n logs; a debug flag printing the generated `odoo.conf`) stay open with them.
- **Rejected:** Leaving them in the Reverb plan only.
- **Source:** Unit 123 live acceptance (iza/staging, 2026-09-10); owner, 2026-09-30.
- **Status:** active

### 2026-05-18 · Every version bump promotes `[Unreleased]` to `[X.Y.Z]` in the same commit; every meaningful change appends to `[Unreleased]`
- **Why:** The owner codified this after the commit tool's auto-changelog kept inserting a patch section per commit, which broke the intended meaning of a version; `CHANGELOG.md` is maintained by hand.
- **Rejected:** Auto-generated changelog entries; bump and promotion in separate commits.
- **Source:** `CLAUDE.md` "Versioning & Changelog"; `internal/repl/repl.go` `Version`.
- **Status:** active

### 2026-05-07 · Feature code requires an approved spec for the unit
- **Why:** Specs are written before code; Unit 111 was specced first after it had been proposed as a plan row without a spec. Implementation may deviate from a spec; the code is the authority.
- **Rejected:** Coding first and documenting after.
- **Source:** `CLAUDE.md` workflow rules; Unit 111 note.
- **Status:** active
