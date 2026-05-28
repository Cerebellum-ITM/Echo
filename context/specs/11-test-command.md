# Unit 11: Test Command

## Goal

Add a REPL command `test <mod>...` that runs the Odoo test suite for
one or more modules against the configured DB. Behaves like a fourth
sibling of `install` / `update` / `uninstall` (Unit 05): same builder
pattern, same picker fallback, same streaming + finalize frame, same
auto-copy on failure.

## Design

`test` mirrors `update` semantically — Odoo runs tests when the module
is installed or updated with `--test-enable`. Since the day-to-day
test loop is "I changed code in an already-installed module, re-run
its tests," we default to `-u` (update). The command does not need an
install-mode variant in v1; devs can run `install <mod>` first if the
module is fresh.

### Argv produced

```
odoo <conn-flags> --no-http --http-port=8189 --test-enable -u <mod1>,<mod2>,... --stop-after-init
```

With `--tags <spec>`:

```
odoo <conn-flags> --no-http --http-port=8189 --test-tags <spec> -u <mod1>,... --stop-after-init
```

`--test-tags` implies `--test-enable` per the Odoo docs, so we don't
emit both — when `--tags` is present we use only `--test-tags <spec>`.

Both `--no-http` and `--http-port=8189` are always emitted: the test
process runs inside the same container as the live Odoo server
(which is already bound to 8069). `--no-http` is the documented way
to skip the HTTP bind, but it was observed to be silently ignored on
Odoo 19 Enterprise — the explicit `--http-port=8189` redirects the
bind to an uncommon high port as a defense in depth. Either alone is
enough on a compliant Odoo; together they survive both quirks.
HttpCase suites still work — they spin up their own ephemeral server
independently of these flags.

Flags are identical across Odoo 17, 18, and 19 (verified against the
official CLI docs for each version). No version branching needed.

### Selection UX

- `test <mod...>` → use the args directly.
- `test` (no args) → open the same fuzzy picker `install`/`update`/
  `uninstall` use, with title `"Modules to test"`. Picker cancel
  (Esc) returns `ErrCancelled`.
- `test --tags <spec>` → `--tags` consumes the next token as the
  test-tag filter spec, all remaining positionals are modules. If no
  modules are given alongside, picker opens as usual.

### Streaming + finalize

Reuses the entire `runModules` pipeline in `internal/repl/repl.go`:

- `startLog("test", args)` opens the frame.
- `logColorer` + `runStats` classify and count error/warning lines.
- `cmd.RunTest` returns the resolved `[]string` of modules picked.
- On success: `successLog("test", resolved, stats.warnings)` →
  `INFO echo.test.module.<mod>: test completed` (one module) /
  `echo.test.modules` (N modules).
- On failure: `copyFailureLog` auto-copies the last error block to
  the clipboard with logger `echo.test.module.<mod>.error`.
- On cancel: `finalize("test", …, ErrCancelled)` →
  `WARNING echo.test.cancelled`.

No new code in the REPL streaming / log layer — the existing path
treats `test` exactly like `install/update/uninstall`.

### Prod confirmation

Tests can have destructive side effects (HttpCase rollbacks, demo
data loads). For consistency with `update` (which already runs in any
stage without an extra prompt), `test` does **not** add a prod-stage
confirmation. Devs who don't want tests on prod simply shouldn't
point Echo at prod. If this becomes a footgun later, gate behind a
`--force` flag in a follow-up.

## Implementation

### `internal/odoo/cmd.go` — add builder

After the existing `Uninstall` builder (around line 79), add:

```go
const TestHTTPPort = "8189"

// Test builds the argv to run unit tests for one or more modules.
// Mirrors Update (uses -u so the module is upgraded if already
// installed, picking up code changes) and toggles --test-enable. When
// tags is non-empty we pass --test-tags <spec> instead, which implies
// --test-enable per Odoo's CLI behavior. Both --no-http and
// --http-port=8189 avoid the port 8069 clash with the live Odoo
// server inside the same container; --http-port is the safety net
// for distributions (Odoo 19 Enterprise) where --no-http alone was
// observed to be ignored.
func Test(c Conn, modules []string, tags string) Cmd {
    args := append(Cmd{"odoo"}, c.flags()...)
    args = append(args, "--no-http", "--http-port="+TestHTTPPort)
    if tags != "" {
        args = append(args, "--test-tags", tags)
    } else {
        args = append(args, "--test-enable")
    }
    args = append(args, "-u", strings.Join(modules, ","), "--stop-after-init")
    return args
}
```

