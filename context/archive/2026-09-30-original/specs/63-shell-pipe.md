# Unit 63: shell-pipe — piped stdin into the Odoo shell, local and remote

## Goal

Make `shell` accept piped stdin: `cat fix.py | echo shell` (and
`… | echo shell --from prod`) runs the piped Python through the Odoo
shell — local or remote — streaming the output Odoo-colored instead of
demanding a TTY. Additionally, `shell-run -` reads the script from stdin
explicitly, mirroring the `echo run -` convention.

```
cat fix.py | echo shell                  # local, headless
cat fix.py | echo shell --from prod --force
echo 'env["res.users"].search_count([])' | echo -C my-shop shell
generate_fix.sh | echo shell-run - --remote
```

## Design

**Detection, not a flag.** When the `shell` command runs with a stdin
that is **not a TTY**, it switches to pipe mode: the stdin stream is fed
to `odoo shell` exactly like `shell-run` feeds a file. Inside the
interactive REPL stdin is always a TTY, so nothing changes there; this
only fires in one-shot — which is precisely the piping use case. `bash`
and `psql` are untouched.

**One pipeline, three sources.** Pipe mode reuses the `shell-run`
machinery end to end; the script source generalizes from "a file path"
to "a reader":

- `shell-run <file>` → the file (today's behavior),
- `shell-run -` → stdin, explicit,
- `shell` with piped stdin → stdin, auto-detected.

Local execution goes through a new `docker.ExecWithStdinReader` (the
existing `ExecWithStdin` becomes a thin file-opening wrapper). Remote
execution reads the stdin bytes and passes them to `runSSHStream` —
which already takes stdin bytes (Unit 62 pipes the script file the same
way).

**Remote composes as in Unit 62**: `--from <target>` / `--remote` on the
piped `shell` select the remote instance; the resolution chain and the
remote-prod gate (`confirmRemoteProd`) are unchanged.

**Prod stays guarded.** Pipe mode is non-TTY by definition, so the
prod-stage confirm fails closed (invariant 9) — a piped run against a
prod database requires an explicit `--force` on the command line.

**Output semantics.** Piped `shell` streams the output through
`emitStreamLine` (level-colored, counted, `copy-last`-able) and
finalizes as `shell`; it does **not** auto-copy — a pipeline consumer
owns the output. `shell-run -` keeps shell-run's auto-copy-prints
behavior (`--no-copy` opts out), since the user invoked the script
runner explicitly.

**`shell-run -` guard.** `-` with a TTY stdin would block forever
waiting for input, so it errors immediately ("stdin is a terminal —
pipe a script or pass a file").

## Implementation

### `internal/docker/exec.go`

- `ExecWithStdinReader(ctx, composeCmd, dir, container, argv, r io.Reader,
  onLine)` — the current `ExecWithStdin` body with the reader passed in;
  `ExecWithStdin` opens the file and delegates.

### `internal/cmd/shellrun.go`

- `ShellScriptOpts.Stdin io.Reader` — when non-nil it overrides
  `ScriptPath` as the script source. Local → `ExecWithStdinReader`;
  remote → `io.ReadAll(Stdin)` → `runSSHStream` stdin bytes.

### `internal/cmd/interactive.go`

- `StdinPiped() bool` exported — true when os.Stdin is not a terminal;
  used by the REPL to detect pipe mode.

### `internal/repl/repl.go` + `internal/repl/shellrun.go`

- `runShell` case `"shell"`: when `cmd.StdinPiped()`, route to
  `runShellPiped(ctx, args)` (start line already emitted) instead of the
  interactive path.
- `runShellPiped`: mirrors `runShellRun`'s streaming frame with
  `Stdin: os.Stdin`, no auto-copy, finalize as `shell`.
- `runShellRun`: positional `-` → stdin mode (TTY-guarded as above),
  skipping the picker/file resolution.

### Wiring

No new flags or Registry entries — pipe mode is auto-detected; `-` is a
positional. Help/README gain a line documenting both forms.

## Dependencies

- Unit 62 (remote shell-run plumbing, `resolveRemoteShell`).

## Verify when done

- [ ] `echo 'print(env)' | echo shell` (one-shot, local) runs headless and
      streams colored output; exit code reflects the run.
- [ ] `cat fix.py | echo shell --from <target> --force` runs the piped
      script on the remote instance.
- [ ] `shell-run -` reads stdin (and keeps auto-copy); `shell-run -` on a
      TTY errors immediately instead of blocking.
- [ ] Piped `shell` against a prod stage without `--force` fails closed
      (exit 2); interactive `shell` in the REPL is completely unchanged.
- [ ] `ExecWithStdin` still passes its existing callers (file source).
- [ ] `go build/vet/test ./...` pass; registry/commandhl cross-checks stay
      green; `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
