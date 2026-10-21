# Unit 149 · relay-target
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-21.

## Goal

Echo operates the Odoo envs that Relay deploys (first `fb/stage` and `fb/stable` on `morwi-fb`) without deploying
them itself. Relay answers where an env lives (`GET /api/v1/resolve/{app}/{env}`, Relay unit 25); Echo reaches the
receiver over SSH with the user's own ssh config and runs `docker exec` in the containers Relay names. Relay's
decision: `relay:context/decisions.md`, "echo_cli reaches receivers over SSH with the user's key; Relay only
resolves the target" (2026-10-02).

## What a Relay env looks like on the receiver (measured on morwi-fb on 2026-10-21)

- Containers `relay-<app>-<env>-odoo` and `-db`, compose project `relay-<app>-<env>`, created by relayd with no
  compose file (`com.docker.compose.project.config_files = -`). `docker compose -p <project> ps --format json` works
  without a file; `docker compose -p <project> logs <container name>` prints nothing, so the default log is
  `docker logs <odoo container>`.
- The addons are baked into the image (`/mnt/extra-addons`); there is no checkout, overlay or `.env` on the host.
- `/etc/odoo/odoo.conf` in the odoo container holds `db_host`, `db_user`, `db_password`, `addons_path` and
  `db_name`, and the image sets `ODOO_RC` to it, so `docker exec <odoo> odoo …` needs no `--db_*` flag.
- The SSH user `ubuntu` is in the `docker` group and cannot read `/home/relay`; nothing on the host is needed.

## Design

### Configuration

```toml
[relay]
url = "https://relay.cerebellum.technology"
token_file = "~/.config/relay/echo-token"   # or token = "rlt_…"; scope echo

[connect_targets.fb-stage]
ssh_host = "morwi-fb"
relay = "fb/stage"
db_name = "fuentebuena"
```

- `[relay]` is global-only, like `[reverb]`; `token` wins over `token_file`, which is read when a command needs it
  (`~` expanded). The token is never logged or copied into a project profile.
- `relay = "<app>/<env>"` marks the target kind. In memory such a target's `RemotePath` is `relay:<app>/<env>`, so
  every place keyed by host and path (the link binding, `targetNameAt`, deploy history) works unchanged; the file
  never stores that path. The new key is written only on Relay targets, so saving the file leaves every other target
  byte-identical.
- `ssh_host` is the user's alias, as for every Echo target. Without one, registration stores the host Relay reports.

### Registration (headless)

`link --add <name> --relay <app>/<env> [--ssh-host <alias>]` resolves the env through Relay (an unknown env or a
missing token fails before anything is written), saves the target with `db_name` from the payload and binds the
current directory like `--add`. `link <name>` binds to an existing one. `--relay` and `--ssh-host` are only valid
with `--add`.

### Resolution

`resolveRemoteShell` sees the `relay:` path and builds the context from one resolve call: odoo and db containers,
db name, Odoo version, stage (normalized like a server profile's), the container's `odoo.conf` as `ConfPath`, and an
`odoo.Conn` with only the database. Both the Odoo and the DB transport are `docker exec -i <container>` (no `cd`,
no compose service), interactive shells `docker exec -it`. The system line and the undeclared-stage warning are the
classic ones.

### Commands

| Command | On a Relay target |
|---|---|
| `shell-run`, `shell`, `test`, `update` | `docker exec` in the odoo container, same argv as today; the prod gate reads Relay's stage |
| `ps` | `docker compose -p <project> ps --format json` |
| `logs` | `docker logs [-f] [--tail N] <odoo container>`; `--all` or named services go through `docker compose -p <project> logs` |
| `i18n-pull` | export in the odoo container with `-c` on the container's own `odoo.conf` (Odoo 19), no ephemeral conf |
| `i18n-update` | gains `--from`/`--remote` for every remote target: the local `<mod>/i18n/<lang>.po` is streamed into the odoo container and imported there (prod-gated); Odoo 19 uses the real conf on a Relay target and the ephemeral one on a classic target, as `i18n-pull` does |
| `deploy` | delegated: `POST /api/v1/deploys {app, env, dry_run}` with the echo token, then followed by polling `GET /api/v1/deploys/{id}` (state and step changes) until it ends; a deploy that does not succeed exits 1; `awaiting_approval` stops following with a warning. Only `--from`, `--remote`, `--dry-run`, `--force` and `--json` are accepted; any other deploy flag is a usage error naming Relay as the planner |
| `push` and every other remote verb | refused before any SSH with exit 2 and the reason: Relay bakes the addons into the image, so `push`, `watch`, `sideload`, `checkpoint`, `up/stop/restart/down`, `db-*`, `actions` and the rest would act on a host path that does not exist or fight Relay's deploys |

The refusal is one check at dispatch (`cmd.RelayGuard`), from the config alone: the target is `--from`, else the
binding, for a command that runs remotely (a remote flag, or a command that is always remote).

### Client

`internal/relay`, a leaf package like `internal/reverb`: `Resolve`, `CreateDeploy`, `Deploy`; Relay's error envelope
mapped to sentinels (`ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`) carrying Relay's message; the token only in
the `Authorization` header, never in an error.

## Implementation

- `internal/config`: `[relay]` (`RelayURL`, `RelayToken`, `RelayTokenFile`), `ConnectTarget.Relay`, the `relay:`
  path helpers; round-trip test (other targets unchanged, `[relay]` kept).
- `internal/relay`: client and tests against `httptest`.
- `internal/cmd`: `relay.go` (resolve into `remoteShellContext`, guard, deploy delegation), the exec helpers keyed on
  the target's exec mode, the `ps`/`logs`/`shell`/`i18n-pull`/`i18n-update`/`link` branches.
- `internal/repl`: the guard in dispatch; `commandFlags` and help for `link --relay/--ssh-host` and
  `i18n-update --from/--remote`; `i18n-update` in `projectlessOneShot` with a remote flag.
- Tests: config round trip; client (payload, error codes, no token in errors); resolve into a context with docker
  exec commands (fake `ssh` on PATH records the command); guard table (supported, refused, classic untouched,
  local commands untouched); deploy delegation against an `httptest` Relay (request body, follow to success,
  failure exit, refused flag).

## Verify when done

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; changed files `gofmt`-clean; `CHANGELOG.md` updated.
- [ ] Live, read-only, against `morwi-fb`: `echo_cli ps --from fb-stage` lists `relay-fb-stage-odoo` and `-db`;
  `echo_cli shell-run --from fb-stage` printing `fb_b2c_origination`'s installed version answers `19.0.4.1.1`;
  `push --from fb-stage` is refused with exit 2.
- [ ] Same against the production Relay once relayd on Ionos serves unit 25 and an echo token exists.
