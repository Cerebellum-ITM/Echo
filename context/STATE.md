# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-10-19.

## Goal

Make Echo safe and cheap to point at any remote Odoo, and drivable by agents from the binary alone: link mode as
the one way to reach a target, a `deploy` whose shipped code is known and undone on failure, hosts that need
nothing of Echo installed (`init --remote`), private modules from a local library (`sideload`), complete
per-command help, and remote database replacement (`db-push`) without manual SSH.

## Where we are

- **v0.27.0 is the last release** (2026-10-07, `3f62780`). `main` is at `2765830`, **13 commits ahead of
  `origin/main`, not pushed and not released**. Unreleased on `main`, all in `CHANGELOG.md` `[Unreleased]`:
  - Unit 132 `db-restore` progress and heartbeat (`35810b7`).
  - Unit 139 `init --remote` / `init --from` (`7fced0f`, merged `561907d`).
  - Units 133 and 137 `sideload` and its library (merged `891fe9d`); lock label of library sideloads
    (merged `8643a1d`).
  - Unit 140 per-command help: `help <cmd>`, `<cmd> --help`, `help agents`, search, `--json` (`cd7312d`).
  - Unit 141 `db-push` plus the operation lock, and `db-pull` reading the server's `filestore_path`
    (`40779ce`). Both merged as `2765830` (branch `feature/command-help-db-push`, kept locally).
- `go build`, `go vet`, `go test ./...` green at `2765830`.
- The installed `~/.local/bin/echo_cli` is `0.27.0+40779ce` (2026-10-19): it has everything above.
- `db-push` was confirmed working on a real target by another session (per the user, 2026-10-19); the dry run
  against `habitta_dev` was checked here. Rows 140 and 141 of the
  [ledger](knowledge/operations.md#live-verification-ledger).
- fuentebuena-dev: its `etc/odoo.conf` carried an `addons_path` entry from another project
  (`/mnt/extra-addons/oehealth_modules_19`), which made every Odoo run warn; removed on 2026-10-19 (backup
  `etc/odoo.conf.bak-20261019` on the server), Odoo restarted clean.

## Blockers

| Blocker | Detail |
|---|---|
| none | Push and release wait only on the user. |

## Next

1. Release 0.28.0 (minor: new commands): promote `[Unreleased]` and bump `Version` in `internal/repl/repl.go` in
   one commit ([operations](knowledge/operations.md#release-and-versioning)), push `main`, then reinstall
   `echo_cli`. Then apply the odoo-probe request below.
2. Unit 142 `op-lock-respect` needs its spec (a proposal card for it was left in the 2026-10-19 session).
3. Live checks still open: the fresh-agent help check (140); the `init --remote` form, prod-downgrade and link
   confirms in tmux; the `sideload` picker in a TTY; `db-restore` progress on a local stack (132).
4. Sideload follow-ups 134, 135, 136; 138 needs a decision. Then 122 (remove `-E`), E8, E5, E6
   ([plan](work/build/plan.md)).

## Messages

| File | From → to | Status |
|---|---|---|
| [REQUEST-odoo-probe-command-help](messages/REQUEST-odoo-probe-command-help.md) | Echo → odoo-probe skill | open; apply after the release with Unit 140 |

## Open questions for the user

- Unit 138 (ship sideloaded Python compiled): pick an option from [138](work/build/138-sideload-obfuscation.md)
  or close it.

## Pending, not blocking

- Small fixes, one commit each ([plan](work/build/plan.md#small-fixes-found-during-the-2026-09-30-store-adoption)),
  now including the defects found while writing the Unit 140 help (`modules --addons-path` unreachable, flags
  parsers accept but `commandFlags` lacks, unused `--remote` on `push`/`watch`/`db-pull`) and the two db-push
  logger names.
- Merged branches kept locally: `feature/command-help-db-push`; three old worktrees in `.claude/worktrees/` and
  about twenty merged `feat/*` branches remain; untouched.
- `ctx_audit.py` flags `messages/README.md` as a stale message; it is the folder's index, a false positive.
- `gofmt -l` flags 7 pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
