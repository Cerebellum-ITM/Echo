# Unit 129 · deploy-saved-plan
> Spec for one unit of [the plan](../../work/build/plan.md). Implement exactly this: no more, no less. Last verified: 2026-09-30.

## Goal

A deploy is reviewed once and executed exactly as reviewed. `deploy --dry-run --save-plan plan.json` writes what
the run would do; `deploy --apply plan.json` runs exactly those modules, shas, sources and actions, or refuses
before touching the server when anything changed since (a ref moved, the disk changed, the lock differs). Today
a dry-run and the run after it are two independent resolutions, so a moved branch, an edit on disk or another
machine's push in between silently changes what ships.

## Design

### A. Flags

- `--save-plan <path>` (with `--dry-run`, see Decisions): the dry-run runs as today and writes the plan to
  `<path>`, relative to the cwd (an output the user asked for), temp + rename, mode 0600. An empty selection
  (`--auto` with nothing pending) writes nothing and says so. The picker works as usual.
- `--apply <path>` combines only with `--force`, `--rollback-on-fail`/`--no-rollback-on-fail` (failure handling
  is decided at apply time), `--json`, `--dry-run` (validate and stop) and `--from` (must name the plan's
  target). Any flag that shapes the run (selection, `--at`, push, checkpoint, test, i18n, git, actions, lint,
  dependency check, fetch) is `ErrUsage`: the plan carries it.

### B. The plan file

JSON, `schema: 1`, no secrets (deploy actions by name and digest, never their `run` text; no DB credential):

```json
{"schema": 1, "created_at": "…", "echo_version": "0.25.0+fa3dad6", "by": "<git user.email>", "root": "/abs/root",
 "target": {"name": "habitta_prod", "ssh_host": "habitta", "remote_path": "/srv/habitta", "db": "habitta", "stage": "prod"},
 "run": {"push": true, "git": false, "checkpoint": "db", "test": false, "i18n_overwrite": false,
         "actions": true, "lint": true, "dep_check": true, "fetch": "auto"},
 "commits": ["<full sha>"],
 "modules": [{"name": "ccima_flow_mail", "action": "update", "source": "ref", "ref": "release/1.4", "sha": "…", "tree": "…"},
             {"name": "ccima_crm_reassign", "action": "install", "source": "worktree", "digest": "sha256:…"}],
 "branch_tip": "", "dest": "", "test_modules": [], "deploy_actions": {"names": ["build/post_push/remote"], "digest": "sha256:…"},
 "lock": "sha256:…", "dependencies": [{"module": "…", "symbol": "…", "kind": "method", "used_by": "…"}]}
```

- `run` holds the **effective** decisions of the dry-run (`resolveDeployPush`, `resolveCheckpointMode`, tests,
  `i18nOverwriteDecision`, git active, actions/lint/dependency check on or off, fetch policy), not the typed flags.
- `modules`: the install/update split (`splitInstallUpdate`) and, when pushing, the identity the lock would
  record (`deployShipEntries`, `internal/cmd/deploy_lock.go:267`): `sha` + `tree` for `commit`/`ref`/`branch`,
  `ref` for pins. A `worktree` module gets `digest` instead: sha256 over its directory as rsync ships it (sorted
  paths, executable bit, content, symlink targets; minus the excludes of `rsyncArgs`, `internal/cmd/push.go:378`).
  Its HEAD sha is not compared: a commit that leaves the content alone changes nothing that ships.
- `lock`: sha256 of `.echo/lock.json` as read on the server, or `absent`. Today it is read only when pushing
  (`internal/cmd/deploy.go:1267`); under `--save-plan`/`--apply` it is always read, and an SSH failure reading
  it fails `--save-plan` and refuses `--apply` (a plan that cannot be checked is useless).
- `dependencies`: Unit 128's findings without file or line.

### C. `--apply`

1. Before any SSH: a plan that is not JSON, has an unknown `schema`, another `root` than this project, or a
   `--from` naming another target is `ErrUsage` (exit 2). Another `echo_version` is a WARNING.
2. The plan becomes deploy arguments for the normal `RunDeploy` path: `--commits` with the recorded commits,
   `--modules` with each `worktree` module bare and each `ref` module as `name@ref` (the ref, so a moved ref
   resolves anew), `--from`, and every `run` decision pinned as a per-run flag (`--push`/`--no-push`,
   `--checkpoint=<m>`/`--no-checkpoint`, `--test`/`--no-test`, `--i18n`/`--no-i18n`, `--no-git`, `--no-actions`,
   `--no-lint`, `--no-dep-check`, `--fetch`/`--no-fetch`). A server-side policy change (say, checkpoint mode)
   therefore does not refuse: the reviewed decision runs. No picker opens.
