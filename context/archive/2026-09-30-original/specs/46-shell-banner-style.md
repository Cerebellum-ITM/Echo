# Unit 46: shell-banner-style — Echo-style the Odoo shell startup block

## Goal

Unit 45 colorized the Odoo *log* lines in `shell`. The Odoo Python shell also
prints a non-log startup block before the prompt:

```
env: <odoo.api.Environment object at 0x7f183e756570>
odoo: <module 'odoo' from '/usr/lib/python3/dist-packages/odoo/__init__.py'>
openerp: <module 'odoo' from '/usr/lib/python3/dist-packages/odoo/__init__.py'>
self: res.users(1,)
Python 3.12.3 (main, Nov  6 2024, 18:32:19) [GCC 13.2.0]
Type 'copyright', 'credits' or 'license' for more information
IPython 9.11.0 -- An enhanced Interactive Python. Type '?' for help.
Tip: Use the IPython.lib.demo.Demo class to load any Python script as an interactive demo.
```

These are not Odoo logs, so they passed through raw and clashed with Echo.
This unit restyles them: the injected namespace globals
(`env`/`odoo`/`openerp`/`self`) read as Echo's structured key=value fields,
and the stock Python/IPython banner lines fade into the background so the
prompt stands out.

## Design

A natural extension of Unit 45's per-line `LineTransform`. After the
ANSI-strip + `renderLogLine` attempt (which handles Odoo logs), the shell
transform falls back to `styleShellBanner`:

- **Namespace globals** — `^(env|odoo|openerp|self): (.*)$` → key in
  `palette.Accent` (bold), `: value` in `Dim`. Mirrors `keyColor`'s
  accent treatment of `module`/`modules` keys, so the globals read like the
  structured fields Echo prints elsewhere.
- **Python/IPython banner** — lines starting with `Python `, `IPython `,
  `Type '`, or `Tip: ` → faded (`Faint`).
- Anything else → `("", false)`, passed through verbatim (the prompt,
  eval output, blank lines).

The user picked this treatment (accent-key globals + faded banner) over a
one-line namespace summary or muting the whole block.

## Implementation

### `internal/repl/shellbanner.go` (new)

- `shellGlobalLine` regexp.
- `styleShellBanner(line, s, p) (string, bool)` — globals → accent/dim,
  banner → faint, else `("", false)`.
- `isIPythonBanner(line) bool` — prefix check for the four banner lines.

### `internal/repl/repl.go` — `runShell` shell transform

```go
opts.LineTransform = func(line string) (string, bool) {
    clean := stripANSISeq(line)
    if styled, ok := renderLogLine(clean, sess.styles, sess.palette); ok {
        return styled, true
    }
    return styleShellBanner(clean, sess.styles, sess.palette)
}
```

## Dependencies

Unit 45 (the `shell` `LineTransform` + `stripANSISeq`).

## Verify when done

- [ ] In `shell`, the `env:/odoo:/openerp:/self:` lines show the key in
      accent and the value dimmed; `self`'s value (`res.users(1,)`) is intact.
- [ ] The Python/IPython/Tip banner lines appear faded.
- [ ] The `In [N]:` prompt and eval output are unchanged (verbatim).
- [ ] Odoo log lines still get the Unit 45 styling (regression).
- [ ] `go build ./...`, `go vet`, `go test ./...` pass; `shellbanner_test.go`
      covers matches, passthrough, and value preservation.
