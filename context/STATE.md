# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-10-19.

## Goal

Make Echo safe and cheap to point at any remote Odoo, and drivable by agents from the binary alone: link mode as
the one way to reach a target, a `deploy` whose shipped code is known and undone on failure, hosts that need
nothing of Echo installed, complete per-command help, and live data, run outcomes and errors returned as JSON
instead of scraped from logs.

## Where we are

- **v0.27.0 is the last release** (2026-10-07, `3f62780`). `main` is at `418ac94`, 14 commits ahead of
  `origin/main`, not pushed. Everything unreleased is in `CHANGELOG.md` `[Unreleased]`.
- **Branch `feature/agent-inspection-tooling`** (from `418ac94`, not pushed, not merged) carries the agent
  tooling of 2026-10-19, one commit per unit/fix:
  - Unit 143 `probe` (`a47c55e`): read-only live-data command in a Postgres `READ ONLY` transaction.
  - Unit 145 headless hygiene: no auto-copy without a TTY (`1be8a7b`), missing `commandFlags` (`9091a31`),
    `modules --config` TTY guard and `--addons-path` (`498b87a`).
  - Unit 146 `link --show --json` (`26c366a`).
  - Unit 144 module commands: validation and zero-test failure (`0219002`), `--json` outcome (`ee60610`).
  - Unit 147 `logs --errors [--json]` (`fb56ac6`).
  - `help agents` rows for the new JSON reads (`357159b`).
  - Live-check fix: `probe --remote` named its target (`0ce2552`).
- `go build`, `go vet`, `go test ./...` green; touched files gofmt-clean.
- **Live-verified read-only on `develop`** (muutrade, Odoo 19, 2026-10-19, user's go): every `probe` form, the
  refused write, `link --show --json`, `logs --errors --json`. Not run live: Odoo 17/18, module `--json`
  (`test`/`update` were excluded on purpose), the headless clipboard. Ledger rows 143-147.
- The installed `~/.local/bin/echo_cli` is still `0.27.0+40779ce`: it has none of the branch.

## Blockers

| Blocker | Detail |
|---|---|
| none | Live checks, merge, push and release wait on the user. |

## Next

1. Remaining live checks ([ledger](knowledge/operations.md#live-verification-ledger) rows 143-147): `probe` on an
   Odoo 17 and an 18 target, and a failing `test --json` / `update --json` on a dev stack (needs the user's go:
   it runs Odoo on the target).
2. Merge `feature/agent-inspection-tooling` into `main` (`branch-flow`, `[MERGE]`), then release 0.28.0 (minor):
   promote `[Unreleased]` and bump `Version` in one commit, push, reinstall `echo_cli`.
3. After the install, apply both odoo-probe requests (below).
4. Unit 142 `op-lock-respect` needs its spec; then the 2026-10-19 audit proposals in the
   [plan](work/build/plan.md#proposed-by-the-2026-10-19-agent-audit-not-specced) (remote `modstate`/`modinfo`
   first).
5. Older live checks (140, 139, 137 picker, 132) and sideload follow-ups 134-136; 138 needs a decision.

## Messages

| File | From → to | Status |
|---|---|---|
| [REQUEST-odoo-probe-command-help](messages/REQUEST-odoo-probe-command-help.md) | Echo → odoo-probe skill | open; apply after the release with Unit 140 |
| [REQUEST-odoo-probe-agent-inspection](messages/REQUEST-odoo-probe-agent-inspection.md) | Echo → odoo-probe skill | open; apply after the release with Units 143-147, together with the one above |

## Open questions for the user

- Unit 138 (ship sideloaded Python compiled): pick an option from [138](work/build/138-sideload-obfuscation.md)
  or close it.

## Pending, not blocking

- Small fixes, one commit each ([plan](work/build/plan.md#small-fixes-found-during-the-2026-09-30-store-adoption)),
  now including `down --from` (not a real remote form).
- Specs 143, 144, 146 and 147 stay in `work/build/` until their live check; then archive them.
- Merged branches kept locally: `feature/command-help-db-push`; three old worktrees in `.claude/worktrees/` and
  about twenty merged `feat/*` branches remain; untouched.
- `ctx_audit.py` flags `messages/README.md` as a stale message; it is the folder's index, a false positive.
- `gofmt -l` flags pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
