# REQUEST · relay-dev-envs
> From relay to Echo (`echo_cli`) and the `odoo-probe` skill. Written 2026-11-19. Status: answered by REPLY-echo-relay-dev-envs.md.

## What is asked

Make `echo_cli` work on Relay's **dev envs** as reliably as it does on fb/stage, and leave an agent able to develop
on one without reading Relay's code. Two parts:

1. **Deep review, then fix.** Review Echo's Relay path (`internal/cmd/relay.go`: `resolveRelayShell`,
   `RelayGuard`, `relaySupported`, `runLinkAddRelay`) against what a dev env really is, run every supported
   command on a live dev env, and turn each defect into a regression test plus a fix. A defect found here is part
   of this request.
2. **One documented workflow for agents.** An agent on a topic forks a dev env with Relay, sends uncommitted code
   with `relay dev push`, and tests and reads with Echo. Write it where agents look: the `odoo-probe` skill
   (`~/.claude/skills/odoo-probe/`), and point to it from Echo's `knowledge/remote-targets.md`. Relay's own skill
   (`~/Documents/Projects/relay/skills/relay/SKILL.md`, section "Echo and Relay") gets a line back to it; changing
   that file is a Relay commit, so leave the exact text in the reply if you do not commit there.

## Why

Relay now gives each topic a private dev env forked from a project's base (units 96–105, live on Ionos since
2026-11-18). Agents already use Echo for `shell-run`, `test`, `update` and `logs`, so that is how they will want to
work on a dev env. Nobody has run Echo against one, and several things differ from fb/stage (below).

## What a dev env is (Relay side, verified)

- Forked from the app's base only: `relay dev fork <app> --branch <b> [--name <n>]`. Today only `habitta` has a
  base: `habitta/base` on Ionos, a neutralized copy of habitta/prod (CCIMA), following `18.0`.
- Its own Odoo container `relay-<app>-<name>-odoo`, its own database `<base db>_<name>` (for example
  `habitta_dev_ui`) **inside the base's Postgres container** `relay-habitta-base-db`, its own filestore and layer
  volumes, a loopback port from the base's `[dev] ports` (8200+).
- `GET /api/v1/resolve/{app}/{env}` answers for a dev env with its own Odoo container and the base's db container
  (Relay `internal/api/resolve.go`; unit 101 spec, `context/work/dev-envs/101-dev-env-model.md`).
- Code reaches it two ways: a commit pushed to its branch (Relay builds the image and deploys it), or uncommitted
  files through `relay dev push [--upgrade-changed | --upgrade m1,m2]`, which writes a layer volume mounted ahead of
  the image's addons (`/mnt/relay-layer`), restarts Odoo and optionally upgrades with a snapshot first. While the
  layer holds files the branch lacks, the env is `dirty`.
- A dev env has no relay-config file; it lives in relayd's SQLite and is destroyed when its branch is deleted
  upstream (when clean) or by `relay dev destroy`.
- Measured 2026-11-18/19 with the `relay` CLI only (Relay `context/work/dev-envs/plan.md`, units 102–105): fork in
  36–63 s with the base stopped about 7 s; `dev push --upgrade-changed` changed a data record; dirty, committed,
  cap and branch-close paths all ran. Relay fixes from that run: `4e2198f` (CLI), `8aab296` (relayd, live as
  `e7ef109`).

## Already verified on the Echo side

- `echo_cli` 0.30.0+45e8f4b is installed; it carries Unit 149 (Relay targets) and Unit 150 (remote `uninstall`).
- Relay targets resolve through `[relay] url` (`https://relay.cerebellum.technology`) and an echo-scope token in
  `~/.config/relay/echo-token` (`[relay] token_file`). The connect targets `fb-stage` and `morwi-fb-odoo` in
  `~/.config/echo/global.toml` point at `fb/stage` and are used daily for `shell-run`, `update`, `logs`, `test`, `i18n-pull`
  (`~/.context/fuentebuena/knowledge/operations.md`, "Staging through Relay").
