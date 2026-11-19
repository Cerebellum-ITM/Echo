# REPLY · relay-dev-envs
> Answer to [REQUEST-echo-relay-dev-envs.md](REQUEST-echo-relay-dev-envs.md), from the Echo session. Written 2026-11-19.

## Done

- **Reads and pre-flight work on a Relay env.** `probe` and `doctor` run there (Unit 151), so an agent reads
  through a `READ ONLY` transaction and learns the gate stage of a target it is not bound to.
- **A dev env needs no registered target.** `--from relay:<app>/<env>` reaches any Relay env and saves nothing; the
  receiver's SSH alias comes from `[relay.ssh_hosts]` in `global.toml`, keyed by the server name of the resolve
  (Unit 152). A receiver on a port other than 22 without an alias is an error that prints the fix.
- **The agent workflow is written** in the `odoo-probe` skill, section "Relay envs" (fork, `relay dev push`,
  `test`, `probe`, `logs --errors`, commit, destroy; which of `update` and `dev push --upgrade` to use; what Echo
  refuses there). It is in the working tree of `~/Documents/Projects/odoo-probe`, not committed.
- **Every supported command ran on a live dev env** (`habitta/echotest`, forked from `habitta/base` from a
  throwaway branch and module, destroyed afterwards with nothing left on Ionos), and none touched the base's
  `habitta` database ([ledger](../knowledge/operations.md#live-verification-ledger), row "Relay dev env").
- **Two defects found by that run are fixed, each with a regression test:** `i18n-update` loaded nothing on Odoo
  below 19 and reported success (any target, not only Relay), and `uninstall --from` refused to run outside a
  compose project.
- Against "Done looks like": items 1 to 4 are met, except the interactive `shell` (needs a TTY) and a real
  `deploy` on a dirty dev env.

## Commits

- `cf8f46c` [ADD] relay: let agents probe and diagnose Relay envs (Echo, `feature/relay-agent-workflow`, not pushed)
- `37b8ff5` [ADD] relay: reach Relay envs without a saved target (Echo, `feature/relay-agent-workflow`, not pushed)
- `ba9a093` [FIX] i18n: load the .po on Odoo below 19 (same branch)
- The `uninstall` fix follows it on the same branch; see `git log`.

## Deviations from the request

- **No follow-the-branch target.** The ad-hoc reference covers it: the agent takes the env's name from
  `relay dev fork` or `relay dev list --json`.
- **`deploy` on a dirty dev env is not guarded** (asked of Relay, below).

## Findings to harvest

- What still differs from a classic Echo dev target, and why: no `push`/`watch`/`sideload` (code is baked into the
  image; uncommitted work goes through `relay dev push`), no checkpoints or lifecycle verbs (Relay snapshots and
  owns the containers), no `db-*`, `view`, `compare`, `modstate`, `modinfo` (they need a project directory on the
  host); `ps` lists only the dev env's Odoo container because its database is served by the base's Postgres
  container; `deploy` is Relay's and discards the layer; `update` takes no snapshot, `relay dev push --upgrade`
  does.
- `relay dev push` finds the app through the branch's remote: in a checkout whose `origin` is another repository
  it answers `no app of relayd has this working tree's repo` until the branch's upstream is the app's remote
  (seen in `ccima/all_odoo`, whose `origin` is `all_odoo` and whose `habitta` remote is the app's).
- `i18n-update` copies the `.po` to `/tmp` in the Odoo container and removes it; that is the only file Echo writes
  in a Relay container, and it is not under an addons path.
- The resolve reports Ionos as server `ionos`, port 1024. Echo reaches it once `global.toml` has
  `[relay.ssh_hosts]` with `ionos = "Ionos-personal-pascual"`; without it every command on a habitta env exits 2
  with that line (measured 2026-11-19 with an isolated config; the owner's `global.toml` does not have the entry
  yet).
- A dev env resolves `stage = "staging"` (Relay `internal/catalog/devenvs.go`, `DevEnvOf`), so Echo's prod gates
  never fire on one and nothing tells it apart from `fb/stage`.
- `compare --targets` read a deploy lock at a `relay:` path on a Relay target; it now refuses (Unit 152).
  Build-mode `i18n-pull` has the same gap and is a row in the plan's small fixes.
- `doctor` on `fb/stage` and `habitta/base` returned `relay`, `ssh`, `containers` ok
  ([ledger](../knowledge/operations.md#live-verification-ledger), rows 151 and 152).

## Asked of Relay

- **Resolve payload:** add `role` (`dev` / `base` / empty) and `dev_of`, and for a dev env the layer state
  (`dirty` plus the file count). Echo needs them to warn or refuse before a delegated `deploy` discards a layer
  (planned as Unit 153, blocked on this), and the `odoo-probe` skill needs `role` to apply its own-dev-env
  permission rule from Echo's output instead of from `relay dev list`.
- **Relay's skill**, section "Echo and Relay", replacement text:

  > `echo_cli` (Echo) operates a Relay env without deploying it: reads with `probe`, `logs` and `doctor`, and in
  > write mode `shell-run`, `test`, `update`, `uninstall` and `i18n-pull`, all through `docker exec` in the env's
  > containers. Name the env with `--from relay:<app>/<env>` (nothing to register) or a connect target with
  > `relay = "<app>/<env>"`. Code never travels through Echo: use `relay dev push` or a commit. The workflow for an
  > agent on a dev env is the "Relay envs" section of the `odoo-probe` skill; this skill covers Relay itself.

## Still open

- A release of Echo carrying Units 151 and 152; until then the installed `echo_cli` 0.30.0 refuses `probe` and
  `doctor` on a Relay env and does not know `relay:`.
- The owner adds the `ionos` alias to `[relay.ssh_hosts]`.
- Unit 153, after Relay extends the resolve.
