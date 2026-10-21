# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-10-21.

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
- `~/.local/bin/echo_cli` is the build of Unit 149's commit (installed 2026-10-21, see below); the last release is
  0.29.0.
- **Unit 149 (Relay targets) is committed on `main`, unreleased.** `morwi-fb-odoo` and `morwi-fb-stable` in
  `global.toml` are now Relay targets (`relay = "fb/stage"` / `"fb/stable"`, `ssh_host = "morwi-fb"`), and
  `[relay]` points at `https://relay.cerebellum.technology` with `token_file = "~/.config/relay/echo-token"`.
  Verified on morwi-fb through a local relayd ([ledger](knowledge/operations.md#live-verification-ledger) row 149).
  They work against production only once relayd on Ionos serves Relay unit 25 and the owner saves an echo token
  in that file; until then every command on them fails naming the missing token file.
- **Unreleased on `main`** (`af3a3cf`, merge of `fix/db-push-filestore-owner`; `main` is 5 commits ahead of
  `origin/main`, not pushed): a restored filestore (`db-push --filestore`, `db-pull --restore`) takes the owner of
  Odoo's `data_dir` instead of the filestore root's, which a new instance does not have (`a570425`; reported on
  `habitta_prod` 2026-10-20 and fixed there by hand). Not checked on a real instance yet. The merge title starts
  with a capital letter, unlike the earlier merges; reword it before pushing if wanted.
- Unit 142 verified on `develop` 2026-10-20 (refusal, stale takeover, release after Ctrl+C); `watch` and Reverb
  unchecked. Unit 148 verified on `develop`. The fix was found on a real `test sale` run on `develop` (231 tests,
  69 failed, 24 errors).
- Live checks done 2026-10-19/20 (user's go): `probe` fully on Odoo 17 (`form-dev`), 18 (`habitta_dev`) and 19
  (`develop`), writes refused on all three; `link --show --json` and `logs --errors --json` on `develop`;
  `test --json` parsed real runs (67 and 17 tests) and the zero-test path. Two bugs found live and fixed before
  the release: `probe --remote` named no target (`0ce2552`), module JSON lacked the target and the run error
  (`73402b0`). Ledger rows 143-147.
- echo-console draws cards for `echo_cli -C` runs (`e9e4963` in its repo) and none for help pages (`aa1ccb8`) and the `odoo-probe` skill accepts `-C`
  (`15093bc`); both are loaded at session start.
- The `odoo-probe` skill now teaches `probe`, the JSON reads and `help <cmd>` (`2e9d26a` in its repo, branch
  `docs/echo-console-cards`, not pushed; no remote). Both requests are archived in
  `archive/2026-10-20-odoo-probe-requests/`.

## Blockers

| Blocker | Detail |
|---|---|
| none | |

## Next

1. After the owner upgrades relayd on Ionos and saves the echo token: `ps`, `logs` and a `shell-run` on
   `morwi-fb-odoo` and `morwi-fb-stable` against Ionos; then live `test`/`update`/`i18n-pull`/`i18n-update` on
   `fb/stage` when the user allows writes there.
2. Live-check the filestore owner fix on an instance without a filestore (ledger row `db-push owner`), then
   release; with Unit 149 on `main` the next release is 0.30.0 (minor: a new target kind).
3. Remaining live checks ([ledger](knowledge/operations.md#live-verification-ledger)): a raising
   `update --json` (its error path is the parser verified live by `logs --errors` and `test`), `link --show --json`
   on an undeclared stage, local `logs --errors` (needs Docker running), the
   headless clipboard (145), `deployed_version` from a lock with module versions (148).
4. The rest of the 2026-10-19 audit proposals in the
   [plan](work/build/plan.md#proposed-by-the-2026-10-19-agent-audit-not-specced) (headless `view`, `logview --last
   --json`, `ps`/`db-list --json` next).
5. Older live checks (140, 139, 137 picker, 132) and sideload follow-ups 134-136; 138 needs a decision.

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
  `feature/op-lock-remote-modules`, `fix/db-push-filestore-owner`; three old
  worktrees in `.claude/worktrees/` and about twenty merged `feat/*` branches remain; untouched.
- `ctx_audit.py` flags `messages/README.md` as a stale message; it is the folder's index, a false positive.
- `gofmt -l` flags pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
