# Build plan
> The open units of Echo, in order. Everything not listed here is implemented; shipped units live in git, the CHANGELOG and [archive](../../archive/2026-09-30-original/specs/). Last verified: 2026-09-30.

Order: unblocked first; a unit gets a spec (`/ctx spec build <NN-name>`) before any code.

| # | Unit | Builds | Depends on | Status |
|---|---|---|---|---|
| 132 | db-restore-progress | Byte progress + 30 s heartbeat for `db-restore` load, unzip and filestore copy, so a working restore never looks hung. [Spec](132-db-restore-progress.md). | — | implemented; live verification pending |
| 139 | init-remote | `init --remote` / `init --from <t>`: write the server-side Echo profile from the client over SSH (detect compose, services, DB, Odoo version; edit an existing profile in place; register and link the target), so a host without `echo_cli` installed works with every remote verb. Numbered after the sideload branch's 133-138. [Spec](139-init-remote.md). | — | implemented; live verification pending |
| 133 | sideload | `sideload <path> --from <t>`: ship addons that live outside the repo to a dev/staging target under `.echo/sideload/`, mounted through an Echo-owned compose override; lock entries `source=sideload`, prod refused, `--list`, `--json`. Re-bases the never-merged prototype `2ba4e48`. [Spec](133-sideload.md). | — | implemented; live verification pending |
| 134 | sideload-safety | DB checkpoint (deploy's policy) and code snapshot of `.echo/sideload/<addon>` (`createCodeSnapshot`) around the sideload run, restored through deploy's `rollbackDecision`. | 133 | pending, needs spec |
| 135 | sideload-rm | `sideload --rm <addon>`: uninstall, unmount, delete, drop the lock entry; `--keep-installed` graduates an addon that now lives in the repo under the same name. | 133 | pending, needs spec |
| 136 | sideload-local | Local mode: bind the addon's own path in a local compose override (live edits, no copy), then `-i`/`-u`. | 133 | pending, needs spec |
| 122 | remove-env-flag | Delete `-E`/`--env`: the parser cases, the `env:` prefix, `resolveReverbShell`, `reverbDeferred`/`requireNoReverb`, the `[reverb] compose_cmd`/`ssh_host` overrides. The HTTP client stays (Unit 120 uses it). Script in [spec 121](../../archive/2026-09-30-original/specs/121-reverb-link-mode-docs.md). | Reverb unit 30 (live since 2026-09-09) | pending, needs spec |
| E8 | reverb-update-verdict | Delegated `update --remote` on a Reverb env takes its verdict from the job status, not from the ERROR-line scanner. Bug found in the live acceptance of 2026-09-10. | Unit 123 | pending, needs spec |
| E5 | link-reverb | `link --reverb`: find the Reverb env of the current branch, register it as a connect target and bind the directory. | Reverb units 30, 31 | pending, needs spec |
| E6 | refresh-remote | `refresh --remote`: trigger `POST /environments/{id}/refresh` on the linked Reverb env. | Reverb unit 32 (done) | pending, needs spec |
| — | i18n-live-stream | Stream the output of the i18n commands live instead of at the end. | — | agreed, not scheduled |
| — | i18n-conf-debug | A debug flag that prints the ephemeral `odoo.conf` Echo generates for Odoo 19 i18n. | — | agreed, not scheduled |

## Small fixes found during the 2026-09-30 store adoption

Defects read in the code (not run); each is described where its topic lives. One commit each, no spec needed unless it grows.

| Fix | Where | Detail |
|---|---|---|
| `scriptExitCode` has no `ErrUsage` branch: `db-*` usage errors (`db-admin`, `ErrDBExists`) exit 1 and auto-copy instead of exiting 2 | `internal/repl/repl.go` | [database](../../knowledge/database.md) |
| The `i18n-pull` builder ignores `SkipDecide`, so inside `sequence` it still asks Run/Copy/Cancel and Cancel does not cancel | `internal/cmd/build_i18npull.go` | [scripting](../../knowledge/scripting.md) |
| `db-pull --restore` downloads the whole dump before `requireDBContainer` fails | `internal/cmd/db_pull.go` | [database](../../knowledge/database.md) |
| `remoteFilestoreTarCmd` uses the local project's `filestore_path` for the remote container | `internal/cmd/db_pull.go` | [database](../../knowledge/database.md) |
| Stale texts: the dirty-module WARNING "does not push the code" (printed even when pushing), the `RunDBBackup` comment about a host filestore, the `promote_resolve.go` comment naming a `develop` default | `internal/cmd` | [deploy](../../knowledge/deploy.md) |
| Dead Unit 14 leftovers: the `planet`/`python`/`anchor` cases of the `logo` key | `internal/banner/header.go` | [ui](../../knowledge/ui.md) |
| `init` does not ask for `[checkpoint]` (Unit 90 follow-up) | `internal/cmd/init.go` | [deploy](../../knowledge/deploy.md) |
| `ResolveProjectAlias` swallows the `LoadGlobal` error: with `-C <alias>` and a `global.toml` that does not parse, Echo says "unknown project alias" instead of the Unit 127 `ERROR echo.config` line | `internal/config/project_alias.go:75` | [architecture-and-traps](../../knowledge/architecture-and-traps.md) |
| `deploy --json` omits `code_sha` and `dependencies`: `deployJSON` leaves out `DeployResult.CodeSHA` and `.Dependencies`, which the README deploy section promises | `internal/repl/deploy.go:99` | [deploy](../../knowledge/deploy.md) |

E5, E6 and E8 come from the Reverb link-mode plan (`~/Documents/Projects/dev_tools/reverb/docs/echo-link-mode-echo-units.md`); they get Echo unit numbers when their spec is written.

Closed without implementation: Unit 14 (meta-commands) and Unit 114 (`connect --serve`); see [decisions](../../decisions.md#process).
