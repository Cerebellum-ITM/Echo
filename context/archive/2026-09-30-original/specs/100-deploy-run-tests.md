# Unit 100: `deploy --test` — run the module tests as part of a deploy

## Goal

Let `deploy` **run module unit tests in the same run**, so one command does
*deploy + test*. Three surfaces:

1. **Per-run**: `deploy --test` runs the tests for this deploy only;
   `deploy --no-test` skips them for this deploy only.
2. **Always-on toggle**: `deploy --test-toggle` flips a persisted `[deploy]
   test` on/off and **prints the resulting value** (`test=on` / `test=off`),
   so you always know how it ended up. Once on, every `deploy` to that target
   runs the tests without re-typing `--test`. Meant for a **linked work
   instance** (the dev/staging instance the repo is linked to).
3. **Persistent module filter**: a saved `[deploy] test_modules` list decides
   *which* modules get tested. Empty → test whatever the deploy deploys
   (automatic). Non-empty → test **exactly those** modules on every deploy, and
   nothing else, until the list is cleared. Managed both headless and via a
   picker (see below).

A failing test **fails the deploy** through deploy's existing verify path: the
modules are not marked deployed, and — when a checkpoint was taken — the DB is
rolled back, exactly like any other failed run.

### Why this exists (the hole it closes)

The `test` command (Unit 11) and `deploy` are separate today: you deploy, then
run `test` as a second manual step. For a linked work instance the natural
workflow is "ship the code and immediately prove it still passes its tests, as
one gesture" — and to have that be the *default* for that instance. `deploy`
already runs an `odoo -i/-u … --stop-after-init` in the remote container and
already has a verify + checkpoint/rollback pipeline; running the tests is just
enabling the test framework on that same run and letting a failure ride the
existing rollback.

Module unit tests do **not** depend on demo data — this is orthogonal to the
`--with-demo` install path; no demo handling is involved.

## Surfaces

### Per-run (transient)

- `--test` — run the tests this deploy, even if the toggle is off.
- `--no-test` — skip the tests this deploy, even if the toggle is on.
  (`--test` and `--no-test` are mutually exclusive.)

### Always-on toggle (persisted, config-only)

- `--test-toggle` — flip `[deploy] test` (off→on, on→off), persist it, log the
  resulting state `test=on|off`, and **exit** (no deploy this invocation),
  mirroring how `--set-push` is a config-only early return. This is the single
  switch the user asked for — no separate on/off variants; the printed final
  value removes the "which state am I in?" ambiguity a blind toggle has.

### Persistent module filter (`[deploy] test_modules`, config-only management)

The filter is a **saved list of module names**. When it is empty, `deploy`
tests the modules it is deploying (its `install ∪ update` set). When it is
non-empty, `deploy` tests exactly those modules on every run — never the others
— until the list is emptied. Each management flag persists the change, prints
the resulting list (`test_modules=a,b,c` or `test_modules=(auto)` when empty),
and exits without deploying:

- `--test-modules` (bare, TTY) — open a **multi-select picker** over the
  available modules with the current filter pre-checked; the saved selection
  becomes the new list. This is the interactive "add & remove in one place".