- Supported on a Relay env: `shell`, `shell-run`, `test`, `update`, `uninstall`, `ps`, `logs`, `i18n-pull`,
  `i18n-update`, `link`; `deploy` is delegated to Relay. Everything else (`push`, `watch`, `sideload`,
  checkpoints, lifecycle verbs) is refused by `RelayGuard` on purpose.
- `probe` refuses a Relay env ("a Relay env has none"); reads go through `shell-run` ending in `env.cr.rollback()`.

## To check (hypotheses, not facts)

- **SSH host and port.** Ionos is `74.208.133.46`, port 1024, user `pascual_chavez`; the laptop reaches it only as
  the alias `Ionos-personal-pascual`. The resolve carries host and port but no user, and Echo warns that a bare
  host carries no port (`relay.go:330`). A dev env target must therefore be registered with
  `--ssh-host Ionos-personal-pascual`. Confirm, and make the failure obvious when it is missing.
- **One target per dev env.** Dev envs come and go with their branches; registering a connect target per fork in
  `global.toml` by hand does not scale. Decide whether Echo needs an ad-hoc form (a `--relay <app>/<env>` on
  `--from`, or a target that follows the current branch's dev env the way `relay dev push` finds it) and a way to
  drop targets whose env is gone.
- **Database commands.** The db container is the base's. Anything Echo runs with `dbExec` must pass the dev env's
  database name and never touch the base's `habitta` database. Check every supported command, `uninstall` and
  `i18n-update` included.
- **Stage.** What `stage` the resolve reports for a dev env and whether Echo's stage-gated write rules
  (`odoo-probe` write mode) treat it correctly: a dev env is disposable and the agent's own, unlike fb/stage.
- **`deploy`.** Echo delegates `deploy` to `POST /api/v1/deploys`. On a dev env a deploy discards the layer. Decide
  whether Echo should say so before queuing, or refuse while the env is `dirty`.
- **Layer and Echo.** Echo must not write files into a dev env's container (it would make Relay's `dirty` false
  while the env runs other code). If Echo should send uncommitted code at all, it calls `relay dev push` or
  `PUT /api/v1/envs/{app}/{env}/layer`; never rsync.
- **`update` versus `dev push --upgrade`.** Both upgrade modules; only Relay's takes a snapshot first. Say in the
  workflow which one an agent uses when.

## How to test (live, with the owner's standing rules)

- Fork a dev env of `habitta/base` from a throwaway branch `relay-test/<topic>` pushed to
  `git@github.com:CCIMA-ODOO/addons-habitta.git` (the client's repo; the local checkout is
  `~/Documents/Projects/ccima/all_odoo`, remote `habitta`). Use a git worktree in the scratchpad, never the main
  checkout. A throwaway module (as Relay's run did with `relay_dev_probe`) keeps client modules untouched.
- Register it in Echo, run every supported command, then `relay dev destroy` it and delete the branch. Check on
  Ionos that no `relay-habitta-<name>-*` container or volume and no `habitta_<name>` database is left.
- Read the `relay` skill (`~/.claude/skills/relay/SKILL.md`) before any `relay` command: it states which actions
  need the owner's go. Forking, pushing to, starting, stopping and destroying **your own** dev env for the topic is
  allowed; the base, habitta/prod (CCIMA) and any other env are not.

## Done looks like

- [ ] Every command in `relaySupported` ran on a live dev env with the result recorded in Echo's live-verification
  ledger (`knowledge/operations.md`), and none touched the base's database.
- [ ] Each defect found has a regression test and a fix, released or ready to release.
- [ ] An agent that reads only `odoo-probe` can: fork, register the target, `dev push`, `test`, `shell-run`,
  commit, and destroy, without asking which flag or alias to use.
- [ ] The reply lists what still differs from a classic Echo dev target, and why.

## Do not

- Do not write files into a dev env's container from Echo, and do not add rsync or checkpoints for Relay envs.
- Do not run anything on habitta/prod (CCIMA), navetec/prod, nyamaker/prod or habitta/base itself beyond reads.
- Do not approve, rehearse or schedule Relay deploys: those stay the owner's.
- Do not change Relay's API from this request; if Echo needs something Relay lacks, write it in the reply as a
  request for Relay.
