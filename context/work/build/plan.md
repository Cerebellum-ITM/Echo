# Build plan
> The open units of Echo, in order. Everything not listed here is implemented; shipped units live in git, the CHANGELOG and [archive](../../archive/2026-09-30-original/specs/). Last verified: 2026-09-30.

Order: unblocked first; a unit gets a spec (`/ctx spec build <NN-name>`) before any code.

| # | Unit | Builds | Depends on | Status |
|---|---|---|---|---|
| 122 | remove-env-flag | Delete `-E`/`--env`: the parser cases, the `env:` prefix, `resolveReverbShell`, `reverbDeferred`/`requireNoReverb`, the `[reverb] compose_cmd`/`ssh_host` overrides. The HTTP client stays (Unit 120 uses it). Script in [spec 121](../../archive/2026-09-30-original/specs/121-reverb-link-mode-docs.md). | Reverb unit 30 (live since 2026-09-09) | pending, needs spec |
| E8 | reverb-update-verdict | Delegated `update --remote` on a Reverb env takes its verdict from the job status, not from the ERROR-line scanner. Bug found in the live acceptance of 2026-09-10. | Unit 123 | pending, needs spec |
| E5 | link-reverb | `link --reverb`: find the Reverb env of the current branch, register it as a connect target and bind the directory. | Reverb units 30, 31 | pending, needs spec |
| E6 | refresh-remote | `refresh --remote`: trigger `POST /environments/{id}/refresh` on the linked Reverb env. | Reverb unit 32 (done) | pending, needs spec |
| — | partial-deploy-dependency-check | `deploy --dry-run` warns when the shipped modules remove methods or fields that modules staying on the server still use (the failure behind the 2026-09-30 incident, see [deploy-safety](../../knowledge/deploy-safety.md)). | Units 124-126 | future, needs design |
| — | i18n-live-stream | Stream the output of the i18n commands live instead of at the end. | — | agreed, not scheduled |
| — | i18n-conf-debug | A debug flag that prints the ephemeral `odoo.conf` Echo generates for Odoo 19 i18n. | — | agreed, not scheduled |

## Small fixes found during the 2026-09-30 store adoption

Defects read in the code (not run); each is described where its topic lives. One commit each, no spec needed unless it grows.

| Fix | Where | Detail |
|---|---|---|
| A TOML syntax error in `global.toml` loads defaults silently and the next save overwrites the file | `internal/config` | [architecture-and-traps](../../knowledge/architecture-and-traps.md) |
| `scriptExitCode` has no `ErrUsage` branch: `db-*` usage errors (`db-admin`, `ErrDBExists`) exit 1 and auto-copy instead of exiting 2 | `internal/repl/repl.go` | [database](../../knowledge/database.md) |
| The `i18n-pull` builder ignores `SkipDecide`, so inside `sequence` it still asks Run/Copy/Cancel and Cancel does not cancel | `internal/cmd/build_i18npull.go` | [scripting](../../knowledge/scripting.md) |
| `db-pull --restore` downloads the whole dump before `requireDBContainer` fails | `internal/cmd/db_pull.go` | [database](../../knowledge/database.md) |
| `remoteFilestoreTarCmd` uses the local project's `filestore_path` for the remote container | `internal/cmd/db_pull.go` | [database](../../knowledge/database.md) |
| Stale texts: the dirty-module WARNING "does not push the code" (printed even when pushing), the `RunDBBackup` comment about a host filestore, the `promote_resolve.go` comment naming a `develop` default | `internal/cmd` | [deploy](../../knowledge/deploy.md) |
| Dead Unit 14 leftovers: the `planet`/`python`/`anchor` cases of the `logo` key | `internal/banner/header.go` | [ui](../../knowledge/ui.md) |
| `init` does not ask for `[checkpoint]` (Unit 90 follow-up) | `internal/cmd/init.go` | [deploy](../../knowledge/deploy.md) |

E5, E6 and E8 come from the Reverb link-mode plan (`~/Documents/Projects/dev_tools/reverb/docs/echo-link-mode-echo-units.md`); they get Echo unit numbers when their spec is written.

Closed without implementation: Unit 14 (meta-commands) and Unit 114 (`connect --serve`); see [decisions](../../decisions.md#process).
