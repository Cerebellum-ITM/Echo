# Unit 07: Action Result Finalization

## Goal

After every long-running Odoo / docker command (`install`, `update`,
`uninstall`, `up`, `down`, `restart`), print a clear single-line result
on success or failure so the user does not have to scan the streamed
log to decide whether the action worked. Catch silent failures: Odoo
sometimes exits 0 even after logging `ERROR` / `CRITICAL`.

This unit also clarifies the post-command UX: when a command finishes,
the next prompt should be visually separated from the streamed output.

## Design

### Success / failure detection

Two signals:

1. **Exit status** of the subprocess — captured by `cmd.Wait()`. Non-zero
   = explicit failure.
2. **Log severity** observed during streaming. While streaming, count
   how many lines were classified `ERROR` or `CRITICAL` (see Unit 08
   for the classifier). Non-zero count = silent failure.

Decision matrix:

| Exit | ERROR/CRITICAL lines | Final line                                              |
|------|----------------------|---------------------------------------------------------|
| 0    | 0                    | `s.Ok.Render("✓ <name> completed")`                     |
| 0    | >0                   | `s.Err.Render("✗ <name> finished with N error(s)")`     |
| !=0  | any                  | `s.Err.Render("✗ <name> failed: <stderr / exit msg>")`  |

`<name>` is the user-visible command name (`install`, `update`, etc.).
For `install`/`update`/`uninstall`, append the module list:
`✓ install completed (sale_management, sale_stock)`.

### Cancellation

When the user cancels the picker (Esc) before the action runs, no
subprocess is launched — print `init cancelled — no changes saved` style
warning, not a `✗ failed` line. This is the existing behaviour; the
finalization only fires when a subprocess actually ran.

### Visual separation

Insert one blank line before the final result line so the green/red ✓✗
stands out from the streamed lines. The next prompt then appears one
line below.

## Implementation

### Counting errors during streaming

Add a small middleware to the stream callback in `runDocker` / `runModules`:

```go
type runStats struct {
    errors int
}

func (s *runStats) wrap(inner func(string)) func(string) {
    return func(line string) {
        if classifyOdooLog(line) == "err" {
            s.errors++
        }
        inner(line)
    }
}
```

`classifyOdooLog` is defined in Unit 08. For `up/down/restart/ps/logs`,
compose output is not Odoo-formatted, so `errors` typically stays 0;
the exit code carries the signal.

### `internal/repl/repl.go` updates

Both `runDocker` and `runModules` adopt the same pattern:

```go
stats := &runStats{}
opts := cmd.ModulesOpts{
    ...
    StreamOut: stats.wrap(func(line string) {
        sess.print(Line{Kind: classifyOdooLog(line), Text: line})
    }),
}

err := cmd.RunUpdate(ctx, opts)
sess.print(Line{Kind: "out", Text: ""}) // blank separator

display := name
if len(args) > 0 {
    display += " (" + strings.Join(args, ", ") + ")"
}

switch {
case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
    sess.print(Line{Kind: "warn", Text: name + " cancelled — no changes saved"})
case err != nil:
    sess.print(Line{Kind: "err", Text: "✗ " + display + " failed: " + err.Error()})
case stats.errors > 0:
    sess.print(Line{Kind: "err", Text: fmt.Sprintf("✗ %s finished with %d error(s)", display, stats.errors)})
default:
    sess.print(Line{Kind: "ok", Text: "✓ " + display + " completed"})
}
```

### Args display formatting

`runModules` receives `args` after flag parsing — but flags (`--with-demo`,
`--all`, `--config`) should not appear in the final summary. Filter
non-flag args before formatting:

```go
positional := nil
for _, a := range args {
    if !strings.HasPrefix(a, "-") {
        positional = append(positional, a)
    }
}
```

`--all` → format as `"all modules"`.

## Dependencies

None new.

## Verify when done

- [ ] `install foo` succeeds → green `✓ install completed (foo)` line
      below the log.
- [ ] `install foo` fails (e.g. unknown module) → red `✗ install failed:
      ...` line.
- [ ] Odoo prints `ERROR ?` but exits 0 → red `✗ update finished with
      1 error(s)` line.
- [ ] Cancelling the picker prints the warn `... cancelled` line, no `✗`.
- [ ] A blank line separates the streamed log from the final result.
- [ ] `up`, `down`, `restart` produce `✓ up completed`-style lines on
      success.
- [ ] Final line does not include flags (`--with-demo`, `--all`, etc.).
