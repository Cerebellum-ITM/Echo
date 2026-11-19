# REPLY · relay-dev-envs
> Partial answer to [REQUEST-echo-relay-dev-envs.md](REQUEST-echo-relay-dev-envs.md), from the Echo session. Written 2026-11-19.

## Done

- **Reads and pre-flight work on a Relay env.** `probe` and `doctor` run there (Unit 151), so an agent reads
  through a `READ ONLY` transaction and learns the gate stage of a target it is not bound to.
- **A dev env needs no registered target.** `--from relay:<app>/<env>` reaches any Relay env and saves nothing; the
  receiver's SSH alias comes from `[relay.ssh_hosts]` in `global.toml`, keyed by the server name of the resolve
  (Unit 152). A receiver on a port other than 22 without an alias is an error that prints the fix.
- **The agent workflow is written** in the `odoo-probe` skill, section "Relay envs" (fork, `relay dev push`,
  `test`, `probe`, `logs --errors`, commit, destroy; which of `update` and `dev push --upgrade` to use; what Echo
  refuses there). It is in the working tree of `~/Documents/Projects/odoo-probe`, not committed.
- Against "Done looks like": item 3 (an agent that reads only `odoo-probe`) is written but not exercised on a live
  dev env; items 1, 2 and 4 are open (below).

## Commits

- `cf8f46c` [ADD] relay: let agents probe and diagnose Relay envs (Echo, `feature/relay-agent-workflow`, not pushed)
- `37b8ff5` [ADD] relay: reach Relay envs without a saved target (Echo, `feature/relay-agent-workflow`, not pushed)

## Deviations from the request

- **No live run on a dev env.** It means pushing a throwaway branch to the client's repository and forking
  `habitta/base`; the owner has not given that go in the Echo session. Everything live was a read: `doctor`,
  `probe` and `ps` on `fb/stage`, and `doctor` on `habitta/base`.
- **No follow-the-branch target.** The ad-hoc reference covers it: the agent takes the env's name from
  `relay dev fork` or `relay dev list --json`.
- **`deploy` on a dirty dev env is not guarded** (asked of Relay, below).

## Findings to harvest

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

- The live run on a dev env with every supported command, write commands included, and the check that none touches
  the base's database (`uninstall`, `i18n-update`, `i18n-pull` run `dbExec` in the base's Postgres container with
  the name the resolve gives). Echo session, on the owner's go.
- A release of Echo carrying Units 151 and 152; until then the installed `echo_cli` 0.30.0 refuses `probe` and
  `doctor` on a Relay env and does not know `relay:`.
- The owner adds the `ionos` alias to `[relay.ssh_hosts]`.
- Unit 153, after Relay extends the resolve.