### `internal/cmd/modules.go` — add runner

Following the `RunInstall` / `RunUpdate` / `RunUninstall` shape, add:

```go
// RunTest runs the Odoo test suite for the given modules. The
// returned slice is the resolved module list (after picker resolution
// and flag stripping) so the REPL layer can name the logger
// hierarchically as echo.test.module.<mod>.
func RunTest(ctx context.Context, opts ModulesOpts) ([]string, error) {
    if err := requireOdooConfig(opts.Cfg); err != nil {
        return nil, err
    }

    var (
        modules []string
        tags    string
    )
    args := opts.Args
    for i := 0; i < len(args); i++ {
        a := args[i]
        switch {
        case a == "--tags":
            if i+1 >= len(args) {
                return nil, fmt.Errorf("--tags requires a value")
            }
            tags = args[i+1]
            i++
        case strings.HasPrefix(a, "--tags="):
            tags = strings.TrimPrefix(a, "--tags=")
        case strings.HasPrefix(a, "-"):
            // forward-compat: ignore unknown flags rather than fail
        default:
            modules = append(modules, a)
        }
    }

    if len(modules) == 0 {
        picked, err := pickModulesInteractive(opts, "Modules to test")
        if err != nil {
            return nil, err
        }
        modules = picked
    }
    return modules, runOdoo(ctx, opts, odoo.Test(buildConn(opts), modules, tags))
}
```

### `internal/repl/repl.go` — dispatch + help

- `dispatchNames` (line 113): add `"test"` to the modules group:

  ```go
  "install", "update", "uninstall", "test", "modules",
  ```

- `dispatch` switch (line 156): extend the modules case:

  ```go
  case "install", "update", "uninstall", "test", "modules":
      sess.runModules(ctx, cmd, args)
  ```

- `runModules` switch on `name` (around line 339): add `test`:

  ```go
  case "test":
      resolved, err = cmd.RunTest(ctx, opts)
  ```

- `helpSections()` Modules section (around line 187): add the help
  entries after `uninstall`:

  ```go
  {"test <mod...>", "Run tests for modules (--test-enable, -u)"},
  {"  --tags <spec>", "Filter via --test-tags (e.g. /sale.TestSaleOrder, :TestX.test_y)"},
  ```

### `internal/repl/commands.go` — Registry

Same position as in `dispatchNames`. The two consistency tests in
`registry_test.go` will fail until both lists match.

```go
"install", "update", "uninstall", "test", "modules",
```

### Tests

No new tests required — the existing `registry_test.go` checks
(`TestRegistryUnique`, `TestRegistryMatchesHelp`,
`TestRegistryMatchesDispatch`) will catch any inconsistency between
`Registry`, `dispatchNames`, and `helpSections`. Run them after the
edits.

## Dependencies

None new. All stdlib + existing internal packages.

## Verify when done

- [ ] `test sale` runs `odoo --no-http --http-port=8189 --test-enable
      -u sale --stop-after-init` against the configured DB and streams
      output through the REPL with log-level coloring (Unit 08).
- [ ] `test sale --tags :TestSaleOrder` emits `--no-http
      --http-port=8189 --test-tags :TestSaleOrder -u sale` (no extra
      `--test-enable`).
- [ ] `test --tags /sale account` resolves modules=[account] and
      tags=/sale.
- [ ] `test` (no args) opens the fuzzy picker with title "Modules to
      test"; Esc cancels with `WARNING echo.test.cancelled`.
- [ ] Successful run closes with `INFO echo.test.module.sale: test
      completed` (single module) or `echo.test.modules` (N).
- [ ] Failed run (test assertion error in Odoo) auto-copies the error
      block to the clipboard with logger `echo.test.module.sale.error`,
      same as `install`/`update`.
- [ ] `help` shows the `test` entry under the Modules section.
- [ ] `go build ./...`, `go vet ./...`, and `go test ./internal/repl/...`
      all pass.
