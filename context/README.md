# Echo context

Echo (`echo_cli`) is a Go CLI that operates Odoo instances: locally through docker compose and on remote
targets over SSH, with deploy, push, watch, checkpoints, rollback and a Reverb link mode. This store holds what
the code does not say quickly: boundaries and traps, the why behind the design, where the work stands and what
is still open.

Storage: `repo`, versioned in `Echo/context/`. Changes are committed through commitcraft when the user asks.

## Start here (in this order, then stop)

1. [STATE.md](STATE.md): where the work stands, blockers, next, open questions.
2. [knowledge/architecture-and-traps.md](knowledge/architecture-and-traps.md): packages, invariants, startup,
   storage and the traps every other file assumes.
3. Only then, the file for the task at hand (map below). Do not read `archive/`.

## Map

| Need | File |
|---|---|
| What Echo is, the link-mode workflow, scope | [knowledge/product.md](knowledge/product.md) |
| Adding a command, error and exit conventions, test seams, done gate | [knowledge/code-standards.md](knowledge/code-standards.md) |
| Theme, Odoo-style log rendering, pickers, prompt, banner, `--json` stdout | [knowledge/ui.md](knowledge/ui.md) |
| Connect targets, link, server profile, SSH transport, Reverb mode | [knowledge/remote-targets.md](knowledge/remote-targets.md) |
| Deploy flow and step order, selection, module source, push, git-deploy, watch, promote | [knowledge/deploy.md](knowledge/deploy.md) |
| Checkpoints, rollback (DB and code), code snapshot, deploy lock, deploy actions | [knowledge/deploy-safety.md](knowledge/deploy-safety.md) |
| Install/update/test, addons discovery, i18n, lint, modinfo/compare/view | [knowledge/modules-and-odoo.md](knowledge/modules-and-odoo.md) |
| db commands, backup/restore, filestore, neutralize, db-pull, db-admin | [knowledge/database.md](knowledge/database.md) |
| Script mode, recipes, sequence, build mode, report, cmd logs and logview | [knowledge/scripting.md](knowledge/scripting.md) |
| Releases and versioning, how units are specced, live-verification ledger | [knowledge/operations.md](knowledge/operations.md) |
| Why something is the way it is | [decisions.md](decisions.md) |
| Open work | [work/build/plan.md](work/build/plan.md) |
| Requests between sessions or repos | [messages/](messages/) |
| Published artifacts | [deliverables/published.md](deliverables/published.md) |

## How to keep this useful

- **English only.** Domain identifiers stay verbatim. Messages from other sessions are kept as written.
- **Current truth, not history.** Correct a wrong fact where it lives; never append "Correction:" or "Update:".
- **Source or Hypothesis.** Every non-obvious claim cites a commit, `internal/path.go:line`, a unit, or "measured
  in <env> on <date>". Unverified claims start with **Hypothesis:**; open conflicts with **Unresolved:**.
- **One owner per fact.** Link instead of repeating. **Nothing the code already says. No secrets.** Technology
  facts true in any project go to the `odoo-dev` / `odoo-probe` skills, not here.
- **Where things go:** durable fact → `knowledge/`; decision → `decisions.md`; open work → a row in
  `work/build/plan.md` and, before any code, a spec `work/build/NN-name.md` (`/ctx spec build NN-name`); request
  to another session or repo → `messages/`; finished unit → harvest its facts and decisions, mark it done in the
  plan, move the spec to `archive/`.
- **Every meaningful change also updates `CHANGELOG.md` `[Unreleased]`**, and a version bump promotes it in the
  same commit ([operations](knowledge/operations.md#release-and-versioning)).
- **Close every session with `/ctx close`**: it rewrites [STATE.md](STATE.md) and harvests what was learned.
