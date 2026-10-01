# Remote Targets
> Owns how Echo reaches a remote Odoo: connect targets, link bindings, the server-side Echo profile, the resolution chain and its flags, SSH transport, the remote prod gate, host-FS vs container probing, `connect` (CDP session cache) and Reverb link mode. Last verified: 2026-09-30.

Vocabulary used across the store:

- **Connect target**: a named `ssh_host` + `remote_path` in the machine-wide `global.toml`.
- **Link binding**: the per-project `[connect]` section that points one local directory at one target.
- **Server profile**: the Echo config that lives on the remote host and describes its own stack.

Deploy, checkpoints and the lock build on this file: [deploy](deploy.md), [deploy-safety](deploy-safety.md). Config storage (where the files live, load-modify-write saves) is in [architecture-and-traps](architecture-and-traps.md).

## Connect targets

- Stored in `global.toml` `[connect_targets.<name>]` (`config.ConnectTarget`: `ssh_host`, `remote_path`, `chrome_path`, `db_name` (display only), plus the git-deploy fields owned by [deploy](deploy.md)). `config.sortedConnectTargets` returns them **sorted by name**, so `link --next` cycles alphabetically, not in registration order.
- **Echo has no SSH port, user or key field in any mode.** `ssh_host` is passed verbatim to `ssh`/`rsync`; port, user, identity and ProxyJump come from the user's `~/.ssh/config` (Unit 108, `reverbSSHHost`). A literal `user@ip` matches no `Host` block and silently loses all of that. Do not add a second SSH-configuration channel; see [decisions](../decisions.md#remote-targets).
- Registration (`registerTarget`, `connect_direct.go`): pick a `Host` alias from `~/.ssh/config` (`sshConfigHosts`: literal aliases only, wildcards skipped, **no `Include` handling**), then pick one of the Echo projects found on that server, then name it. It needs a TTY (pickers). Entry points: `link --add`, `connect --add`, or `connect` with no targets registered. `link --add` registers and binds without minting a session or opening Chrome (c296284).
- A server project is only offered when its profile recorded `project_path`. Echo backfills it at startup on the server (`config.BackfillProjectPath`); an old profile that was never run again is invisible until Echo runs there once.
- `-C <value>` (local directory alias) is a different thing: it resolves a local path (a real directory wins, then `[project_aliases]`, then a connect target whose `remote_path` is a local dir). Aliases are local paths; targets are SSH destinations. They are separate maps on purpose (Unit 48, `config/project_alias.go`).

## Link bindings (`link`)

- Binding = copying the target's `ssh_host`/`remote_path` (and `chrome_path`) into the project's `[connect]` (`bindLinkTarget`). State is keyed by the project root's key; a subdirectory of a repo resolves to the git root (see [architecture-and-traps](architecture-and-traps.md)).
- **Save happens before the probe.** An unreachable host is a WARNING `linked but unreachable`, exit 0, binding kept: a dead VPN must not lose the binding (Unit 60, `bindLinkTarget`).
- Rebinding to the host+path already bound is a no-op (`already linked`, no write, no probe). Matching is by `ssh_host`+`remote_path`, not by name (`linkTargetName` returns `""` for a hand-written binding that matches no target).
- Modes `--show`, `--rm`, `--next`, `--list`, `--add` and a positional target are mutually exclusive; `--json` is valid only with `--list` (`parseLinkArgs`). Usage errors must surface as `ErrUsage` so they exit 2 instead of falling into the command-failure path (exit 1 plus clipboard copy) (Unit 106).
- **`--list` and bare-picker rows are offline**: built from local records only, no SSH, no stage/health per row. Resolving `stage` per target would cost one SSH round trip per row, and the switcher has to be instant. `--show` is the probing command (Unit 106).
- `--show` reads the server profile, prints the system line, then `deploy code` (branch/sha/ref/at, git targets only), the deploy lock summary, the `reverb env ... api=on|off` line (Reverb targets only) and the remote `compose ps`. odoo-probe's pre-flight reads `api=on|off` (Unit 120, `reportReverbEnv`).
- Without a name and with one registered target `link` auto-uses it; with several it opens a TTY-guarded picker with a "register" row; with none it fails with `ErrNoConnectTargets` pointing at `link --add` (`pickConnectTarget`, shared with `deploy`).

