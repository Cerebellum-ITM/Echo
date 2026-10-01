# Operations
> How Echo is released and versioned, how units are specced and closed, how live verification is done, and the single live-verification ledger (one row per item). Last verified: 2026-09-30.

## Release and versioning

- SemVer. The version is the package var `Version` in `internal/repl/repl.go:30` (a `var (...)` block, not a `Version: "..."` literal); `FullVersion()` appends `VersionMeta`, injected by the Makefile ldflags as `+<shortsha>` always and `.dirty` when the tree has changes (`0.25.0+abc1234.dirty`). A bare `go build` has no metadata.
- `CHANGELOG.md` follows Keep a Changelog. Two rules:
  1. Every meaningful change appends to `[Unreleased]` (`Added`, `Changed`, `Fixed`, `Removed`, `Deprecated`, `Security`) **in the same commit** as the code.
  2. A version bump promotes `[Unreleased]` to `[X.Y.Z]` **in the same commit** that edits `Version`. Never split the bump from the promotion.
- Units accumulate on one working branch between releases; a release goes to `main` through a PR with a merge commit, then the release commit (bump + promotion), tag and a GitHub release with binaries. `make build_release` writes `bin/echo_cli_darwin_arm64`, `_linux_amd64`, `_linux_arm64`; `make build` installs `~/.local/bin/echo_cli` (the binary is always `echo_cli`, never `echo`). The `release-flow` skill drives the sequence and never publishes without an explicit confirmation.
- Latest tag: v0.25.0 (`main` = `eb1b49c`). Units 118-126 and the later fixes are unreleased as of 2026-09-30.
- **Stale sentence to fix at release time**: `[Unreleased] Deprecated` says `-E` "se retira cuando la unidad 30 de Reverb esté desplegada". Reverb unit 30 has been deployed and verified since 2026-09-09, so removal (Unit 122) is unblocked; reword the entry. Removing `-E` is a breaking change: it needs a `Removed` entry and an explicit version decision.

## How units are specced and closed

- Work is organised in numbered **units** (last numbered unit: 126). Numbers 14 and 27 were never delivered/assigned, and number 66 was used twice (db-admin, db-use); do not treat these as errors. Some work has no number: remote `ps`, the `uninstall`-via-`odoo shell` fix, `link --add`, the `echo-link-mode` items E5/E6/E8 (planned in the Reverb repo, `~/Documents/Projects/dev_tools/reverb/docs/echo-link-mode-echo-units.md`; Echo units 118/119/120/121/123 = E1/E2/E3/E4a/E7).
- A unit is specced before it is built (`/ctx spec [topic] [NN-]name` writes `work/<topic>/NN-name.md`), implemented exactly as specified, and closed with `/ctx close`, which marks it done and moves the spec to `archive/` once its durable facts are in knowledge or decisions. Open units live in `work/`; the current pick-up list is STATE.
- Archived specs carry "Verify when done" checkboxes that were mostly never ticked even though the unit shipped. They are not open work; open verification lives only in the ledger below.
- The Reverb repo's own tracker (`~/Documents/Projects/dev_tools/reverb/context/progress-tracker.md`) holds Reverb-side verification; Echo's ledger cites it where relevant.

## Verification practice

