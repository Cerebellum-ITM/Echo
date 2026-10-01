# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-09-30.

## Goal

Make Echo safe to point at any remote Odoo: link mode as the one way to reach a target (Reverb environments
included), and a `deploy` whose shipped code is known, reproducible from any ref and undone when a run fails.
The deploy half came out of the habitta_prod incident of 2026-09-30
([deploy-safety](knowledge/deploy-safety.md)).

## Where we are

- **v0.25.0 is the last release** (`main` = `eb1b49c`, tag `v0.25.0`). Branch `wip/ps-remote` (HEAD `fa3dad6`)
  is 12 commits ahead, all unreleased and listed in `CHANGELOG.md` `[Unreleased]`: config round-trip (118),
  db exec transport (119), Reverb from marker (120), `-E` deprecated + link-mode docs (121), `update` via Reverb
  (123), deploy lock (124, `a73d4db`), per-module source `mod@ref`/`--at` (125, `1d29ccb`), code rollback (126,
  `fa3dad6`), `link --add` (`c296284`), remote `ps`, `uninstall` through `odoo shell` (`b3086b0`), 1Password
  item title/tags.
- `go test ./...` is green at `fa3dad6`. Units 124-126 are covered end to end with a fake `ssh` on `PATH`
  ([code-standards](knowledge/code-standards.md)), but none has run against a real server yet.
- **This context store was adopted today** (`/ctx adopt`) and is **not committed yet**: the old six-file layout
  and the 124 specs are in [archive/2026-09-30-original/](archive/2026-09-30-original/); the repo's `CLAUDE.md`
  now imports `@context/README.md`.
- Every unit up to 126 is implemented except the open ones in [work/build/plan.md](work/build/plan.md). Unit 14
  (meta-commands) and Unit 114 (`connect --serve`) were closed without implementation ([decisions](decisions.md#process)).

## Blockers

| Blocker | Detail |
|---|---|
| none | Unit 122 used to wait for Reverb unit 30, which is live since 2026-09-09 ([remote-targets](knowledge/remote-targets.md)). |

## Next

1. Commit this store adoption (commitcraft, scope `context`), separate from code.
2. Live check of 124-126 against the real server, authorised by the user:
   `echo_cli deploy --modules ccima_flow_mail@99f2109 --from habitta_prod --dry-run`
   ([ledger](knowledge/operations.md#live-verification-ledger)).
3. Release 0.26.0: bump `Version` in `internal/repl/repl.go` and promote `[Unreleased]` in the same commit; reword
   the `[Unreleased] Deprecated` sentence that says `-E` waits for Reverb unit 30 ([operations](knowledge/operations.md#release-and-versioning)).
4. Unit 122 (remove `-E`): write its spec first, then E8, E5, E6 ([plan](work/build/plan.md)).

## Messages

| File | From → to | Status |
|---|---|---|
| — | — | none pending |

## Open questions for the user

- Release 0.26.0 now (before Unit 122), or accumulate more on `wip/ps-remote`?
- The auto-memory note "Build plan state at 0.24.0" is stale project state; delete it now that this store owns
  the plan, or keep it?

## Pending, not blocking

- Live verifications owed by most units: one table in the [ledger](knowledge/operations.md#live-verification-ledger).
- Small code defects found while adopting the store: [plan, small fixes](work/build/plan.md#small-fixes-found-during-the-2026-09-30-store-adoption).
- Technology facts collected during the adoption belong in the `odoo-dev` / `odoo-probe` skills, not here; the
  list was handed to the user on 2026-09-30.
- `gofmt -l` flags 8 pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
