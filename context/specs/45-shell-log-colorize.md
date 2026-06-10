# Unit 45: shell-log-colorize — Echo-style Odoo logs in the shell

## Goal

When `shell` opens the Odoo Python shell, Odoo prints a block of startup
logs straight through the PTY before the IPython prompt:

```
2026-06-10 01:23:49,976 336 INFO ? odoo: Odoo version 18.0-20241118
2026-06-10 01:23:50,274 336 INFO habitta_prod odoo.modules.loading: loading 1 modules...
2026-06-10 01:23:53,327 336 INFO habitta_prod odoo.modules.registry: Registry loaded in 3.067s
```

These come out monochrome and visually clash with the rest of Echo, whose
Odoo log lines (streamed `update`/`install`, post-command recaps) are styled
per-segment (level chip, pastel logger, accent db). This unit makes the
`shell` startup logs match — restyling each recognized log line on the way to
the terminal — while leaving the interactive parts (IPython banner, prompt,
eval output) untouched.

## Design

`shell` / `bash` / `psql` run interactively via a host-side PTY
(`docker.ExecInteractive`): the container's fused stdout+stderr is teed to the
terminal and to a capture buffer (for auto-copy on failure). Until now the tee
was a raw `io.Copy`, so Echo never saw the lines as lines.

The colorizer is **opt-in per session** via a `LineTransform` function:
`func(line string) (string, bool)` — return the restyled line + true when
recognized, or `("", false)` to pass the original bytes through. Only `shell`
sets one; `bash`/`psql` keep the plain passthrough (bash has no startup logs,
psql has its own banner).

The styling lives in the `repl` layer (`renderLogLine`), which the `docker`
package cannot import. So the transform is an **opaque closure threaded down**:
`repl.runShell` builds it (capturing `styles`/`palette`), `cmd.ShellOpts`
carries it as `docker.LineTransform`, `RunOdooShell` forwards it to
`ExecInteractive`. No import cycle — `docker`/`cmd` treat it as a black box.

**Interactivity is the hard constraint.** A naive line buffer would hold the
prompt (`In [1]: `, no trailing newline) until the user hits Enter. So:

- Complete, newline-terminated lines are transformed (or passed verbatim).
- A leftover partial line is flushed **raw and immediately** unless it starts
  with a digit (the only thing a forming Odoo log line can start with). A
  digit-leading partial waits up to `partialFlushDelay` (30 ms) for its
  newline, then flushes raw if it never arrives. Interactive content never
  starts with a digit, so keystroke echo never incurs the delay.

The capture buffer keeps the **raw** (ANSI-free) text; only the terminal sees
the styled output, so `copy-last` / auto-copy stay clean.

## Implementation

### `internal/docker/shell.go`

- `type LineTransform func(line string) (string, bool)`.
- `const partialFlushDelay = 30 * time.Millisecond`.
- `ExecInteractive` gains a trailing `transform LineTransform` param. In the
  TTY path, when non-nil, replace `io.Copy(io.MultiWriter(os.Stdout, &buf),
  ptmx)` with `copyWithLineTransform(os.Stdout, &buf, ptmx, transform)`; nil
  keeps the plain copy. Non-TTY fallback unchanged.
- `copyWithLineTransform(out, capture io.Writer, src io.Reader, transform)`:
  reads `src` in a goroutine; on each chunk drains complete lines via
  `emitCompleteLines`, then either schedules a 30 ms flush (digit-leading
  partial) or flushes raw now; a timer fire flushes the partial raw; on EOF
  drains + flushes and returns.
- `emitCompleteLines(out, capture, pending, transform)`: for each `\n`,
  writes raw line to `capture`, and to `out` either `transform`'d content +
  original ending (`\r\n`/`\n`) or the raw line; returns the remainder.
- `isDigit(b byte) bool`.

### `internal/cmd/shell.go`

- `ShellOpts.LineTransform docker.LineTransform`.
- `RunOdooShell` passes `opts.LineTransform`; `RunBash`/`RunPsql` pass `nil`.

### `internal/repl/repl.go` — `runShell`

- For `case "shell"`, set
  `opts.LineTransform = func(line string) (string, bool) { return
  renderLogLine(line, sess.styles, sess.palette) }` before `RunOdooShell`.

## Dependencies

None new. Reuses `renderLogLine` (`logrender.go`), `time`.

## Verify when done

- [ ] `shell` against a real Odoo prints the startup `INFO …` lines styled
      like streamed `update` output (level chip, pastel logger, accent db).
- [ ] The IPython banner, the `In [N]:` prompt, and eval output appear
      verbatim, and the prompt shows up immediately (no buffering lag).
- [ ] Keystroke echo is not laggy while typing in the shell.
- [ ] `bash` and `psql` are unchanged (raw passthrough).
- [ ] Auto-copy capture / `copy-last` after a failed `shell` holds raw,
      ANSI-free text.
- [ ] `go build ./...`, `go vet`, `go test ./...` pass; `shell_test.go`
      covers `emitCompleteLines` and the partial-prompt flush.
