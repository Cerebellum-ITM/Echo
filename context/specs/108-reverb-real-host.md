# Unit 108: Reverb mode against a real host — SSH identity, retry window, server-side ops

## Goal

Unit 107 was verified only against a simulated Reverb. Running the real
acceptance check (`scripts/echo-contract-check.sh` in the Reverb repo)
against the dev host exposed three defects a simulator cannot express,
and the contract grew the fields needed to fix them. This unit makes
`-E` actually usable against a live Reverb, and un-defers the two
sections that were blocked by token scope rather than by design.

## Contract changes this unit consumes

`GET /api/v1/resolve/{project}/{env}` now also returns:

```json
"id": 48,
"ssh_port": 1024,
```

`GET /api/v1/envs` rows now carry `"id"` too.

`id` is the key: every route past resolve is addressed by it
(`/environments/{id}/deploy|snapshots|overlay|files`). Without it a
client holding `<project>/<env>` can read but not act — which is why
§6/§7 could not be written in Unit 107.

The `echo` scope now reaches snapshots (create), the lifecycle verbs and
deploy. It still does **not** reach snapshot restore/delete, env
destroy, project/env creation, fork or rebuild — those stay admin and
must keep being refused.

## Design

### 1. SSH identity: one mechanism for both modes

**The problem.** Echo has never had a port field, in any mode. It passes
the host verbatim to `ssh`/`rsync` and lets the user's `~/.ssh/config`
resolve port, user, key and ProxyJump:

```bash
ssh -o BatchMode=yes <sshHost> "cd <remotePath> && docker compose exec -T …"
rsync -az -e "ssh -o BatchMode=yes" ./mod/ <sshHost>:<dest>/mod/
```

That works in classic mode because `ssh_host` is a name **the user
wrote** — an alias like `muutrade` that matches their `Host muutrade`
block. In Reverb mode `ssh_host` is written by the daemon
(`public_host`), and it is a literal `deploy@10.0.0.5`. `ssh` matches
`Host` blocks against the token as typed, so a literal `user@ip` matches
none of the user's blocks: the port is lost (→ tries 22 → "connection
refused", which reads as a dead host), and so are the identity and any
ProxyJump.

**The decision: a local override, not a port field.** Echo gains
`[reverb] ssh_host` — an alias for this Reverb's host — which **wins
over the payload's `ssh_host`**:

```toml
[reverb]
url      = "http://127.0.0.1:8484"
token    = "rvb_…"
ssh_host = "reverb-dev"      # a Host block in ~/.ssh/config
```

Rejected alternative: reading `ssh_port` and emitting `ssh -p N`. It
introduces a **second SSH-configuration channel** that only ever
resolves the least useful third of the problem. The payload gives host
and port but, by explicit contract design, **never the identity**
("Reverb ships no keys"). So any user with several keys in the agent or
a ProxyJump in front still has to write the `~/.ssh/config` block — at
which point the port plumbing is dead weight and the two mechanisms
disagree about what "how to reach this host" means. One alias covers
port, key and jump at once, and makes both modes configured identically.

The cost is that Reverb mode is no longer zero-setup: it needs one
ssh_config block **per Reverb host** — not per environment, so the
contract's actual promise ("zero per-environment config") is untouched.

**`ssh_port` is still read — as a diagnostic only, never as argv.** When
no override is set, the resolved target keeps the payload's host, and
the resolution logs a WARNING naming the gap, so the failure is legible
instead of looking like a dead host:

```
echo.<cmd>.reverb: the daemon reports SSH on port 1024 for deploy@10.0.0.5 —
  a literal user@host matches no Host block in ~/.ssh/config, so the port,
  key and ProxyJump it carries are not applied. Add a Host block for it, or
  set [reverb] ssh_host to your own alias.
```

This is a deliberate deviation from the Reverb-side brief, which asked
for `ssh -p N` plumbing. It must be reported back.

### 2. `not_ready` retry window: follow the job