- `--test-modules=<csv>` — set the whole list headlessly (replaces it).
- `--test-add <csv>` / `--test-add=<csv>` — add module(s) to the list.
- `--test-rm <csv>` / `--test-rm=<csv>` — remove module(s) from the list.
- `--test-clear` — empty the list (back to automatic = test what's deployed).

(Internally a filter of `[a, b]` becomes Odoo `--test-tags /a,/b`; the user
works in module names, not raw tag grammar.)

The management flags are standalone config-only operations (like `--set-push`):
they don't combine with a deploy selection (`--commits`/`--modules`/`--auto`/…)
in the same call.

## Design

### Resolution — `resolveDeployTest(p, prof, cfg) bool`

Whether this run tests, mirroring `resolveDeployPush`:

1. `--no-test` → false
2. `--test` → true
3. `prof.DeployTest != nil` → its value (server declaration wins)
4. `cfg.DeployTest != nil` → its value (local `[deploy] test`)
5. default → false

### Which modules — `resolveTestModules(prof, cfg, deployed)`

- `test_modules` (resolved server-first: `prof.DeployTestModules` over
  `cfg.DeployTestModules`) when non-empty → use it verbatim.
- otherwise → the deploy's own `install ∪ update` set (`deployed`).

### Argv composition

Today:

```go
argv := odoo.WithI18nOverwrite(odoo.InstallUpdate(conn, install, update), overwrite)
```

When tests are on, wrap with a new `odoo.WithTests(cmd, testModules)` helper
(sibling of `WithI18nOverwrite`) that appends, so tests run during the same
`-i/-u` load:

- `--test-enable` (implied by `--test-tags`, emitted for clarity),
- `--test-tags /<mod1>,/<mod2>` over the resolved test-module set,
- `--no-http --http-port=8189` — the defensive HTTP isolation `odoo.Test`
  already uses (the deploy run shares the container with the live server bound
  to 8069),
- `--log-level=test`.

Because deploy already reloads the modules (`-u <mods>`), tests run against the
freshly-loaded code — no separate `--update` step (unlike the standalone
`test` command's fast path). The run's log line gains a `test=<mods>` field so
the executed command is self-describing.

### Verify & rollback (reuse, no new machinery)

- A test failure makes the Odoo process exit non-zero → deploy's existing
  exit-code check fails the deploy.
- `runFailureScanner` gains test-failure markers (`FAILED`, the
  `odoo.tests`/`openerp.tests` failure lines, the `N failed, M error(s)`
  summary) so a run that exits 0 while reporting failed tests still fails.
- On failure, the existing `handleDeployFailure` path runs: rolled-back SHAs
  are never marked deployed. Rollback itself only happens when a checkpoint was
  taken (Unit 89/90 policy) — this unit does **not** force a checkpoint on just
  because tests are enabled; the two stay orthogonal. (On a dev linked instance
  with checkpoints off, a failed-test deploy still fails and leaves the modules
  unmarked, it just doesn't auto-rollback unless checkpoints are enabled.)

### Prod guard

The suite belongs on the work instance, not prod. On `stage == "prod"`,
tests-on (per-run `--test` or the resolved always-on) requires `--force`, like
deploy's existing prod handling; without it deploy stops with a message
pointing at `--force`. The always-on config targets a dev/staging linked
instance, so this rarely fires in the intended workflow.

### Config plumbing (mirror `[deploy] push` + `[deploy] actions`)

- `deployArgs`: add `test bool`, `noTest bool`, `testToggle bool`,
  `testModulesSet *[]string` (nil = not touched; non-nil, incl. empty = set),
  `testAdd []string`, `testRm []string`, `testClear bool`, `testModulesPick
  bool` (bare `--test-modules`).
- `parseDeployArgs`: parse them; reject `--test`+`--no-test`; the management
  flags follow the `--set-push` standalone shape.
- `RunDeploy`: a config-only early-return block (before resolving the deploy)
  handles `--test-toggle` and every `--test-modules*/--test-add/--test-rm/
  --test-clear` op — persist, log the resulting `test=`/`test_modules=` value,
  return. Mirrors the `--set-push` block.
- `config`: `deployFile.Test *bool` (TOML `[deploy] test`) and
  `deployFile.TestModules []string` (`[deploy] test_modules`); `cfg.DeployTest`
  + `cfg.DeployTestModules`; `RemoteProfile.DeployTest` +
  `RemoteProfile.DeployTestModules`; `mergeDeployTest` (bool, project wins) and
  `mergeDeployTestModules` (wholesale like `mergeDeployActions`: a non-empty
  project list replaces global). The project-config writer that already emits
  `[deploy]` for `push`/`actions` also emits `test` / `test_modules`.
- The picker reuses `runFuzzyPicker`; pre-checking the current filter may need a
  small "initially selected" extension to `runFuzzyPickerCore` (it already
  threads `deployed`/`markable` sets — add a `preselected` in the same shape).

## Out of scope

- No change to the standalone `test` command (Unit 11) — still the tool for
  ad-hoc / iterative runs decoupled from a deploy.
- No raw `--test-tags <spec>` grammar on deploy: the filter is module names
  (mapped to `/mod` tags internally). Arbitrary tag expressions stay in the
  `test` command.
- Coverage / parallelism / structured result parsing beyond pass-fail is out of
  scope; deploy cares only whether the suite passed.

## Tests

- `parseDeployArgs`: `--test`, `--no-test`, their mutual-exclusion,
  `--test-toggle`, `--test-modules`/`--test-modules=a,b`, `--test-add`,
  `--test-rm`, `--test-clear` shapes, and that a management flag rejects being
  combined with a deploy selection.
- `resolveDeployTest`: the precedence table (flag › server › local › default),
  mirroring `resolveDeployPush`'s test.
- `resolveTestModules`: pinned list wins; empty → deployed set.
- `odoo.WithTests`: `/<mod>` tag composition + the `--no-http`/`--http-port`/
  `--log-level=test` isolation flags land.
- `mergeDeployTest` / `mergeDeployTestModules`: project-over-global, nil/empty
  inheritance.
