# Echo

> Interactive CLI for Odoo development environments — Docker, modules, and databases through one short prompt.

Echo is a single-binary REPL for Odoo projects. Drop into a project directory,
run `echo`, and you get a styled prompt that wraps `docker compose`,
`pg_dump`/`pg_restore`, and the `odoo` CLI behind short memorable commands.
Output streams in real time, colored by log level, and every long-running
command ends with a clear ✓/✗ result line.

```
 ❯ echo
 ┌─ ECHO ────────────────────────────────────────────────────────────┐
 │ Welcome, pascual                       Theme: tokyo · Logo: echo  │
 │ Project: my-shop · Odoo 18 · dev                                  │
 └───────────────────────────────────────────────────────────────────┘

  echo my-shop-a1b2 [dev/18.0]:~$ up
  $ up
  ✓ Container db-1   Started
  ✓ Container odoo-1 Started

  ✓ up completed
```

## Status

Echo is a work in progress; below is what currently ships in `main`.

| Area     | Working                                                       | Pending                              |
|----------|---------------------------------------------------------------|--------------------------------------|
| Project  | `init`, `reset`                                               | `version`, `stage`, `theme`, `logo`  |
| Docker   | `up`, `down`, `restart`, `ps`, `logs` (with `--copy`/`--all`) | —                                    |
| Modules  | `install`, `update`, `uninstall`, `modules` (`--config`)      | `test`                               |
| Database | `db-backup` (`--with-filestore`), `db-restore`, `db-drop`, `db-list` | —                             |
| Shell    | —                                                             | `shell`, `bash`, `psql`              |
| i18n     | —                                                             | `i18n-export`, `i18n-update`         |
| REPL UX  | ↑↓ history, fzf-style picker, level-colored logs, ✓/✗ result | Tab autocomplete, full ASCII banners |
| Themes   | charm, hacker, odoo, tokyo                                    | —                                    |

The full build plan lives in [`context/specs/00-build-plan.md`](context/specs/00-build-plan.md);
per-unit specs sit alongside it.

## Install

Requires Go 1.25+.

```sh
go install github.com/pascualchavez/echo@latest
```

The binary is installed as `echo` in `$GOBIN`. Make sure that directory is on
your `PATH`.

To build from source instead:

```sh
git clone https://github.com/pascualchavez/echo.git
cd echo
go build -o echo .
```

## Quick start

From the root of an Odoo project (one with a `docker-compose.yml`):

```sh
echo
```

On first run, Echo can't find a project config and asks you to run `init`:

```
  echo my-shop-a1b2 [dev/18.0]:~$ init
```

`init` is an interactive form (Charm `huh`) that auto-detects:

- The compose flavor (`docker compose` vs `docker-compose`)
- Running containers (lists Odoo + db candidates via `compose ps`)
- Databases inside the db container (via `psql -lqt`)
- POSTGRES credentials from `.env`

It walks you through picking the Odoo version, Odoo and DB containers, DB name,
and stage (`dev`/`staging`/`prod`). The result is saved to
`~/.config/echo/projects/<sha256-of-path>.toml`. Echo never writes anything
into your project repo.

Once `init` is done the prompt updates to reflect the chosen stage and version,
and every command is wired to the right containers.

## Command reference

### Project

| Command  | Description                                                        |
|----------|--------------------------------------------------------------------|
| `init`   | Interactive setup (Odoo version, containers, DB, stage)            |
| `reset`  | Wipe Echo config — global, per-project, or both                    |
| `help`   | Print the in-REPL command list, grouped by area                    |
| `clear`  | Clear screen and reprint the header                                |
| `exit` / `quit` / `Ctrl+D` | Quit Echo                                        |

### Docker

| Command            | Description                                          |
|--------------------|------------------------------------------------------|
| `up [service]`     | `docker compose up -d`                               |
| `down [service]`   | `docker compose down`                                |
| `restart [service]`| `docker compose restart`                             |
| `ps`               | Show compose container status                        |
| `logs [service]`   | Follow Odoo logs (or `[service]`); `Ctrl+C` exits    |
| `  -t N`           | Tail the last `N` lines (default `100`)              |
| `  --no-follow`    | Disable follow mode, bounded output                  |
| `  -c, --copy`     | Bounded output **and** copy to system clipboard      |
| `  --all`          | All compose services instead of just Odoo            |

### Modules

| Command                  | Description                                          |
|--------------------------|------------------------------------------------------|
| `install <mod>...`       | Install modules in the active DB                     |
| `  --with-demo`          | Include demo data                                    |
| `update <mod>...`        | Update modules                                       |
| `  --all`                | Update every installed module                        |
| `uninstall <mod>...`     | Uninstall modules                                    |
| `modules`                | List modules from configured addons paths            |
| `  --config`             | Interactive form to pick which folders are addons paths |

