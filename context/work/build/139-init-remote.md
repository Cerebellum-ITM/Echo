# Unit 139 · init-remote
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-15.

## Goal

`echo_cli init --remote` (new host + path) and `echo_cli init --from <target>` (registered target whose profile
is missing or wrong) write the server-side Echo profile **from the local machine over SSH**, so a host where
`echo_cli` was never installed works with every remote verb (`link --show`, `doctor`, `deploy`, `push`,
`sideload`, …). Interactive by default, fully headless with flags.

## Design

Why: every remote verb reads `~/.config/echo/projects/<key>.toml` on the server ([remote-targets: server-side
Echo profile](../../knowledge/remote-targets.md#server-side-echo-profile)), and today only `echo_cli init` run
**on** the server creates it. The binary itself is never executed remotely. This unit changes who writes the
file, not where the stack is declared: the profile still lives on the server and stays the only source of the
stack and the stage ([decisions](../../decisions.md#remote-targets)).

Decisions (the implementer makes none):

1. **Entry points.** `init --remote` picks the SSH host from `~/.ssh/config` (`sshConfigHosts`, same picker as
   `registerTarget`) and asks for the remote path (input). `init --from <target>` takes host and path from a
   registered connect target (exact name; unknown name → `unknown connect target`, exit 1; no binding fallback).
   `init` with neither flag is the current local `init`, unchanged.
2. **Path and key.** The path is normalized remotely with `cd <path> && pwd` (logical path, the same form Go's
   `os.Getwd` gives a server-side Echo through `$PWD`), so `~/x` and trailing slashes work. The directory must
   contain `docker-compose.yml` or `docker-compose.yaml` (the names `internal/project` accepts), else exit 1
   `no docker-compose.yml in <path> on <host>`. Key = `config.ProjectKey(<normalized path>)`. The normalized
   path is what goes into `project_path` and into the connect target's `remote_path`; they must be the same
   string or the profile is unreachable. A symlinked spelling of the same directory gives another key: that is
   the existing rule, not a new one.
3. **Compose command.** Read the remote `~/.config/echo/global.toml` first (optional). If it declares
   `compose_cmd`, use it for every probe. Otherwise probe `docker compose version`, then `docker-compose
   version`. v2 → nothing to write (the parser defaults to `docker compose`). Only v1 → if the remote
   `global.toml` is absent, write it with `compose_cmd = "docker-compose"` only; if it exists without
   `compose_cmd`, exit 1 with a hint to add the key there. **Never rewrite an existing remote `global.toml`.**
   Neither → exit 1 `docker compose not found on <host>`.
4. **Detection (all best-effort except the service list).**
   - Services: `cd <path> && <compose> config --services` (works with the stack stopped, so no `up` prompt,
     unlike local `init`). Failure → exit 1 with the compose stderr. Running containers from
     `<compose> ps --status=running --format '{{.Service}}\t{{.Name}}'` only enrich the labels.
   - Defaults: `odoo_container`/`db_container` by `matchService` (`odoo`; `db`, `postgres`), as local `init`.
   - `.env` through `remotePullEnv` for `POSTGRES_USER`/`POSTGRES_DB`.
   - Databases: `<compose> exec -T <db> psql -U <user> -lqt` (parse with the same code as
     `docker.ListDatabases`). Failure → the DB field becomes an input.
   - Odoo version: `<compose> exec -T <odoo> odoo --version`, major from `Odoo Server NN.0`. Failure → default.
5. **Existing profile = edit in place.** If the remote project file exists, decode it with `toml.Decode` into
   `projectFile`, prefill the form from it and rewrite only the init fields: `odoo_version`, `stage`,
   `odoo_container`, `db_container`, `db_name`, `project_path`. Every other section (`[deploy]`, `[push]`,
   `[checkpoint]`, `addons_*`, `conf_path`, …) is preserved. Refuse (exit 1, nothing written):
   - a `[reverb]` marker: `profile is owned by Reverb; edit it there`;
   - any key `MetaData.Undecoded()` reports (written by a newer Echo): name the keys, ask to update Echo.
   A file that does not parse → the Unit 127 error form (`server profile <path> on <host> does not parse: …`).
6. **Stage safety.** Headless requires `--stage` (an undeclared stage reads as prod everywhere, so the writer
   must say it). If the existing profile's stage is `prod` (or undeclared) and the new stage is not `prod`,
   confirm with the [remote prod gate](../../knowledge/remote-targets.md#remote-prod-gate) wording
   (`confirmRemoteProd`-style); `--force` skips; no TTY and no `--force` fails closed. Downgrading a prod
   stage silently would disable every remote gate.
7. **Write.** One `runSSH` with the TOML on stdin:
   `umask 077 && mkdir -p ~/.config/echo/projects && cat > <file>.tmp.$$ && mv <file>.tmp.$$ <file>`.
   Then re-read it with `fetchRemoteProfile` and require `ParseRemoteProfile` to succeed (round-trip check).
8. **After the write.** Register a connect target (not with `--from`, it already exists): name input defaulting
   to `defaultTargetName`; headless only with `--name` (absent → INFO `profile written; no target registered`).
   A name that exists with another host/path → exit 2 `target <name> already points at <host>:<path>`; the
   same host+path → reuse silently. Then, with a TTY, a confirm `Link <root> to <name>?` (default yes) that
   calls `bindLinkTarget` (which also probes, so the link is verified end to end). Headless binds only with
   `--link`.
9. **Flags.** `--remote`, `--from <t>`, `--host <alias>`, `--path <dir>`, `--stage dev|staging|prod`,
   `--odoo-version 17|18|19`, `--odoo-container <svc>`, `--db-container <svc>`, `--db <name>`, `--name <t>`,
   `--link`, `--force`. `--host`/`--path` only with `--remote`; `--name` not with `--from`; `--remote` and
   `--from` are exclusive; unknown flags and positionals are rejected. All usage errors are `ErrUsage`
   (exit 2) and are raised before any SSH. Every value flag consumes its value
   ([trap](../../knowledge/architecture-and-traps.md#cross-cutting-traps)). Without a TTY, any field not given
   by a flag that has no detected default is `ErrNonInteractive` naming the missing flag; detected defaults
   (services, DB, version) are accepted headlessly, `--stage` never is.
10. **Logging** (Odoo-style, [ui](../../knowledge/ui.md)): `echo.init` for the flow and `echo.init.detect` for
    probes. Lines: `probing host=… path=…`, `detected compose=… services=N odoo=NN dbs=N`,
    `profile written file=~/.config/echo/projects/<key>.toml stage=…`, `profile updated …` when it existed,
    `registered target target=… host=… path=…`, then the link lines `bindLinkTarget` already emits.

## Implementation

### `internal/config`

- `RemoteInitFields` struct (the six fields of decision 5).
- `RenderRemoteProject(existing []byte, f RemoteInitFields) ([]byte, error)`: empty `existing` → fresh file;
  otherwise `toml.Decode` into `projectFile`, refuse `[reverb]` (`ErrReverbProfile`) and undecoded keys
  (`*UnknownKeysError` listing them), set the fields, encode. Pure, no I/O.
- `RenderRemoteGlobalCompose(cmd string) []byte` for decision 3's v1 case.

### `internal/docker`

- Extract the line parsers of `ListContainers` and `ListDatabases` into `ParseContainers(out)` and
  `ParseDatabases(out)` so the remote path reuses them; the local functions call them. No behavior change.

### `internal/cmd/init_remote.go` (new)

- `RunInitRemote(ctx, InitRemoteOpts) (InitRemoteResult, error)`; opts: `Cfg`, `Root`, `Args`, `Palette`, `Log`.
  Result: host, path, key, profile file, created/updated, target name, linked.
- `parseInitRemoteArgs` (decision 9), seam `initRunSSH = runSSH` for every SSH call.
- Flow: parse → resolve host/path (`--from` via the global targets) → normalize path + compose file check →
  read remote global/project files → compose detection → probes → form (huh, same groups as `RunInit` minus
  the alias group, prefilled) or flags → stage gate → render → write → round-trip read → register → link.

### `internal/cmd/init.go`, `connect_direct.go`, `connect.go`

- `RunInit` is untouched; the REPL routes to `RunInitRemote` when `--remote` or `--from` is present.
- `registerTarget`'s "no Echo projects with a stored path" error and `fetchRemoteProfile`'s
  `run init there first` hint both add: `or run \`echo_cli init --remote\` from here`.

### Wiring ([new command wiring](../../knowledge/code-standards.md#new-command-wiring))

- `internal/repl`: `runInit(args)` handler dispatching local vs remote, exit mapping for `ErrUsage`,
  `ErrNonInteractive`, `ErrCancelled`, `huh.ErrUserAborted`.
- `commandFlags["init"]` with the flags of decision 9; help rows under `init`.
- `main.go` `projectlessOneShot`: `init` when `hasRemoteFlag(args)`.
- Not added to `sequenceCommands` (interactive setup, not a pipeline step).

### Store and docs

- [remote-targets](../../knowledge/remote-targets.md): the server-profile section says the profile is written
  either by `init` on the server or by `init --remote|--from` from a client, with the key/path rule (decision
  2) and the "never rewrite remote `global.toml`" rule.
- README `init` section; `CHANGELOG.md` `[Unreleased]` → `Added`.

## Dependencies

- none (BurntSushi `toml` already provides `MetaData.Undecoded`).

## Verify when done

- [ ] `RenderRemoteProject`: fresh file; existing file with `[deploy]`, `[push]`, `[checkpoint]` keeps them
      byte-for-byte equivalent after decode; `[reverb]` refused; an unknown key refused and named; the output
      round-trips through `ParseRemoteProfile` with the six fields set.
- [ ] `RunInitRemote` headless with a scripted `initRunSSH`: the profile lands at
      `projects/<sha256 of the normalized path>.toml`, `project_path` and the registered target's
      `remote_path` are the same string, no `compose up` is issued, and the remote `global.toml` is written
      only in the v1-and-absent case.
- [ ] Stage gate: existing `prod` (and undeclared) → `--stage dev` without TTY or `--force` fails closed and
      writes nothing; with `--force` it writes.
- [ ] Usage: `--host` without `--remote`, `--remote --from x`, `--name` with `--from`, an unknown flag, a
      positional → exit 2 before any SSH; `init --from prod --stage dev` does not read `prod` as a positional.
- [ ] Headless without `--stage` → exit 2 naming `--stage`.
- [ ] Live, against a server directory that has no Echo profile (no binary installed): `echo_cli init --remote
      --host <alias> --path <dir> --stage staging --name <t> --link`, then `echo_cli link --show`,
      `echo_cli doctor --from <t>` (all checks ok) and `echo_cli sideload <addon> --from <t> --dry-run` (once
      Unit 133 is merged). Record it in the [ledger](../../knowledge/operations.md#live-verification-ledger).
- [ ] The interactive form driven in tmux (tui-probe): prefill from an existing profile, the prod-downgrade
      confirm, the link confirm.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; `registry_test.go`, `commandhl_test.go` pass;
      CHANGELOG entry in the same commit.
