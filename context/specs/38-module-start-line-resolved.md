# Unit 38: name the resolved modules on the start line

## Goal

Make `update` / `install` / `uninstall` (and `test`) print, at the
**start** of the run, exactly which module(s) are being executed — even
when the set came from the fuzzy picker or `update --last`, where the raw
args don't name anything. Today the start line is emitted before the
modules are resolved, so `update --last` and a picker selection produce a
generic `echo.update.start` and you can't tell what ran until the end.

## Design

`runModules` calls `sess.startLog(name, args)` *before* invoking
`cmd.RunUpdate` ([repl.go](../../internal/repl/repl.go)), and `startLog`
infers the modules from the raw args via `resolvedFromArgs`
([logemit.go](../../internal/repl/logemit.go)). That inference can't see a
picker selection or the `--last` set read from disk — both are resolved
*inside* `RunUpdate`, after the start line is already out.

The fix moves the module-command start line to the moment the final set is
known. The cmd layer gains an `OnResolve(resolved []string)` callback that
each `Run*` invokes with the resolved modules (or the `{"--all"}`
sentinel) **immediately before** the Odoo subprocess runs; the REPL wires
it to emit the start line there. This reuses the exact resolution the
end-of-run line already uses, so start and end now agree.

The start line names the modules two ways, so they're always visible
regardless of how the logger collapses:

- the hierarchical logger — `echo.update.module.<mod>.start` (1 module),
  `echo.update.modules.start` (N>1), `echo.update.all.start` (`--all`);
- a `modules=` field spelling out the full set
  (`moduleField`: `sale`, `sale,account`, `all`).

Examples:

```
… INFO db echo.update.module.sale.start: update modules=sale          # update --last → sale
… INFO db echo.update.modules.start: update modules=sale,account      # picker → 2 mods
… INFO db echo.update.all.start: update modules=all                   # update --all
```

The early generic `startLog` is dropped for module commands (the picker's
alt-screen would wipe a pre-picker line anyway); `modules` (the read-only
listing) keeps its plain `startLog`. When a command errors *before*
resolving (missing DB config, `ErrNoLastUpdate`, picker cancelled), no
start line is emitted — there are no modules to name and the
failure/cancel line already frames the outcome.

## Implementation

### `OnResolve` hook (`internal/cmd/modules.go`)

```go
type ModulesOpts struct {
    // … existing fields …
    // OnResolve, when set, is called with the final module set (or the
    // {"--all"} sentinel) immediately before the Odoo subprocess runs, so
    // the caller can emit a start line that names the actual modules —
    // including picker and `--last` resolutions the raw args don't reveal.
    OnResolve func(resolved []string)
}

// emitResolved invokes opts.OnResolve when set. Called by each module
// Run* right before runOdoo.
func emitResolved(opts ModulesOpts, resolved []string) {
    if opts.OnResolve != nil {
        opts.OnResolve(resolved)
    }
}
```

Call `emitResolved(opts, <set>)` immediately before every `runOdoo(...)`:

- `RunUpdate`: the `--last` `--all` branch and modules branch, the `--all`
  branch, and the final explicit/picker branch — `{"--all"}` for the all
  cases, the module slice otherwise.
- `RunInstall`, `RunUninstall`, `RunTest`: before their single `runOdoo`,
  with the resolved `modules`.

### Start line from the resolved set (`internal/repl`)

`startResolved` (next to `startLog` in
[copylast.go](../../internal/repl/copylast.go)):

```go
// startResolved emits the start line for a module command once its final
// module set is known (after picker / --last resolution), so the line
// names the actual modules. The logger encodes the target
// (echo.<cmd>.module.<mod> / .modules / .all) and the modules= field
// always spells out the full set.
func (sess *session) startResolved(name string, resolved []string) {
    logger := echoCommandLogger(name, resolved) + ".start"
    emitOdooLog("INFO", logger, name,
        []logField{{"modules", moduleField(resolved)}},
        sess.styles, sess.palette, sess.cfg.DBName)
}
```

`runModules` ([repl.go](../../internal/repl/repl.go)):

- Remove the unconditional `sess.startLog(name, args)` at the top.
- Set `opts.OnResolve = func(resolved []string) { sess.startResolved(name, resolved) }`.
- In the `case "modules":` branch (read-only listing, no resolution), call
  `sess.startLog(name, args)` before `cmd.RunModules` so its start framing
  is unchanged.

### Cleanup (`internal/repl/logemit.go`)

With `startLog` no longer used for module commands, `startLogger`'s
module-path branch and `resolvedFromArgs` are dead. Simplify
`startLogger` to `"echo." + name + ".start"` (drop the `args` param and
the `isModuleCommand` branch) and delete `resolvedFromArgs`. `startLog`'s
own `isModuleCommand` guard stays (its callers are all non-module now, so
positionals always ride as the `args` field — unchanged behavior).

### Tests

- `internal/repl/logemit_test.go` (new or existing): `startLogger`
  returns `echo.<name>.start`. (The resolved start line itself goes through
  `emitOdooLog`, already covered; assert `echoCommandLogger` + `moduleField`
  pairing for 1 / N / `--all` if not already.)
- `internal/cmd/modules_test.go`: a small test that `OnResolve` fires with
  the resolved set — e.g. `update --last` (with a seeded record) calls
  `OnResolve` with the saved modules, and `update --all` with `{"--all"}`.
  Stub `OnResolve` to capture; the run itself errors at `runOdoo` without
  docker, which is fine — assert the callback fired with the right set
  before the subprocess. (If `runOdoo` can't be reached without docker in
  the test harness, assert via the existing arg-resolution helpers instead
  and keep the OnResolve wiring covered by the REPL smoke path.)

### Docs

- `CHANGELOG.md` → `[Unreleased] / Changed`: the `update`/`install`/
  `uninstall` start line now names the resolved modules (picker / `--last`
  / `--all`), not just at the end.
- `context/progress-tracker.md` → mark Unit 38 done.
- `context/specs/00-build-plan.md` → add the Unit 38 row.

## Dependencies

None new. Reuses `emitOdooLog`, `echoCommandLogger`, `moduleField`.

## Verify when done

- [ ] `update --last` prints a start line naming the repeated module(s)
      (e.g. `echo.update.module.sale.start: update modules=sale`).
- [ ] A picker selection prints a start line naming the picked modules.
- [ ] `update --all` start line shows `modules=all`; explicit
      `update sale account` shows `modules=sale,account`.
- [ ] `install` / `uninstall` behave the same way.
- [ ] A pre-resolution error (`ErrNoLastUpdate`, cancelled picker, missing
      DB) emits no start line, and the failure/cancel line is unchanged.
- [ ] `modules` listing start line is unchanged.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