Unit 107 retried 3× every 2s = 6 seconds. A real `env_create` takes
60–90s, so targeting a freshly created environment always failed.

Instead of a blind backoff, follow the provisioning job — the payload
that would let us wait intelligently is one call away:

1. `409 not_ready` → look up the environment id from `GET /api/v1/envs`
   (the row exists while provisioning; it now carries `id`).
2. `GET /api/v1/jobs?environment_id=<id>` → find the queued/running
   `env_create` / `env_fork`.
3. Poll `GET /api/v1/jobs/{id}` and stream its **new** events as INFO
   lines (the response carries `{job:{status,…}, events:[{seq,level,message,ts}]}`;
   track the last `seq` so each event prints once).
4. When the job leaves queued/running, re-resolve. `failed`/`canceled`
   → a clear error naming the job and its error.

Fallback when the job cannot be identified (no id, listing failed, no
matching job): a plain backoff bounded at **~2 minutes** rather than 6
seconds, so the environment has time to come up either way.

### 3. Overlay shadow comes from the server

Unit 107 computed the shadow report client-side, `ssh test -d
<addons>/<mod>` per module. Wrong on two counts: it is N SSH round-trips,
and the interesting comparison is against the **deployed git tree**,
which the client does not have.

After a Reverb push, ask instead:

```
GET /api/v1/environments/{id}/overlay
→ {"modules":[…], "shadowed":[…], "size_bytes": N}
```

`shadowed` is the subset also present in the git checkout — the modules
Odoo resolves to the overlay copy. The warning names them and says the
running code is the overlay's, not the deployed one. A failure of this
call degrades to no warning (the push already succeeded; a missing
advisory must not fail the command).

### 4. Ambiguous `-E <env>` already lists candidates

`reverb.FindEnv` (Unit 107) already errors with every `<project>/<env>`
candidate named. Verified, kept, and the test tightened — no change
beyond decoding the new `id` field on the listing rows.

### 5. Un-defer §6 (snapshots) and §7 (lifecycle)

Both were refused in Unit 107 because an `echo` token could not reach
them. It can now.

**`checkpoint` in Reverb mode maps to snapshots** — and keeps **no**
local checkpoint store for these targets (a parallel store would
duplicate state and confuse rollback):

- `checkpoint list` → `GET /api/v1/environments/{id}/snapshots` →
  `{"snapshots":[{id,kind,note,size_bytes,created_at}],"total_size_bytes":N}`,
  rendered through the existing table (`name`←`id`, `method`←`kind`,
  size, age from `created_at`).
- `checkpoint create` → `POST /api/v1/environments/{id}/snapshots`
  `{"note":…}` → `202 {"job_id":…}` → follow the job (same follower as
  §2) so the user sees progress rather than silence.
- `checkpoint rm` → **refused**: deleting a snapshot is admin-scoped by
  contract. The error says so and points at the Reverb UI.

**Lifecycle verbs go through the API**, because Reverb reconciles
desired vs observed state and a compose command run behind its back
shows up as drift:

- `up` → `POST /api/v1/projects/{p}/envs/{e}/start`
- `stop`, `down` → `…/stop`
- `restart` → `…/restart`

All return `202 {"job_id":…}` and are followed like any other job.
`down` maps to `stop` with an explicit log line: Reverb models a desired
state, so there is no compose-style "down"; the containers stay defined
and stopped.

`ps` and `logs` keep using SSH from `paths.compose_dir` — read-only, and
the doc says so.

Still refused, with the reason named: `deploy` and `watch` (§5/§8 — they
need the push-to-Reverb-remote design, Unit 109), `checkpoint rm`,
and anything admin-scoped.

## Implementation

### `internal/reverb`

- `Env` gains `ID int64` (`json:"id"`) and `SSHPort int` (`json:"ssh_port"`);
  `EnvRef` gains `ID int64`.