3. That path builds a fresh plan. After the Unit 128 check and before the `p.dryRun` branch
   (`internal/cmd/deploy.go:1548`), where nothing has been written on the server yet
   ([step order](../../knowledge/deploy.md#step-order)), `diffPlans(saved, fresh)` compares target host, path,
   db and stage; the module set with each action, source, `sha`, `tree`, `digest`; `branch_tip`; `dest`
   (`resolvePushDest`); `test_modules`; the actions digest (name, phase, where, exec_path, run); `lock`;
   `dependencies`; and `run` (it only differs when a pin cannot hold, e.g. the target no longer opts into git).
4. Any difference refuses with nothing changed on the server: one line per difference, then the error.

```
ERROR echo.deploy.plan: changed what=ref module=ccima_flow_mail planned=99f2109 now=a1c4e02 ref=release/1.4
ERROR echo.deploy.plan: changed what=lock planned=3f2a9c1 now=b77d0e4
```

   `plan is stale: 2 changes since plan.json was saved at <created_at> — re-plan with deploy --dry-run
   --save-plan`. New sentinel `ErrPlanStale`: exit 1 through `sess.finalize` (no auto-copy, nothing failed to
   copy); `--json` adds `plan_stale: [{what, module, planned, now}]`.
5. No difference: INFO `plan matches age=<d>`; gates and run continue as a normal deploy (Decisions).

### D. Dependency check (Unit 128) and limits

- The check runs on apply unless the plan recorded it off; a different finding set means the staying modules
  changed on the server, so the plan is stale. An identical set still asks, like any deploy (Decisions).
- A hand edit on the server between plan and apply is invisible, as it is to the lock: no disk drift check
  (declined 2026-09-30, [decisions](../../decisions.md#deploy-safety)).
- Any lock write in between refuses, even a `push` of an unrelated module: a partial deploy is judged against
  what stays, which is what that push changed.

## Decisions

The four open points were settled by the user on 2026-09-30 with the recommended options.

- **`--save-plan` requires a path**: a bare `--save-plan` (or one followed by another flag) is `ErrUsage`, exit 2.
  Nothing implicit to clean up. Rejected: a default file under `~/.config/echo/plans/<projectKey>/`;
  `./deploy-plan.json`.
- **`--save-plan` without `--dry-run` is `ErrUsage`**: the plan is the review artifact, and a flag that silently
  stops a deploy or records one surprises. Rejected: implying `--dry-run`; a real run that also writes its plan.
- **`--apply` keeps every gate**: the prod confirm, and Unit 128's confirm on staging/prod when there are
  findings; `--force` skips both, no TTY fails closed. The plan adds a guarantee and removes no gate, and an
  agent can make and apply a plan nobody read. Rejected: no prompts on apply; skipping only the dependency
  confirm when the findings equal the plan's.
- **No plan expiry**: `plan matches` prints the plan's age, in red from one hour on like `deploy --rollback`.
  The comparison gives correctness, time does not. Rejected: refusing over 24 h without `--force`; a
  `[deploy] plan_max_age` key.

## Implementation

### internal/cmd

- `deploy_plan.go` (new): `deployPlan` types; `writePlan`; `loadPlan(path, root)` (step C.1); `planDeployArgs`
  (step C.2); `diffPlans` (pure); `worktreeDigest(dir)`; `actionsDigest`; `ErrPlanStale`.
- `deploy.go`: parse both flags and their exclusivity; `--apply` loads the plan and replaces `opts.Args`.
  `RunDeploy` fills the plan where it already computes each part (selection and sources, `splitInstallUpdate`,
  `deployShipEntries`, `resolveDeployActions`, the Unit 128 check); at the `p.dryRun` branch it writes or
  compares. `DeployResult` gains `Plan` and `PlanStale`.
- `deploy_lock.go`: nothing new; the plan hashes the raw bytes from `fetchDeployLock` and tells an SSH failure
  from an absent file by its `lockState`, both added by Unit 131 ([131](../2026-09-30-131-doctor/131-doctor.md)).

### internal/repl

- Both flags in `commandFlags["deploy"]` and the help rows; build mode does not offer them.
- `runDeploy` routes `ErrPlanStale` through `sess.finalize`; `finishDeployJSON` emits `plan` and `plan_stale`.

### Docs

- `knowledge/deploy.md`: a "Saved plans" section and the comparison in the step order; `deploy-safety.md` links
  it from the lock section. `decisions.md`: the four resolutions. `README.md` (deploy) and `CHANGELOG.md`
  `[Unreleased]` Added. `work/build/plan.md`: mark done.

## Dependencies

- Units 124 (lock), 125 (sources, `mod@ref`, `--at`), 126 (snapshot and rollback, unchanged on apply).
- Unit 127 (the recorded `stage` is normalized) and Unit 128 (findings), both implemented first.
- No new Go dependency.

## Verify when done

- [ ] `deploy --modules a@<branch>,b --dry-run --save-plan p.json` on the fake remote writes `p.json` (0600,
      ref + sha + tree for `a`, digest for `b`, lock digest) and the fake `ssh` log shows no server write.
- [ ] `deploy --apply p.json --force` with nothing changed ships exactly the plan (lock entries carry its shas and
      trees) and keeps its checkpoint decision after the server's `[checkpoint] mode` changed.
- [ ] Each change refuses with exit 1, a `changed what=…` line and no snapshot, rsync, compose or lock write: a
      commit on `<branch>` (`ref`), an edit in `b` (`disk`), a `push` of another module (`lock`), the module
      installed meanwhile (`action`), a changed server action list (`actions`).
- [ ] Not JSON, schema 2, another root, another `--from`, or `--apply` with `--modules` exits 2 before any SSH.
- [ ] `--apply --dry-run` on an unchanged plan logs `plan matches` and stops; on prod without a TTY, `--apply`
      without `--force` exits 2 (Decisions). `--json` carries `plan` and `plan_stale`, one object on stdout.
- [ ] Table tests for `diffPlans` and `worktreeDigest` (excludes, executable bit, symlinks, order).
- [ ] `go build ./...`, `go vet ./...` and `go test ./...` pass; new tests use the existing seams (temp `HOME`,
      fake `ssh` on `PATH` via `newFakeRemote`, `lockRunSSH`, `stdinIsTTY`), never a real server.
