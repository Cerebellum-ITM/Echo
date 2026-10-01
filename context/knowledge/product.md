# Product
> What Echo is, who uses it, the link-mode workflow, scope and the command families (one line each, owners linked). Last verified: 2026-09-30.

## What Echo is

- `echo_cli` is a single-binary Go CLI that operates **Odoo 17, 18 and 19** environments, locally (a project with `docker-compose.yml`) and on **remote targets over SSH** (a server running the same compose layout, including Reverb-managed environments). Source of the version range: `config.Defaults` (default 18), `odoo.Major` builders, Unit 53 (Odoo 19 i18n).
- Four ways to run it: interactive REPL styled like Claude Code's REPL (prompt, picker, history, Tab completion); one-shot `echo_cli <cmd>`; recipes `echo_cli run <file>` (one command per line); `echo_cli connect …` (direct, projectless). Global flags `--version`/`-v`, `--help`/`-h`, `-C <dir|alias>` work from anywhere. Mechanics: [architecture-and-traps](architecture-and-traps.md#startup-and-root-resolution-maingo), [scripting](scripting.md).
- Users: an Odoo developer working daily against local stacks and several remote stacks, and **AI agents** that drive Echo headless through the `odoo-probe` skill (stable `--json` outputs, exit codes 0/1/2/3, no prompts without a TTY). Headless forms exist because the skill needed them: `link --show`, `link --list --json`, `promote --show-branch`, `logview --json`, `deploy --json`, `modstate --json`. Consumer-facing wording lives in the skill repo (`~/Documents/Projects/odoo-probe`), not here.

## Product principles (still valid)

- **Odoo cohesion**: output looks like Odoo's own log (logger names, `key=value`, level colours); generic CLI decorations are rejected. Owner: [ui](ui.md).
- **The REPL stays version-agnostic**: Odoo version differences are absorbed in the command/builder layer ([architecture-and-traps](architecture-and-traps.md#packages-and-boundaries)).
- **Stage is first-class**: `dev` (green), `staging` (yellow), `prod` (red) drive the prompt, banner, picker bar and the confirmation gates. Gate semantics: [architecture-and-traps](architecture-and-traps.md#prod-gate-rule).
- **No Echo files in the user's repo**: state lives under `~/.config/echo/`; the server side keeps only the lock and code snapshots under the target's `remote_path`.
- **Safe by default on remote**: destructive or shipping commands gate on the target's stage, checkpoint the DB when the stage warrants it, and restore on failure ([deploy-safety](deploy-safety.md)).

## The link-mode workflow (the main remote workflow)

- A **repo can be only a module source** (no `docker-compose.yml`, e.g. a monorepo of addons). `link` binds that directory to a **connect target**: a named `[connect_targets.<name>]` entry in `global.toml` (SSH host alias from `~/.ssh/config` + the project's path on the server). The binding is the per-project `[connect]` table; `link` with no argument switches between targets; `link --add` registers a new target and binds it. Owner: [remote-targets](remote-targets.md).
- The **server's own Echo profile** (written by `echo_cli init` there, or by Reverb for Reverb environments) is the source of truth for container names, DB, stage, Odoo version, addons paths and compose flavor. The laptop never re-declares them.
- With a binding, remote verbs take `--remote` (the linked target) or `--from <target>`: `shell`, `shell-run`, `test`, `view`, `compare`, `logs`, `restart`, `up`, `stop`, `ps`, `update`, `i18n-pull`, `db-pull`, `db-admin`, `checkpoint`, `sequence`.
- Delivery loop: edit locally -> `deploy` (pick commits and dirty modules, ship code, update/install modules, verify) or `push` (rsync only) or `watch` (auto push + deploy on new commits of the deploy branch) -> inspect with `logs`/`logview`. A local `promote` funnels worktrees into the single deploy branch. Owners: [deploy](deploy.md), [deploy-safety](deploy-safety.md).
- Reverb environments are plain targets: Reverb writes the same profile a hand-built host would have, plus a marker table that unlocks snapshot checkpoints and API lifecycle verbs when a local token exists. The older `-E <project>/<env>` flag is deprecated; its removal is Unit 122 (open). Owner: [remote-targets](remote-targets.md).

## Scope

- In: everything in the families below; `connect` (open Chrome logged in as any user, no password, via a forged session and CDP); offline XML lint of Odoo modules (`lint`, also a deploy pre-flight); recipes and an interactive `sequence` builder; command-history and `report` over past runs.
- Out (by design or never built): a GUI/web UI; a plugin system; multi-DB shells (shells always use the project's configured DB); being an SSH manager (no port or user field: `ssh_host` is an alias resolved by `~/.ssh/config`); orchestrating Reverb itself; Odoo versions other than 17/18/19.
- **Not built, closed**: the in-REPL meta-commands `theme`, `logo`, `version`, `stage` (Unit 14, closed without implementation on 2026-09-30) and the planet/python/anchor logos. They do not exist: the theme is the `theme` key of `global.toml`; version and stage come from `init`/the project profile. `connect --serve` (Unit 114) was a proposal never implemented and is archived as discarded.
- Open product work: see STATE and the work items; do not infer it from this file.

## Command families

Authoritative list: `Registry` in `internal/repl/commands.go`; reference and flags: `README.md`.

- **Project and config**: `init`, `reset`, `alias`, `link`, `connect`. [remote-targets](remote-targets.md), [architecture-and-traps](architecture-and-traps.md#config-and-state-storage).
- **Docker lifecycle**: `up`, `down`, `stop`, `restart`, `ps`, `logs` (`up`, `stop`, `restart`, `ps`, `logs` also run on a remote target). [remote-targets](remote-targets.md), [ui](ui.md).
- **Modules**: `install`, `update`, `uninstall`, `test`, `modules`, `modinfo`, `modstate`, `view`, `compare`, `lint`. [modules-and-odoo](modules-and-odoo.md).
- **i18n**: `i18n-export`, `i18n-update`, `i18n-pull`. [modules-and-odoo](modules-and-odoo.md).
- **Database**: `db-admin`, `db-backup`, `db-restore`, `db-pull`, `db-drop`, `db-neutralize`, `db-list`, `db-use`. [database](database.md).
- **Shells**: `shell`, `shell-run`, `bash`, `psql`. [scripting](scripting.md), [remote-targets](remote-targets.md).
- **Remote delivery**: `push`, `deploy`, `watch`, `checkpoint`, `actions`, `promote`. [deploy](deploy.md), [deploy-safety](deploy-safety.md).
- **Scripting and history**: `sequence`, `run` (one-shot only, not in `Registry`), `report`, `logview`, `copy-last`, and the universal `<cmd> --build`/`-b`. [scripting](scripting.md).
- **REPL**: `help`, `clear`, `exit`, `quit`. [ui](ui.md).
