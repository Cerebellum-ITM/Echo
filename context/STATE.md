# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-11-18.

## Goal

Make Echo safe and cheap to point at any remote Odoo, and drivable by agents from the binary alone: link mode as
the one way to reach a target, a `deploy` whose shipped code is known and undone on failure, hosts that need
nothing of Echo installed, complete per-command help, and live data, run outcomes and errors returned as JSON
instead of scraped from logs.

## Where we are

- **v0.30.0 is released** (2026-11-09): `main` = `45e8f4b` = `origin/main`, tag `v0.30.0`, GitHub release with
  the three binaries (https://github.com/Cerebellum-ITM/Echo/releases/tag/v0.30.0), from `main` without a PR.
  It carries Unit 149 (Relay targets, remote `i18n-update`), Unit 150 (remote `uninstall`, merged `0ad6c4d` with
  the no-recreate fix), restarts that never recreate the db container plus `doctor`'s `dbimage` check
  ([deploy-safety](knowledge/deploy-safety.md#restarts-never-recreate-the-database)), `requirements.txt` in
  `deploy --push` (merged `963422d`; [deploy](knowledge/deploy.md#requirements-file)) and the filestore owner fix
  of `db-push --filestore` / `db-pull --restore` (`a570425`).
- `~/.local/bin/echo_cli` is `0.30.0+45e8f4b` (installed 2026-11-18 with `make build` on a clean `main`).
- Shipped without a live check: the filestore owner fix, remote `uninstall`, the no-recreate start on a re-pulled
  db image (ledger rows `db-push owner`, 150, `no-recreate start` in
  [operations](knowledge/operations.md#live-verification-ledger)). The requirements sync is verified on
  habitta_dev 2026-11-03; the habitta image recipe does not install the file yet.
- Relay targets `morwi-fb-odoo` / `morwi-fb-stable` are verified only through a local relayd (2026-10-21, ledger
  row 149); against Ionos they need relayd serving Relay unit 25 and the echo token in
  `~/.config/relay/echo-token`.

## Blockers

| Blocker | Detail |
|---|---|
| none | |

## Next

1. Live-check what 0.30.0 shipped unverified, on a dev target with the user's go: `db-push --filestore` to an
   instance without a filestore, `uninstall --from <t>` on a dev target, and a start after a db image re-pull.
2. After the owner upgrades relayd on Ionos and saves the echo token: `ps`, `logs` and a `shell-run` on
   `morwi-fb-odoo` and `morwi-fb-stable`; then live `test`/`update`/`i18n-pull`/`i18n-update`/`uninstall` on
   `fb/stage` when writes there are allowed.
3. Remaining live checks in the [ledger](knowledge/operations.md#live-verification-ledger): a raising
   `update --json`, `link --show --json` on an undeclared stage, local `logs --errors`, the headless clipboard
   (145), `deployed_version` from a lock with module versions (148).
4. The rest of the 2026-10-19 audit proposals in the
   [plan](work/build/plan.md#proposed-by-the-2026-10-19-agent-audit-not-specced) (headless `view`,
   `logview --last --json`, `ps`/`db-list --json` next).
5. Older live checks (140, 139, 137 picker, 132) and sideload follow-ups 134-136; 138 needs a decision.

## Messages

| File | From → to | Status |
|---|---|---|
| [messages/REQUEST-echo-relay-dev-envs.md](messages/REQUEST-echo-relay-dev-envs.md) | relay → Echo, `odoo-probe` | open: review Echo on Relay dev envs, fix, document the agent workflow |
| Relay's `context/messages/REQUEST-echo-propose-scripts.md` (`~/Documents/Projects/relay`) | relay → Echo, `odoo-probe` | open since 2026-11-03 |

## Open questions for the user

- Unit 138 (ship sideloaded Python compiled): pick an option from [138](work/build/138-sideload-obfuscation.md)
  or close it.
- `feat/sequence-builder` (8 commits, also on origin, 5 months old, on a stale base, bumps repl to 0.18.0): drop
  it or rebuild the `sequence` command on current `main`.

## Pending, not blocking

- Small fixes, one commit each ([plan](work/build/plan.md#small-fixes-found-during-the-2026-09-30-store-adoption)),
  including `down --from` (not a real remote form).
- Specs 142, 144, 146, 147, 148, 149 and 150 stay in `work/build/` until their remaining live checks; then archive
  them.
- Merged branches kept locally: `fix/no-recreate-db`, `feature/deploy-requirements` (checked out in the worktree
  `.claude/worktrees/magical-curran-0b83c4`), `fix/db-push-filestore-owner`, `dev-pcc/pensive-colden-0d8f57` and
  the earlier `feature/*` / `feat/*` ones; two more old worktrees in `.claude/worktrees/` (detached). Delete only
  on the user's go.
- The `[MERGE] fix/db-push-filestore-owner` title (`af3a3cf`) starts with a capital letter, unlike the other
  merges; it is on `origin/main`, so it stays.
- `ctx_audit.py` flags `messages/README.md` as a stale message; it is the folder's index, a false positive.
- `gofmt -l` flags pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