## Server-side Echo profile

- Remote verbs do not redeclare the stack locally. They read the server's own Echo config over SSH: `~/.config/echo/global.toml` (optional) and `~/.config/echo/projects/<key>.toml`, where `<key>` = `config.ProjectKey(remote_path)` (same hash Echo uses locally). A missing project profile fails with a hint to run `init` on the server (`fetchRemoteProfile`, `config.ParseRemoteProfile`).
- Everything remote comes from this profile, never from the local config: compose command, container names, DB name, **stage**, Odoo version, addons mode/paths, `conf_path`, server-declared `[push]`, `[checkpoint]`, `[deploy]` (actions, push, test) and the `[reverb]` marker. The precedence between server and local values is a deploy concern: [deploy](deploy.md).
- **The profile's `stage` is what every remote gate reads**, not the local `cfg.Stage`. A gate that read the local stage would not ask for `db-admin --from prod` run from a dev checkout (Unit 116).
- A remote profile that does not parse fails the command before it touches the server: `fetchRemoteProfile` (`internal/cmd/connect.go:340`) turns the `*config.ParseError` from `ParseRemoteProfile` into `server profile <path> on <host> does not parse: line N, column M: …` (exit 1). `registerTarget` instead skips an unparseable profile with a WARNING naming the file (`remoteEchoProjects`, `internal/cmd/connect_direct.go:235`). A target with no `odoo_version` shows `odoo=unknown` in the system line, and the Odoo 19 i18n path falls back to the legacy form (see [modules-and-odoo](modules-and-odoo.md#i18n)).
- The remote DB credentials are not in the profile: they come from the remote project's `.env` (`remotePullEnv`). A read failure yields an empty map and Odoo falls back to its own container config; it is never an error.

## Remote resolution chain and flags

- Remote mode is selected by `--from <target>` / `--from=<target>` (implies remote) or bare `--remote` (`remoteFlagsIn`). `-E`/`--env` is the deprecated Reverb flag (below).
- Chain (`resolvePullRemote`, `resolveRemoteTarget`): explicit `--from` name > the project's `[connect]` binding > global targets (one is auto-used with a `using connect target` line, several open a TTY-guarded picker, none gives `ErrNoPullRemote`). An unknown `--from` name is an error (`unknown connect target`).
- `resolveRemoteShell` (`shell_remote.go`) is the shared preamble: resolve target, read the server profile, build the Odoo connection, derive the DB transport, and attach the Reverb context when applicable. It serves `shell`, `shell-run`, `update`, `test`, `view`, `compare`, `up/stop/restart/ps/logs`, `push`, `checkpoint`, `actions`, `db-pull`, `db-admin`, `watch` and `deploy`'s `--rollback`, `--restore-code`, `--set-code` and lock-read paths. The main `deploy` flow, `i18n-pull`, `connect` and `link` call `resolveRemoteTarget`/`resolvePullRemote` + `fetchRemoteProfile` directly, so they do **not** get the Reverb marker context (they still get the DB transport via `remoteConnectTarget`).
- **A remote flag is a one-shot-projectless trigger.** `projectlessOneShot` (`main.go`) lets many commands run with no `docker-compose.yml` when `--from`/`--remote` is present (`hasRemoteFlag`). Some commands are projectless always. Locally they still need the compose project. Full list lives in `main.go`; do not copy it.
- **Every parser must consume the value of `--from` (and `-E`/`--env`).** `remoteFlagsIn` only reads; each command parser also has to skip the value token or `--from prod` leaves `prod` as a positional (a module name, a DB name, a script file). Found in Units 75, 79, 80, 107, 116; applies to any new value-taking remote flag.
- **Unknown flags are silently ignored by `connect` (`parseConnectArgs`, `parseDirectArgs`) and `test` (`parseTestArgs`)**, kept for forward compatibility, while `update --remote`, `link` and `compare` reject them with an error. A mistyped flag on the first group runs the command without it.

## SSH transport

- Three buffered/streamed runners in `connect.go`/`remote.go`:
  - `runSSH`: one remote command, buffered stdout, stderr folded into the error; for short request/response calls (profile read, `cat`, `test -f`).
  - `runSSHStream`: stdout and stderr both forwarded line by line to a callback, 1 MiB scanner buffer (tracebacks and SQL logs exceed the default 64 KiB), the error carries the last non-blank stderr line. This is what keeps "stream, never buffer" true for remote runs and lets remote Odoo output go through the same colorizer, counters and `report`/`copy-last` capture as local.
  - `runSSHToFile`: binary stdout straight to a file with progress every ~1 MiB; deletes the partial file on any failure.
- All use `ssh -o BatchMode=yes`: a missing key or password prompt fails fast instead of hanging; auth is the user's ssh config/agent. Interactive remote shells use `ssh -tt -o BatchMode=yes` through `docker.RunInteractive` (the same PTY machinery as local, so capture and colorizing match); they need a TTY and fail closed without one (`shell_remote.go`, Unit 62).
- Remote command shape: `cd <remote_path> && <compose> exec -T <container> <argv...>`. Every token is shell-quoted (`shellQuote`) but the compose command is emitted raw so `docker compose` splits into two words. `exec -T` yields plain logs (no ANSI) (`remoteExec`, `remoteComposeCmd`).
- `compose exec` bypasses the image entrypoint, so remote Odoo calls pass explicit `--db_*` flags from the remote `.env`. An unset value must be skipped, never emitted as `--db_port=` (Odoo crashes on `int('')`).
- Test seams for these runners (`sshStreamCommand`, `sshToFileCommand`, fake `ssh` on PATH): [code-standards](code-standards.md).

## Remote prod gate

- Rule: a remote **mutation** on a target whose server-profile stage is `prod` asks for confirmation (`confirmRemoteProd`); `--force` skips it; with no TTY it fails closed. Read-only remote verbs never gate. The local-stage rule is in [architecture-and-traps](architecture-and-traps.md).
- **An undeclared stage is `prod`.** A server profile whose `stage` is missing or not `dev`/`staging`/`prod` gates as `prod` everywhere `target.stage` is read: the confirm above, the deploy checkpoint default, test-on-prod, `watch` on prod, `db-admin` risk, `db-pull` auto-neutralize and the `ECHO_STAGE` of deploy actions (`remoteConnectTarget`, `internal/cmd/db_remote.go:27`). The command logs one WARNING right after the system status line, `target stage is not declared on the server (stage="") — treated as prod; set stage in the server profile` (`warnUndeclaredStage`, called by `deploy`, `i18n-pull`, `link --show`, `connect` and `resolveRemoteShell`). The status line and pickers still show the raw value. Reverb targets built by `resolveReverbShell` take the stage from the resolve payload and are not normalized; Reverb always writes `dev` or `staging` (Unit 127).
- Gated today (grep `confirmRemoteProd`): `shell`, `shell-run`, `update`, `test`, `stop`, `restart`, `push`, `push --clean`, `checkpoint`, `actions` edit, `deploy --restore-code`, `deploy --set-code`; `connect` has its own equivalent. `up` is not gated (non-destructive).
- Not gated: `logs`, `ps`, `view`, `compare`, `i18n-pull`, `db-pull` (reads the remote, writes locally).
- The asymmetry is intentional: local `restart`, `update`, `install`, `uninstall`, `test` never gate, but the same verb on a remote prod target does, because a remote prod restart or `test --update` is an outage (Units 72, 75).
- Reverb lifecycle verbs and snapshots still pass through the same gate before calling the API (`runReverbEnvAction`, `runCheckpointReverb`).

## Host filesystem vs container probe

- A remote module's files can live in two places, and the probe order is fixed (`remoteModuleBase`, `view_remote.go`): first the remote **host** filesystem (`<remote_path>/<addons_path>/<mod>/__manifest__.py`, via `ssh test -f`), then the **container** for absolute (conf-mode) paths (`remoteContainerCmd ... test -f`). The first hit fixes the transport for the following `find`/`cat`. Relative paths are host, absolute are container.
- Used by `view --from`, `compare --from`, `compare --all`, `push` module discovery. Their primitives are shared: `resolveRemoteView`, `remoteModuleFiles`, `remoteReadModuleFile`.
- A target that only has a Reverb context has empty `AddonsPaths`; the base probes then fall back to `.`, `addons`, `custom` (`reverb.go` comment, `remoteModuleBase`).
- Module pickers for remote `test`/`view`/`compare` list the **local** linked checkout's modules (the binding ties directory to target); `update`'s picker lists the **remote** project's addons, or installed modules with `--installed` (`remoteUpdateCandidates`). See [modules-and-odoo](modules-and-odoo.md).

## DB exec transport (Unit 119)

- Everything Echo runs inside the remote Postgres container assumed `db_container` was a compose **service**. For a Reverb host the DB is not a service of the environment's compose, so it failed with `no such service` even though the container name is right as `--db_host`.
- `connectTarget.dbExec` is `compose` (default: `cd <path> && <compose> exec -T <db> ...`) or `docker` (`docker exec -i <db> ...`, no `cd`). It is `docker` when the server profile has the `[reverb]` marker. `-i`, not `-it`: stdin matters (`pg_restore < file`) and SSH runs have no TTY (`db_remote.go`).
- `withDBExecFallback` retries once with `docker exec -i` when a compose exec error contains `no such service` (hand-built targets whose Postgres left the compose). The retry is not memoized and logs nothing: no ambient logger exists at that depth, and a one-extra-call cost is cheaper than threading state.
- **Every hand-built `connectTarget` goes through `remoteConnectTarget`** (the single place the transport is derived). A new remote path that builds the struct by hand is born with the wrong default.
- Scope: everything inside the Postgres container over SSH (`runRemoteDBCmd`: psql, checkpoints, `df` pre-flight, dump/restore, `db-pull`'s `pg_dump`, deploy/i18n-pull module queries). `db-list` is local-only and untouched. Details of the DB commands: [database](database.md).

## `connect` (browser session)

- `connect` forges a web session (no password involved): the mint script `scripts/connect_mint.py` runs inside the Odoo container (`python3 -` over `compose exec -T`, locally or via SSH) and calls `root.session_store.new()` plus the session-token computation; Echo then sets the `session_id` cookie in a local Chrome over CDP and navigates to `<base>/odoo` (Unit 18).
- **2FA is bypassed on purpose** (dev tool); the only brake is the prod confirm (`maybeConfirmConnectProd`, keyed off the target's stage; `--force` skips).
- Why CDP: the `session_id` cookie is HttpOnly, so a JS bookmarklet cannot set it, and CDP needs no inbound port and nothing installed in Odoo. Rejected: a helper HTTP server (needs an open port), an SSH tunnel (Odoo rewrote `/odoo` redirects to `web.base.url`, dropping the localhost-scoped cookie), installing an Odoo module (Unit 18).
- The DB credentials for the mint travel as `-e ECHO_DB_*` on the `compose exec` command line (`execPythonInOdoo`, `execPythonRemote`), so they are visible in the process list of the machine running compose for the duration of the call.
- Chrome: a dedicated persistent profile at `~/.local/share/echo/connect-chrome` (override `$ECHO_CONNECT_CHROME_PROFILE`; per-target `chrome_path` selects the binary), separate from the user's daily Chrome which cannot expose a debug port. Reuse by reading the profile's `DevToolsActivePort` and probing `/json/version`; a dead port file is removed and Chrome relaunched with `--remote-debugging-port=0`. CDP runs at **browser** level and creates its own target, so reuse never hijacks an existing tab (`connect_cdp.go`, Unit 29).
- Default opens a new tab in the shared context (one Odoo session at a time per profile). `--new-window` creates an isolated browser context (`disposeOnDetach:false`, so closing Echo's connection leaves the window) and allows several users at once.
- `preferHTTPS`: if `web.base.url` is `http` but the host answers on `https` (short probe), the session lands on https and the cookie is `secure`. An empty `web.base.url` aborts before Chrome launches.
- Session cache (`connect_cache.go`, `config/connect_session.go`): `~/.config/echo/connect-sessions/<key>.toml` (dir 0700, file 0600), one entry per (target, login), key = `ProjectKey` of `local:<root>:<db>` or `ssh:<host>:<path>:<db>`. It stores the live `session_id` in plaintext. Reuse path: TTL 5 days (`connectSessionTTL`, below Odoo's ~7-day session GC; only a pre-filter) then always probe `GET <base>/odoo` with the cookie and **no redirect-follow** (2xx = valid, 303 to login = dead, 5 s timeout). `--fresh` forces a re-mint. A cache read/write failure never fails the command.
- Log vocabulary: `echo.connect`, `echo.connect.cache`, `echo.connect.mint`, narrated through `ConnectOpts.Log` (nil is a no-op for non-REPL callers).

## Reverb mode and link mode

Reverb is a separate platform that provisions Odoo environments and owns their lifecycle through an HTTP API. Echo drives a Reverb environment as one more connect target; Reverb publishes the information Echo needs in the environment's server profile.

### How a Reverb target is recognized (link mode)

- The server profile of a Reverb environment carries a `[reverb]` marker table (`env_id`, `project`, `env`, `api_url`), written by Reverb's own unit 30 (`config.ReverbMarker`). Reverb unit 30 is deployed and was accepted live on `iza/staging` (2026-09-09).
- `reverbEnvFromProfile` builds the Reverb context only when **marker and credentials both exist**: a daemon URL (the marker's `api_url`, else local `[reverb] url`) **and** local `[reverb] token`. With a marker but no token or URL, `resolveRemoteShell` logs one INFO naming what is missing and everything runs classically.
- That is the invariant that keeps every `rsc.reverb != nil` branch safe: wherever the field is set, the API is reachable, because those branches are API calls.
- The marker's `api_url` wins over the local `[reverb] url` (the host can move its daemon without every laptop changing). The token is always local; the marker is a file on a shared host and carries no secrets. `[reverb]` config is global-only; the token is a credential, never logged, never copied into a project profile (`SaveProject`), and global writers must preserve it (see [architecture-and-traps](architecture-and-traps.md)).
- The marker is read from the **project** profile only: it is a property of the environment, not of the host.

### What changes automatically when the context is set

- `checkpoint` create/list use Reverb snapshots; Echo keeps no local checkpoint store for it (a parallel store would shadow the snapshots). `checkpoint rm` stays refused (admin scope). See [deploy-safety](deploy-safety.md).
- `up`, `stop`, `restart` go through the API so Reverb's desired-vs-observed reconciliation sees no drift; `down` maps to `stop` with a note (Reverb has no compose-style `down`). `ps` and `logs` stay SSH (`runReverbEnvAction`, `reverbActionFor`).
- `push` defaults to the environment **overlay** (the profile's `[push] path`); `push --clean` empties it. Reverb replaces `addons` wholesale on every deploy and never touches the overlay, and a module in the overlay shadows the git copy. A shadow warning asks the server (`GET /environments/{id}/overlay`) because the comparison is against the deployed git tree, which the client does not have; failure degrades to no warning (`push_reverb.go`). Semantics of push and overlay: [deploy](deploy.md).
- **In link mode `paths.Addons` is empty**, so `isUnderAddons` cannot check and the refusal of a `--dest` under addons does not fire (it only protects the deprecated `-E` path, where the resolve payload names the addons dir).
- `update <mods> --remote` is delegated: `POST /environments/{id}/update` (`modules`, `snapshot`), then the job's events are streamed. Reverb stops Odoo, resets the signaling sequences, takes a `pre_update` checkpoint and rolls back on failure, none of which a `compose exec ... -u` beside the live Odoo does. `--all` is rejected with Reverb's reason; `--i18n` stays classic (the job has no i18n switch and silently changing its meaning is worse than not delegating); `--no-checkpoint` becomes `snapshot:false` (Unit 123, `runUpdateRemote`).
- DB exec transport switches to `docker` (previous section).
- `deploy --remote` and `watch` are not delegated: in link mode they are the rsync-to-overlay loop, which is what a Reverb environment wants for uncommitted work. `reverbDeferred` (`reverb.go`) only applies to the `env:` reference, i.e. only to `-E`.

### Known gaps

- **Unresolved:** E8, a delegated `update` takes its verdict from Echo's stream scanner, which counts `ERROR` lines of the Odoo output (live acceptance 2026-09-10: a succeeded job closed as `update failed errors=9` because of pre-existing errors in that database). `runUpdateReverb` returns nil on job success but the scanner still sees the streamed lines; the job status should be the verdict (`succeeded` -> exit 0 with counts as information, `failed` -> exit 1 with the job error). Settle by treating the job status as authoritative for delegated runs.
- Open and unbuilt: E5 `link --reverb` (find the environment of the current branch, register the target, bind), E6 `refresh --remote`. There is no `--reverb` flag and no `refresh` command.
- Link-mode snapshots/lifecycle through the marker path and the registration-over-real-`global.toml` case were not recorded as verified live; pending items are in [operations](operations.md).

### `-E` (deprecated, removal is Unit 122)

- `-E <project>/<env>` / `--env` is sugar for `--from env:<spec>` (`reverbRefPrefix`); `env:` is a reserved prefix of the target namespace, which is how the existing single `from` string carried Reverb through every call site without signature changes. `resolveReverbShell` resolves over HTTP at call time and builds the target in memory, persisting nothing.
- It now emits a WARNING pointing at link mode. Unit 122 deletes it and is **unblocked** (Reverb unit 30 is live). Removal scope: the `-E`/`--env` parsing in `remoteFlagsIn` and the per-command parsers (including the value consumers added in Units 107/116 and `i18n_pull.go`, `test_remote.go`, `update_remote.go`), the `env:` prefix helpers, `resolveReverbShell`, `reverbDeferred`/`requireNoReverb` and their calls, the `[reverb] compose_cmd` and `ssh_host` overrides (one release as no-ops with a deprecation line), the help rows. The `internal/reverb` HTTP client stays; link mode uses it. After removal `-E` must read as an unknown flag.
- Facts that only matter until then: `[reverb] ssh_host` overrides the payload host and `ssh_port` is read only to warn (never as `ssh -p`); a payload without `ssh_host` is an error naming `REVERB_PUBLIC_HOST`, with no fallback host; a `409 not_ready` environment is waited on by following its provisioning job and re-resolving after `reverbSettleWait` (20 s), because "job finished" and "resolve says ready" are two observations (`resolveWaiting`).
- API token scope `echo` reaches snapshot create, lifecycle, deploy and update; not snapshot restore/delete, destroy, create/fork/rebuild (spec 108).
- **A simulator proves nothing about the contract.** The first live run of `-E` exposed the SSH identity, the `not_ready` window and the overlay shadow that a simulator (answers on any port, authorizes anything) had hidden (Unit 108). Check Reverb behaviors against the real host.
