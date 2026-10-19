# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-10-15.

## Goal

Make Echo safe and cheap to point at any remote Odoo: link mode as the one way to reach a target, a `deploy`
whose shipped code is known and undone on failure, and now hosts that need nothing of Echo installed
(`init --remote`) plus private modules shipped from a local library (`sideload`) without touching the client's
repository.

## Where we are

- **v0.27.0 is the last release** (2026-10-07, `3f62780`). `main` is at `8643a1d`, **9 commits ahead of
  `origin/main`, not pushed and not released**. Unreleased on `main`, all in `CHANGELOG.md` `[Unreleased]`:
  - Unit 132 `db-restore` progress and heartbeat (`35810b7`).
  - Unit 139 `init --remote` / `init --from` (`7fced0f`), merged as `561907d`.
  - Units 133 and 137 `sideload` and its library (`d4ecb4f`, `ae632b5`), merged as `891fe9d`; the branch was
    built in another session's worktree and had never been merged until it was rebased onto `main` today.
  - Lock label of library sideloads `sideload:<branch>@<sha>` (`6ac9fb7`), merged as `8643a1d`.
- `go build`, `go vet`, `go test ./...` green at `8643a1d`. Feature branches of today and the sideload worktree
  were deleted after the merges.
- **Live on morwi-fb (2026-10-15)**, a host with no `echo_cli` and no `~/.config/echo` before today:
  `init --remote` wrote both profiles; connect targets `morwi-fb-stable` (`~/projects/fb_stable`, db
  `fuentebuena_stable`) and `morwi-fb-odoo` (`~/projects/fb_odoo`, db `fuentebuena`), both stage `staging`,
  Odoo 19; `~/Documents/Projects/morwi/fuentebuena` is linked to `morwi-fb-odoo`; `doctor` ok=6 on both.
  `forced_config_parameter` (library `odoo-forced-config-parameter@19.0`, commit `b8e4393`) is sideloaded on
  `morwi-fb-stable`, so that project now has an Echo-owned `docker-compose.override.yml`. Rows in the
  [ledger](knowledge/operations.md#live-verification-ledger).
- The installed `~/.local/bin/echo_cli` predates all of the above: it has no `init --remote` and no `sideload`.
  Today's runs used a binary built from `main`.

## Blockers

| Blocker | Detail |
|---|---|
| none | Push, release and the remaining live checks wait only on the user. |

## Next

1. Release 0.28.0 (minor: new commands): promote `[Unreleased]` and bump `Version` in `internal/repl/repl.go` in
   one commit ([operations](knowledge/operations.md#release-and-versioning)), push `main`, then reinstall
   `echo_cli`.
2. Live checks still open: the `init --remote` form, prod-downgrade and link confirms in tmux; the `sideload`
   picker in a TTY; `db-restore` progress on a local stack (Unit 132).
3. Sideload follow-ups 134 (checkpoint and code snapshot), 135 (`--rm`), 136 (local mode); 138 needs a decision.
   Then 122 (remove `-E`), E8, E5, E6 ([plan](work/build/plan.md)).

## Messages

| File | From → to | Status |
|---|---|---|
| [REQUEST-odoo-probe-command-help](messages/REQUEST-odoo-probe-command-help.md) | Echo → odoo-probe skill | open; apply after the release with Unit 140 |

## Open questions for the user

- Unit 138 (ship sideloaded Python compiled): pick an option from [138](work/build/138-sideload-obfuscation.md)
  or close it.

## Pending, not blocking

- Small fixes from the 2026-09-30 adoption, one commit each
  ([plan](work/build/plan.md#small-fixes-found-during-the-2026-09-30-store-adoption)).
- Three old worktrees in `.claude/worktrees/` (`confident-elion-650faa`, `magical-curran-0b83c4`,
  `recursing-franklin-ab22db`, all detached) and about twenty merged `feat/*` branches remain; untouched.
- `ctx_audit.py` flags `messages/README.md` as a stale message; it is the folder's index, a false positive.
- `gofmt -l` flags 8 pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