When `install`/`update`/`uninstall` are called without module names, Echo
opens a fzf-style fuzzy picker scoped to the project's configured addons
paths.

### Database

| Command                       | Description                                                       |
|-------------------------------|-------------------------------------------------------------------|
| `db-backup [name]`            | `pg_dump -Fc` into `./backups/<db>_<ts>.dump`                     |
| `  --with-filestore`          | Package dump + host filestore into a `.zip` (Odoo-compatible)     |
| `db-restore [--as N] [--force]`| Pick a backup, create the target DB, restore                     |
| `db-drop [name] [--force]`    | Drop a database; red `huh.Confirm` prompt unless `--force`        |
| `db-list`                     | Table of DBs with size and creation date; `●` marks the active one|

All destructive commands run a connection guard against `pg_stat_activity`
and abort with a clear message if Odoo (or anything else) is still attached
to the target DB.

On the first successful backup, Echo appends `backups/` to your `.gitignore`
when one exists at the project root.

## Output features

- **Level-colored streams.** Odoo log lines (`DEBUG`/`INFO`/`WARNING`/`ERROR`/`CRITICAL`) are recolored using the active theme; Python tracebacks inherit the color of the line that triggered them.
- **Action result line.** Every long-running command (`install`, `update`, `uninstall`, `up`, `down`, `restart`, `db-backup`, `db-restore`, `db-drop`) finishes with `✓ <name> completed` or `✗ <name> failed: …`. Silent failures — exit 0 with `ERROR`/`CRITICAL` log lines — render as `✗ <name> finished with N error(s)`.
- **Fuzzy picker.** Filter is always active; type to narrow, `Tab` to toggle, `Enter` to confirm. Single-select variants are used for restore and drop.
- **History.** ↑/↓ navigation across sessions, persisted to `~/.config/echo/history` (cap 1000 entries, consecutive duplicates collapsed).

## Themes

Four palettes ship in the binary: `charm`, `hacker`, `odoo`, `tokyo`. The
theme is stored in `~/.config/echo/global.toml` so it's shared across all
projects. Stage modifies the prompt accent: `dev` (green), `staging`
(yellow), `prod` (red).

## Configuration

```
~/.config/echo/
├── global.toml       # theme, logo, compose flavor
├── history           # REPL command history
└── projects/
    └── <sha256>.toml # one file per project path
```

Per-project files are keyed by the SHA-256 of the project root so two projects
with the same folder name never collide. `reset` lets you wipe global,
per-project, or both.

## Project layout

```
.
├── main.go                  # entry point
├── internal/
│   ├── banner/              # header + ASCII logo rendering
│   ├── clipboard/           # cross-platform clipboard (logs --copy)
│   ├── cmd/                 # command implementations (init, docker, modules, db, picker, …)
│   ├── config/              # ~/.config/echo/ TOML layout
│   ├── docker/              # compose + psql + pg_dump wrappers
│   ├── env/                 # .env parser
│   ├── odoo/                # odoo CLI invocation builders
│   ├── project/             # walk-up to find project root
│   ├── repl/                # interactive prompt, dispatch, line styling
│   └── theme/               # palettes + styles
└── context/                 # six-file methodology docs + per-unit specs
```

## Development workflow

This project is built using the
[Six-File Context Methodology](context/) (spec-driven dev). Each feature gets
a spec in [`context/specs/`](context/specs/) before it's implemented; the
build plan in [`context/specs/00-build-plan.md`](context/specs/00-build-plan.md)
tracks the ordered units. The methodology docs are also useful as a tour of
the project for new contributors.

Commits follow a fixed format generated by
[`commitcraft`](https://github.com/pascualchavez/commitcraft):

```
[TAG] scope: short title

Body explaining the change.
```

Common tags: `ADD`, `FIX`, `IMP`, `REF`, `DOC`, `REM`, `REL`.

## Roadmap

Pending units, in plan order:

- **Unit 10** — `shell`, `bash`, `psql` direct shells into the containers.
- **Unit 11** — `test <mod>...` with version-specific Odoo CLI flags.
- **Unit 12** — `i18n-export` / `i18n-update` for `.po` files.
- **Unit 13** — Tab autocomplete from the command registry.
- **Unit 14** — meta commands (`theme`, `logo`, `version`, `stage`).
- **Unit 15** — All four ASCII logos with per-segment token coloring.

## License

TBD.
