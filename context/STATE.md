# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-10-20.

## Goal

Make Echo safe and cheap to point at any remote Odoo, and drivable by agents from the binary alone: link mode as
the one way to reach a target, a `deploy` whose shipped code is known and undone on failure, hosts that need
nothing of Echo installed, complete per-command help, and live data, run outcomes and errors returned as JSON
instead of scraped from logs.

## Where we are

- **v0.29.0 is released** (2026-10-20): `main` = `b00a8be`, pushed, tag `v0.29.0`, GitHub release with the three
  binaries (https://github.com/Cerebellum-ITM/Echo/releases/tag/v0.29.0), from `main` without a PR. It carries
  Unit 148 (`modstate`/`modinfo` remote and JSON), Unit 142 (operation lock in deploy, watch, sideload and
  checkpoint) and the `ValidationError`/`UserError` message fix of `test --json`. v0.28.0 (same day) carried
  help pages, `db-push`, `sideload`, `init --remote`, `db-restore` progress and Units 143-147.
- `~/.local/bin/echo_cli` is `0.29.0+b00a8be`. `CHANGELOG.md` has no `[Unreleased]` section; add one with the
  first change.
- Unit 142 verified on `develop` 2026-10-20 (refusal, stale takeover, release after Ctrl+C); `watch` and Reverb
  unchecked. Unit 148 verified on `develop`. The fix was found on a real `test sale` run on `develop` (231 tests,
  69 failed, 24 errors).
- Live checks done 2026-10-19/20 (user's go): `probe` fully on Odoo 17 (`form-dev`), 18 (`habitta_dev`) and 19
  (`develop`), writes refused on all three; `link --show --json` and `logs --errors --json` on `develop`;
  `test --json` parsed real runs (67 and 17 tests) and the zero-test path. Two bugs found live and fixed before
  the release: `probe --remote` named no target (`0ce2552`), module JSON lacked the target and the run error
  (`73402b0`). Ledger rows 143-147.
- The `odoo-probe` skill now teaches `probe`, the JSON reads and `help <cmd>` (`2e9d26a` in its repo, branch
  `docs/echo-console-cards`, not pushed; no remote). Both requests are archived in
  `archive/2026-10-20-odoo-probe-requests/`.

## Blockers

| Blocker | Detail |
|---|---|
| none | |

## Next

1. echo-console: recognise `echo_cli -C <dir> <cmd>` as a run. Its `parseEchoCommand`
   (`~/Documents/Projects/echo-console/hooks/echo/command.ts`) rejects a command that does not start with a
   letter, so `-C` runs get no card; the odoo-probe skill keeps `cd <project> &&` until it does.
2. Remaining live checks ([ledger](knowledge/operations.md#live-verification-ledger)): a raising
   `update --json`, `link --show --json` on a Reverb target and an undeclared stage, local `logs --errors`, the
   headless clipboard (145), `deployed_version` from a lock with module versions (148).
3. The rest of the 2026-10-19 audit proposals in the
   [plan](work/build/plan.md#proposed-by-the-2026-10-19-agent-audit-not-specced) (headless `view`, `logview --last
   --json`, `ps`/`db-list --json` next).
4. Older live checks (140, 139, 137 picker, 132) and sideload follow-ups 134-136; 138 needs a decision.

## Messages

None open.

## Open questions for the user

- Unit 138 (ship sideloaded Python compiled): pick an option from [138](work/build/138-sideload-obfuscation.md)
  or close it.

## Pending, not blocking

- Small fixes, one commit each ([plan](work/build/plan.md#small-fixes-found-during-the-2026-09-30-store-adoption)),
  including `down --from` (not a real remote form).
- Specs 142, 144, 146, 147 and 148 stay in `work/build/` until their remaining live checks; then archive them.
- Merged branches kept locally: `feature/command-help-db-push`, `feature/agent-inspection-tooling`,
  `feature/op-lock-remote-modules`; three old
  worktrees in `.claude/worktrees/` and about twenty merged `feat/*` branches remain; untouched.
- `ctx_audit.py` flags `messages/README.md` as a stale message; it is the folder's index, a false positive.
- `gofmt -l` flags pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