- Done gate and test seams: [code-standards](code-standards.md#done-gate). Unit tests and the fake-`ssh` end-to-end harness prove logic; they do not prove behaviour against real Docker, Postgres, Odoo or SSH. Most remote units were delivered verified only against seams because the dev sandbox had no docker or SSH.
- TTY behaviour is checked by driving the real binary in tmux (tui-probe skill); wizards of Units 88 and 93 were verified that way locally.
- Real targets used so far (names as registered in `global.toml` or as projects): `habitta_dev` and `habitta_prod` (linked from the `all_odoo` repo), `muutrade` -> `develop` (the linked test environment for deploy work), `morwi/Acumedic`, `morwi/fuentebuena`, and the Reverb environment `iza/staging`.
- Mutating commands against a real server (deploy, rollback, push, db writes, anything on `prod`) need the user's explicit go each time. Prefer the read-only form first (`deploy --dry-run`, `link --show`, `--list`).
- Headless output consumed by the `odoo-probe` skill (`--json`, exit codes, `link --show`, `promote --show-branch`, `logview --json`) is a contract; changing it needs a wording pass in that skill (repo `~/Documents/Projects/odoo-probe`).

## Live-verification ledger

How to read it: `pending` = no record of a real-environment check exists in the tracker, specs or sibling repos as of 2026-09-30. It does not mean broken; real use since (the 2026-09-30 deploy work exercised push, deploy and watch heavily) may have covered some rows, but nothing recorded it. When a row is verified, set `verified`, add the date and the target, and keep the source. `partial` = part of the row has a source. `verified` rows are listed so they are not redone.

| Unit | What to verify | Status |
|---|---|---|
| 9-12 | Db, shell, `test` and i18n basics against real Odoo/Postgres/docker (old "not tested" lines) | unknown; likely covered by daily use, never recorded |
| 11, 75 | `test` locally and `test --from <target>` (muutrade -> develop) against a real Odoo; the `--http-port=8189` isolation on Odoo 19 Enterprise | pending |
| 15 | Banner on a wide TTY (soundwave/shadow, stage colour) | pending (user visual check) |
| 16 | `copy-last` through tmux and the OSC 52 path; checklist is the git-ignored local `.unverified/untested.html` (2026-05-18, may be stale) | pending |
| 18 | `connect` (CDP login) | verified (tracker, user) |
| 21 | Live command/flag highlight | verified (tracker, user) |
| 22 | `db-restore` of a real native Odoo zip end to end | pending |
| 23 | `db-drop`/`db-restore --force` terminating live connections | pending |
| 24 | Flag highlight and Tab flag completion in a real TTY | pending |
| 25 | Filestore restore/backup inside the container (`docker cp`) | pending |
| 26 | Addons discovery from the container `odoo.conf` | partial: the conf-mode bug of Unit 50 was found and fixed on `habitta_prod`; no explicit pass |
| 28 | `connect` session cache (probe, 5-day TTL, `--fresh`) | pending |
| 29 | `connect` window modes against real Chrome (new tab, `--new-window`) | pending |
| 30 | `db-neutralize` and `db-restore --neutralize`: neutralized state in a real container | pending |
| 31, 33, 35 | Script mode, `--level`, `update --last` against a real Odoo project | pending |
| 36 | Loose `Warn:`/`Error:` reformat with a real wkhtmltopdf report | pending |
| 38, 40 | Module start line and `report` recap from a real update (real warnings/errors) | pending |
| 39, 51, 74 | Recipe picker, build-mode pickers (Run/Copy/Cancel) and manual deploy marks in a TTY | pending |
| 42, 47 | `modinfo` verdicts and `modstate --json \| jq` on a real project | pending |
| 43 | `view` with `bat` present and absent, host and conf mode | pending |
| 44 | Migration detection on an update that really migrates a module | pending |
| 45 | Shell/logs ANSI restyle | verified (user, on the server) |
| 46 | Shell banner restyle | pending (visual) |
| 49 | `update --i18n` really overwrites saved terms | pending |
| 50, 76, 77 | `i18n-pull` single, multi-module and build mode against a remote | partial: conf-mode bug found/fixed on `habitta_prod`; no explicit pass |
| 53 + i18n fixes | i18n export/update/pull on a real Odoo 19 (project modules loaded, `.po` complete) | partial: follow-up fixes imply real use; no explicit pass |
| 55, 56, 57 | Picker bar, `ps` table, `db-list`/`modules` listing in a TTY | pending (visual) |
| 58 | `logs` painted like `update` against a running container | pending |
| 59 | `shell-run` on real docker | pending |
| 60, 62, 72 (logs), 83 | `link`, `shell-run`, `logs -t`, `push --remote` against a real target | partial: exercised on `iza/staging` (Reverb) 2026-09-09; classic hand-built targets not recorded |
| 61, 64, 65, 69, 70 | Deploy core (picker, i18n detection, history marks, dirty modules, `update --installed` picker) on a real remote | pending |
| 63 | Shell pipe against a real remote instance | pending |
| 71 | Long db-name truncation in the TTY | pending (visual) |
| 72 | Remote `restart` and classic remote `logs` on a real host | pending |
| 73 | `sequence` in a TTY with container and remote | pending |
| 78 | `deploy --auto` and `--json` against a real remote | pending |
| 79 | Remote `view` (host mode and conf mode) | pending |
| 80, 86 | `compare` and `compare --all` against a real container and remote | pending |
| 81, 82 | Command-log history and `logview` in a TTY | pending |
| 84 | `watch` with a real remote and real commits | pending |
| 85, 98 | `db-pull --from habitta_prod` end to end (real dump, `--restore` with a local stack) | pending (left to the user); only the project gate is recorded passing live |
| 87 | `watch` log follow: resume without duplicates, retry on a killed container, no interleaving | pending |
| 88 | `update --build` remote branch | partial: local path verified with tui-probe; remote path pending |
| 89 | Checkpoint create/rollback on a real server | partial: a user run on `habitta_prod` found the "db stopped" bug (fixed); the rest pending |
| `deploy --rollback` | Default preserves the checkpoint, `--consume-checkpoint` consumes (muutrade/develop); re-check after Unit 126 changed rollback | pending |
| 90 | Checkpoint policy resolved server-first, local fallback | pending |
| 91, 94, 95 | `push` path with a real build context, `push --set-dest`, deploy push default against a remote | pending |
| 92, 93 | Deploy actions: run, remote picker and upload | pending |
| 96 | `odoo-probe` skill wording for consuming `logview --json` / `watch-deploy` (documentation pass, not a runtime check) | pending |
| 99 | `i18n-pull --to-worktree` end to end against a remote | pending |
| 100 | `deploy --test` with a real suite on a remote | pending |
| 102 | Git deploy on a real git-deploy server (muutrade -> develop, with dirty overlay) | pending |
| 103 | `promote --show-branch` headless | verified (live, headless) |
| `promote --dirty` | Copy-files mode on the exact scenario that failed with `git apply` | verified (live, 2026-07-17) |
| 105 | `deploy --from habitta_dev` runs only the dev action (per-target actions) | pending |
| 106 | `link --next` and the already-linked no-op against a real binding | pending; `link --list --json` and usage exit 2 verified (`all_odoo`) |
| 107, 108 | Real `-E` acceptance run (36/36) | moot: retired with `-E` (Unit 122) |
| 110 | A lint block leaves no drift (`push --clean` shows nothing) and no checkpoint; `watch` inherits the pre-flight | pending |
| 112 | `deploy --set-code` and `--with-local`, remote half, on a real git-deploy server | pending (local reset verified end to end in temp repos) |
| 113 | `deploy --set-git-branch --rename`, remote provenance write/clear, `--restore-code`, `link --show` report | pending |
| 115 | Module discovery from the repo root, path arguments, `link --list` from a subfolder | verified (`morwi/Acumedic`) |
| 116 | `db-admin --remote` with a generated password | verified (`morwi/fuentebuena`); local path (login with the printed password, hash in `res_users.password`, no re-hash at login) and the prod-from-dev confirm pending |
| 117 | `db-admin --save` create path | verified (live) |
| 117 | Update path (second `--save`: updates, keeps history and manual note), item with empty/local `web.base.url`, password not visible in `ps`, decline-after-reset prints password plus warning | pending |
| 118 | Register a target over a real `global.toml` that has `[reverb]`; the section survives | pending |
| 119 | `checkpoint create --remote`, `db-pull --remote`, disk pre-flight and module queries against an env with profile (`docker exec -i` mode and the `no such service` fallback) | pending |
| 120 | Marker behaviours with the new code on an env with profile: checkpoint -> snapshots, up/stop/restart via API, overlay shadow warning, `push --clean`, `link --show` `reverb env … api=on` / `api=off` | pending; the 2026-09-09/10 acceptance used a binary and a hand-pasted snippet that did not exercise it (Reverb tracker) |
| 123 | Delegated `update <mods> --remote` on a Reverb env | verified on `iza/staging`, 2026-09-10 (Reverb tracker); the verdict bug E8 is open work, not a verification item |
| 124 | Deploy lock written/verified and `push --clean`/`--restore-code` writing it, on a real server | pending |
| 125 | `deploy --modules ccima_flow_mail@99f2109 --from habitta_prod --dry-run` first; the real deploy needs the user's authorization | pending |
| 126 | Code snapshot, automatic code+DB restore on failure and the declined-rollback `code` checkpoint on a real server | pending (never run on a real server) |
