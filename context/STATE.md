# State
> Where the work stands, what blocks it, what is next. Rewritten whole at every `/ctx close`; no history here. Last rewritten: 2026-10-01.

## Goal

Make Echo safe to point at any remote Odoo: link mode as the one way to reach a target (Reverb environments
included), and a `deploy` whose shipped code is known, reproducible from any ref, checked against what stays on
the server and undone when a run fails. The deploy half came out of the habitta_prod incident of 2026-09-30
([deploy-safety](knowledge/deploy-safety.md)).

## Where we are

- **v0.26.0 is the last release** (2026-10-01): PR [#24](https://github.com/Cerebellum-ITM/Echo/pull/24) merged
  as `d3f6541` (`[MERGE] feat/reverb-link-deploy-safety`), release commit `72fc85a`, tag `v0.26.0`, GitHub
  release with the three binaries. `main` and the working branch `feat/reverb-link-deploy-safety` are both at
  `72fc85a`; nothing is unreleased. The branch was `wip/ps-remote` until the release: the merge was rewritten
  and force-pushed under the new name the same day ([decisions](decisions.md#process)), so PR #24 still shows
  the old head name. The checkout was last left on `main`; switch back to the working branch before new work.
- **Units 127-131 are implemented**, one commit each, specs archived under `archive/2026-09-30-<unit>/`:
  127 config load safety (`a43813c`), 128 partial-deploy dependency check (`1432557`), 131 `doctor`
  (`34510e8`, also the deploy-lock read split and the `dump` checkpoint disk preflight fix), 130
  `compare --targets` (`341d6b8`), 129 saved deploy plan `--save-plan`/`--apply` (`787a2f7`).
- Before them on the branch: 118-121, 123, 124 deploy lock (`a73d4db`), 125 per-module source (`1d29ccb`),
  126 code rollback (`fa3dad6`), `link --add`, remote `ps`, `uninstall` through `odoo shell`, and the store
  adoption (`516a05e`).
- `go build`, `go vet` and `go test ./...` are green at `72fc85a`. Units 124-131 are covered with temp `HOME`
  and a fake `ssh` on `PATH` ([code-standards](knowledge/code-standards.md)); none has run against a real
  server yet ([ledger](knowledge/operations.md#live-verification-ledger)).
- The `odoo-probe` skill (`~/Documents/Projects/odoo-probe/SKILL.md`) teaches 127-131 (config parse error,
  undeclared stage, `doctor`, `compare --targets`, `deploy --lock`, dependency check, saved plans): commit
  `2dff0e7` in that repo, not pushed.

## Blockers

| Blocker | Detail |
|---|---|
| none | The live checks wait only on the user's authorization. |

## Next

1. Live checks of 124-131 against the real hosts, authorised by the user, read-only first:
   `echo_cli doctor --from habitta_prod`, `echo_cli compare --targets habitta_dev,habitta_prod`,
   `echo_cli deploy --modules ccima_flow_mail@99f2109 --from habitta_prod --dry-run --save-plan plan.json`
   (rows in the [ledger](knowledge/operations.md#live-verification-ledger)).
2. Small fixes, one commit each ([plan](work/build/plan.md#small-fixes-found-during-the-2026-09-30-store-adoption));
   two were added by 127-129: `-C <alias>` hides a config parse error, and `deploy --json` omits `code_sha` and
   `dependencies`.
3. Unit 122 (remove `-E`): write its spec first, then E8, E5, E6.

## Messages

| File | From → to | Status |
|---|---|---|
| — | — | none pending |

## Open questions for the user

- The `branch-flow` skill names branches `feature/<slug>`; the working branch is `feat/reverb-link-deploy-safety`.
  Rename it, or start the next unit (Unit 122) on a fresh `feature/<slug>` branch?

## Pending, not blocking

- Units 127-131 deviated from their specs only in small ways the user accepted (type names, an extra `rawStage`
  field on `connectTarget`, `--force` also skipping the dependency prompt in a TTY, `compare` usage errors now
  exiting 2); the behaviour-visible ones are in [deploy-safety](knowledge/deploy-safety.md#dependency-check) and
  [modules-and-odoo](knowledge/modules-and-odoo.md).
- Technology facts collected during the adoption belong in the `odoo-dev` / `odoo-probe` skills, not here; the
  list was handed to the user on 2026-09-30.
- `gofmt -l` flags 8 pre-existing files (e.g. `internal/cmd/actions_test.go`); untouched on purpose.