- New types + calls:
  - `Overlay{Modules, Shadowed []string, SizeBytes int64}` ·
    `GetOverlay(ctx, envID)`
  - `Snapshot{ID, Kind string, Note *string, SizeBytes *int64, CreatedAt string}` ·
    `ListSnapshots(ctx, envID) ([]Snapshot, int64, error)` ·
    `CreateSnapshot(ctx, envID, note) (jobID string, err error)`
  - `EnvAction(ctx, project, env, action) (jobID string, err error)` for
    `start|stop|restart`
  - `Job{ID, Kind, Status string, Error *string}` ·
    `JobEvent{Seq int, Level, Message, TS string}` ·
    `GetJob(ctx, id) (Job, []JobEvent, error)` ·
    `ListJobs(ctx, envID) ([]Job, error)`
  - `FollowJob(ctx, id, onEvent, poll)` — polls until terminal, emitting
    each event once by `seq`.
- `post(ctx, path, body, out)` alongside the existing `get`, sharing the
  auth header and error mapping. The token stays out of every error.

### `internal/config`

- `reverbConfig` gains `ssh_host` → `Config.ReverbSSHHost`.

### `internal/cmd/reverb.go`

- `reverbEnv` gains `id int64`.
- `resolveReverbShell`: apply the override
  (`cfg.ReverbSSHHost` wins over `re.SSHHost`); when it is absent and the
  payload carries a non-22 `ssh_port`, emit the diagnostic WARNING.
- 409 handling moves from `ResolveWithRetry`'s blind loop to
  `waitForEnvironment` (job following + bounded fallback).
- `requireNoReverb` shrinks to `deploy`, `watch`, `i18n-pull`; the
  lifecycle and checkpoint entries move to the new implementations, with
  `checkpoint rm` refused separately.

### `internal/cmd/push_reverb.go`

- `warnOverlayShadow` calls `GetOverlay` instead of probing over SSH.

### `internal/cmd/checkpoint.go` / new `checkpoint_reverb.go`

- `RunCheckpoint` branches to the Reverb implementation when
  `rsc.reverb != nil`, before the local store is touched.

### `internal/cmd/docker_remote.go`

- `runRemoteUp/Stop/Restart` branch to `EnvAction` for Reverb targets;
  `RunDown` routes to the stop action instead of refusing.

### Docs

- README: the `[reverb] ssh_host` block, why it exists, and the tunnel
  note for `url` (`reverbd` binds `127.0.0.1:8484` and has no public
  route yet — `ssh -L 8484:127.0.0.1:8484 <host>`).
- Help rows + CHANGELOG.

## Verify when done

- [ ] `[reverb] ssh_host` wins over the payload's host in every ssh and
      rsync invocation; absent, the payload's host is used unchanged.
- [ ] With no override and `ssh_port != 22`, the resolution warns naming
      the port, `~/.ssh/config` and the override — and still proceeds.
- [ ] No `-p` is ever added to an ssh/rsync argv, and none to
      `docker compose` (the compose file embeds its own project name).
- [ ] A `409 not_ready` follows the provisioning job, streams its events,
      and resolves once it succeeds; an unidentifiable job falls back to
      a ~2-minute bounded wait; a failed job errors naming it.
- [ ] The post-push shadow warning comes from `GET /overlay` and names
      the modules; a failed call degrades to no warning, not an error.
- [ ] `checkpoint list` renders the environment's snapshots; `create`
      enqueues one and follows the job; `rm` is refused as admin-scoped.
- [ ] No local checkpoint entry is written for a Reverb target.
- [ ] `up`/`down`/`stop`/`restart` enqueue the lifecycle action and
      follow the job; `down` says it maps to stop.
- [ ] `deploy` and `watch` still refuse, naming the reason.
- [ ] Classic targets are byte-identical to today (`rsc.reverb == nil`).
- [ ] The token appears in no log line or error.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
- [ ] **The real acceptance check passes 36/36** against a live Reverb —
      this is the gate the unit exists for; a simulated server does not
      count:
      `REVERB_API=… ECHO_CHECK_KEY=… bash scripts/echo-contract-check.sh <p> <e>`
      then the same flow replayed with Echo's own commands.
