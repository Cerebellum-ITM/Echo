# Unit 128 · partial-deploy-dependency-check
> Spec for one unit of [the plan](../../work/build/plan.md). Implement exactly this: no more, no less. Last verified: 2026-09-30.

## Goal

Before a deploy ships code, Echo warns when a module it ships drops a Python method, a field or an XML id that a
module staying on the server still uses. This is the failure behind the 2026-09-30 incident:
`ccima_crm_reassign` shipped without `_get_promotion_from_sale_order` while the `ccima_flow_mail` left on the
server still called it ([deploy-safety](../../knowledge/deploy-safety.md)).

## Design

### What is compared

For every module the run ships **and that already exists on the server** (a module being installed has no old
version, so it removes nothing):

- **Old** = the module directory on the server now, at the same destination the push writes
  (`moduleDestDir`; on a git-deploy target, the module directory inside the checkout).
- **New** = the tree that ships: the archive dir for `commit`/`ref` sources, the working tree for `worktree`
  sources, and for modules riding the git deploy branch the tree at `gitTip` (archived the same way).

From both, Echo extracts three kinds of symbols, by regex, ignoring `tests/` and `migrations/`:

| Kind | Python / XML pattern | Referenced in staying modules as |
|---|---|---|
| method | `def <name>(` inside a class | the word `<name>` in `.py` and `.xml` files |
| field | `<name> = fields.<Type>(` | the word `<name>` in `.py` and `.xml` files |
| xmlid | `id="<id>"` on `record`, `template`, `menuitem`, `act_window`, `report` | `<module>.<id>` in `.py`/`.xml` (unqualified local refs are not checked) |

**Removed** = symbols present in old and absent from new, minus:

- names still defined in the new tree of any other module shipped in the same run (moved, not removed);
- dunder names and the ORM API names a module commonly overrides, which other modules call on the base model
  anyway: `create`, `write`, `unlink`, `copy`, `read`, `search`, `search_read`, `search_count`, `browse`,
  `exists`, `name_get`, `name_search`, `default_get`, `fields_get`, `onchange`, `init`, `_auto_init`,
  `_compute_display_name`, `_name_search`, `_search`, `action_archive`, `action_unarchive`, `toggle_active`.

### Where it looks for uses

**Staying modules** = the module directories next to each shipped module's destination on the server (siblings
under the same addons dir or build context) that are not shipped in this run. A staying module that defines the
removed method or field itself is skipped for that symbol (it provides it).

One SSH round trip does the search: `grep -rnwE` over the staying modules' `*.py` and `*.xml` for the removed
names (and the qualified xml ids), with output capped per symbol. A second round trip fetches the old trees of
the shipped modules as one tar (`*.py`, `*.xml`, nothing else).

### What the operator sees

In the plan, for both `--dry-run` and real runs, right after the `code` lines:

```
WARNING echo.deploy.plan: dependency removed=_get_promotion_from_sale_order kind=method from=ccima_crm_reassign used_by=ccima_flow_mail at=ccima_flow_mail/models/mail_flow.py:212
INFO    echo.deploy.plan: dependency check clean modules=4 removed=0
```

- At most five `at=` locations per symbol; the rest are counted (`more=N`).
- `--json`: `DeployResult.Dependencies` lists `{module, symbol, kind, used_by, file, line}`.

### What it blocks

- **dev**: warnings only.
- **staging / prod** (and an undeclared stage, see Unit 127): a real run with findings asks in a TTY
  (`Deploy anyway? N symbols removed while still used`); without a TTY it fails closed (exit 2) unless
  `--force`. `--dry-run` never blocks.
- `--no-dep-check` skips the check for one run and logs that it did, like `--no-lint`. It is not a config key,
  for the same reason as `--no-lint`.
- `watch` passes `--force`, so it is never blocked; its cycles still log the warnings.

### Limits (documented, not solved)

- Regex, not a Python parser: no class awareness (a method removed from one model but defined on another of the
  same module counts as present), no signature changes, no JS/OWL, no QWeb `t-call` of a removed template by
  short name.
- A word match can be a comment or an unrelated variable: that is why it warns and asks instead of refusing.
- Only modules visible on the server's filesystem are searched; modules baked into an image and not in the
  build context are invisible.

## Implementation

### internal/depcheck (new package, pure)

- `Symbols(root string) (Set, error)`: walks a module dir and returns methods, fields and xml ids.
- `Removed(old, new Set, keep Set) []Symbol`: the difference minus the ORM denylist and `keep` (symbols still
  defined by other shipped modules).
- `GrepPattern(removed []Symbol, module string) string` and `ParseGrep(out string, roots map[string]string)
  []Ref`: build the remote grep and map its `path:line:text` output back to `(module, file, line)`.

### internal/cmd

- `deploy_depcheck.go`: `runDependencyCheck(ctx, rsc, shipped, dests, newRoots) ([]Finding, error)` fetches the
  old trees (one `tar` over `runSSH`, extracted with `extractTar` into a temp dir), runs `depcheck`, lists
  staying modules and runs the grep. A transport failure is a WARNING (`dependency check skipped`), never a
  failed deploy.
- `deploy.go`: parse `--no-dep-check`; call the check after the `code` plan lines, before the prod gate; log
  findings; apply the blocking rule; fill `DeployResult.Dependencies`. Branch-riding modules are archived at
  `gitTip` for the check only.
- `internal/repl`: `--no-dep-check` in `commandFlags["deploy"]` and the help rows.

### Docs

- `knowledge/deploy-safety.md`: the check, its blocking rule and limits; `knowledge/deploy.md#step-order` gains
  the step.
- `README.md` (deploy section) and `CHANGELOG.md` `[Unreleased]` Added.
- `work/build/plan.md`: mark done.

## Dependencies

- Unit 127 (undeclared stage counts as prod for the blocking rule). Units 124-126 (shipped sources, dests).
- No new Go dependency.

## Verify when done

- [ ] The incident, reproduced with the fake `ssh` harness: server has `ccima_crm_reassign` with the method and
      `ccima_flow_mail` calling it; `deploy --modules ccima_crm_reassign@<sha without it> --dry-run` prints the
      WARNING with `used_by=ccima_flow_mail` and the file and line.
- [ ] Same deploy on a staging target without a TTY fails with exit 2 and changes nothing; with `--force` it
      proceeds; on dev it proceeds with the warning.
- [ ] A method moved from one shipped module to another shipped in the same run is not reported.
- [ ] A removed xml id referenced as `<module>.<id>` by a staying module is reported; a removed `create`
      override is not.
- [ ] An installing module (no old version) reports nothing; `--no-dep-check` skips the SSH calls and logs it.
- [ ] `depcheck` has table tests for extraction, the difference, the denylist and grep parsing.
- [ ] `go build ./...`, `go vet ./...` and `go test ./...` pass.
