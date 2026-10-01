# Unit 33: `--level` log level on module commands

## Goal

Let `update`, `install`, and `uninstall` take a `--level <lvl>` flag that
maps to Odoo's native `--log-level=<lvl>`, so a developer can dial the
verbosity of a module operation up or down (e.g. `update sale --level
debug_sql` to see the SQL, or `update sale --level warn` to quiet the
run). Invalid levels are rejected with the list of valid values before
Odoo is invoked.

## Design

Echo already speaks Odoo's `--log-level` — `test` hard-codes
`--log-level=test`. This unit exposes it as a user flag on the three
module lifecycle commands that build an `odoo -i/-u/--uninstall …
--stop-after-init` argv. The valid set is Odoo's own log levels, identical
across 17/18/19:

```
debug_rpc_answer  debug_rpc  debug  debug_sql  info  warn  error  critical  test  notset
```

The flag is parsed in the `cmd` layer (where Echo owns CLI ergonomics),
validated against that set, and appended to the Odoo argv via a new
`odoo.WithLogLevel` helper so the Odoo-flag knowledge stays in the `odoo`
package. When `--level` is omitted, nothing changes — Odoo uses its
default (`info`). The flag works the same in one-shot/recipe mode (it
needs no TTY), so it composes with Unit 31/32.

## Implementation

### `odoo.WithLogLevel` + `odoo.LogLevels` (`internal/odoo/cmd.go`)

```go
// LogLevels are the values Odoo's --log-level accepts (17/18/19).
var LogLevels = []string{
    "debug_rpc_answer", "debug_rpc", "debug", "debug_sql",
    "info", "warn", "error", "critical", "test", "notset",
}

// WithLogLevel appends `--log-level=<level>` to an argv when level is
// non-empty; a no-op otherwise. Keeps the Odoo flag spelling in the
// odoo package while letting the cmd layer decide when to apply it.
func WithLogLevel(cmd Cmd, level string) Cmd {
    if level == "" {
        return cmd
    }
    return append(cmd, "--log-level="+level)
}
```

### Flag parsing + validation (`internal/cmd/modules.go`)

The three parse loops (`RunInstall`, `RunUpdate`, `RunUninstall`) gain
`--level <lvl>` and `--level=<lvl>` handling. Because the space form
needs look-ahead, switch each from `for _, a := range opts.Args` to an
indexed `for i := 0; i < len(opts.Args); i++`. Factor the shared bits:

```go
// validLogLevel reports whether lvl is one of Odoo's accepted levels.
func validLogLevel(lvl string) bool {
    for _, v := range odoo.LogLevels {
        if v == lvl {
            return true
        }
    }
    return false
}

// ErrInvalidLogLevel is returned for an unrecognized --level value.
var ErrInvalidLogLevel = errors.New("invalid --level")
```

In each Run*, after collecting `level`:

```go
if level != "" && !validLogLevel(level) {
    return nil, fmt.Errorf("%w %q; valid: %s",
        ErrInvalidLogLevel, level, strings.Join(odoo.LogLevels, ", "))
}
```

Then wrap the built argv:

- `RunInstall`: `odoo.WithLogLevel(odoo.Install(buildConn(opts), modules, withDemo), level)`
- `RunUpdate`: both branches — `odoo.WithLogLevel(odoo.UpdateAll(buildConn(opts)), level)` and `odoo.WithLogLevel(odoo.Update(buildConn(opts), modules), level)`
- `RunUninstall`: `odoo.WithLogLevel(odoo.Uninstall(buildConn(opts), modules), level)`

`--level` is a flag, so it must not be mistaken for a module name: the
default branch that collects positionals into `modules` already skips
nothing, so the `--level`/`--level=` cases must be handled before the
positional default (a bare `--level` with no following value → error
`--level requires a value`).

### REPL wiring

- `internal/repl/commands.go` `commandFlags`: append `"--level"` to the
  `install`, `update`, and `uninstall` slices (so it highlights as a
  known flag and Tab-completes).
- `internal/repl/repl.go` `helpSections()` Modules section: add a
  `{"  --level <lvl>", "Odoo --log-level (debug…critical, default info)"}`
  sub-row under each of install / update / uninstall (or one shared note).

### Tests

- `internal/odoo/cmd_test.go` (new or existing): `WithLogLevel` appends
  `--log-level=debug` and is a no-op on `""`.
- `internal/cmd/modules_test.go`: parsing `update sale --level debug`
  yields modules `[sale]` and the argv ends with `--log-level=debug`;
  `--level=warn` form; an invalid level returns `ErrInvalidLogLevel`; a
  bare `--level` with no value errors.
- The existing `commandhl_test.go` `commandFlags`↔`Registry` guard and
  `registry_test.go` help/Registry cross-check must stay green.

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `--level <lvl>` on
  `update`/`install`/`uninstall`.
- `context/progress-tracker.md` → mark Unit 33 done with a session note.

## Dependencies

None new. Uses Odoo's built-in `--log-level` (verified identical on
17/18/19).

## Verify when done

- [ ] `update sale --level debug` runs and Odoo emits debug output; the
      built argv ends with `--log-level=debug`.
- [ ] `--level=warn` (the `=` form) works identically.
- [ ] `install`/`uninstall` accept `--level` the same way.
- [ ] An invalid `--level nope` fails with `ErrInvalidLogLevel` and lists
      the valid values; a bare `--level` (no value) errors.
- [ ] Without `--level`, behavior is byte-for-byte unchanged.
- [ ] `--level` is not treated as a module name (no spurious module).
- [ ] `--level` highlights as a known flag and Tab-completes for the three
      commands.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
