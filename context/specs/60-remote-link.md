# Unit 60: `link` — bind the local cwd to a connect target + streaming remote exec

## Goal

Two deliverables that together let Echo run commands on a remote Odoo host
from a local checkout (the foundation `deploy` builds on in Unit 61):

1. A new command `link [<target>] [--show] [--rm]` that binds the current
   working directory (typically a pure addons repo with no
   `docker-compose.yml`) to a named global connect target by writing the
   target's `ssh_host` / `remote_path` into the per-project `[connect]`
   section — the same section `connect` and `i18n-pull` already consume.
2. A **streaming** remote-exec primitive: a `runSSH` variant that forwards
   remote stdout/stderr line by line in real time through the same
   `emitStreamLine` pipeline local commands use, instead of buffering the
   whole output like `runSSH` does today. This satisfies invariant 2
   (stream, never buffer-and-dump) for remote runs and is what makes
   live remote logs possible.

```
link                # picker over global connect_targets, then bind cwd
link prod           # bind cwd to the "prod" target directly
link --show         # print the current binding (and probe the remote)
link --rm           # remove the [connect] section from this project
```

## Design

**Reuse, don't reinvent.** Unit 50 (`i18n-pull`) already built target
resolution (`--from` → project `[connect]` → global targets), the
projectless one-shot path, `fetchRemoteProfile`, `remoteContainerCmd`, and
`shellQuote`. Unit 60 adds only (a) the explicit *write* side of that
binding and (b) the streaming transport. Nothing about how `connect` /
`i18n-pull` resolve their remote changes.

**Binding model.** The per-project config is keyed by the SHA-256 of the
project root's absolute path. `link` must work from a repo without a
compose file, so it is a `projectlessOneShot` like `i18n-pull`: when
`FindRoot` fails, the key falls back to cwd (or `-C <dir>`). `link <target>`
looks up the named `ConnectTarget` in `global.toml` and copies its
`SSHHost`/`RemotePath` into `Config.ConnectSSHHost`/`ConnectRemotePath`,
persisted via the existing `config.Save` round-trip (which already maps
those fields to the `[connect]` TOML section). No new storage format.

- No `<target>` + TTY → single-select fuzzy picker (`cmd.PickOne`) over the
  global connect targets, showing `name  (host → path)`. No targets
  configured → clean error pointing at `echo connect <name>` registration.
- No `<target>` + non-TTY → fails closed with `ErrNonInteractive`
  (invariant 9), exit 2.
- `--show` with no binding → INFO "not linked" (exit 0); `--rm` is
  idempotent.
- `<target>` that doesn't exist in `global.toml` → error listing the
  available target names.

**Probe after binding.** After saving (and on `--show`), `link` probes the
remote: `fetchRemoteProfile` over SSH, then reports the server's profile as
log fields (`db`, `stage`, `odoo version`, containers). The save happens
*before* the probe — a broken VPN must not lose the binding — and a probe
failure is a `WARNING` ("linked but unreachable"), not a command failure.
A `prod` stage in the probe is reported with the stage chip semantics used
elsewhere (display only; no confirm — `link` mutates nothing remote).

**Streaming remote exec.** New `runSSHStream(ctx, host, remoteCmd string,
stdin []byte, onLine func(string)) error` next to `runSSH` in
`internal/cmd`:

- `exec.CommandContext("ssh", "-o", "BatchMode=yes", host, remoteCmd)` with
  `StdoutPipe` + `StderrPipe`, each drained by a `bufio.Scanner` goroutine
  feeding `onLine` (mutex-serialized), `cmd.Wait()` after both close.
- The non-zero-exit error keeps the last stderr line for context (the full
  stream already went to `onLine`, so nothing is lost).
- Callers compose it with the existing `remoteContainerCmd` builder, so a
  remote `compose exec`/`compose ps` is one call:
  `runSSHStream(ctx, host, remoteContainerCmd(target, argv…), nil, onLine)`.
- On the REPL side the lines flow through `emitStreamLine` — remote Odoo
  log lines colorize, classify (warnings/errors counters), and feed
  `report`/`copy-last` exactly like local output. Remote runs are
  indistinguishable from local ones on screen except for the resolved
  target in the start line.

**Proof of life.** To exercise the streaming path end-to-end inside this
unit (instead of waiting for Unit 61), `link --show` streams the remote
`cd <remote_path> && <compose> ps` table after the profile probe — a live,
visibly-streamed remote command against the bound target.

**Log lines.** `link` emits Odoo-style lines via the same
`Log func(level, sub, msg, db string, fields ...[2]string)` callback shape
as `i18n-pull`, rendered by the REPL under `echo.link`: `target resolved` →
`saved` (with `host=`/`path=` fields) → `probing remote` → `linked`
(profile fields) or `WARNING remote unreachable`.

## Implementation

### `internal/cmd/link.go`

- `LinkOpts{Cfg, Root, Args, Palette, Log, StreamOut}`.
- `parseLinkArgs(args)` → `{target string, show, rm bool}`; `--show`/`--rm`
  mutually exclusive with each other and with a positional target.
- `RunLink(ctx, opts) error`: dispatch to bind / show / rm as designed.
  Bind = resolve target (arg or picker) → mutate cfg copy → `config.Save`
  → probe. Show = read binding → probe → stream remote `compose ps`.
  Rm = clear `ConnectSSHHost`/`ConnectRemotePath`/`ConnectChromePath` →
  `config.Save`.

### `internal/cmd/connect.go` (or a new `remote.go`)

- `runSSHStream` as designed, beside `runSSH`. `runSSH` stays for the
  short request/response calls (profile fetch, `cat`); existing callers
  untouched.
- Unit test for the stream runner using a fake command (e.g. swap the
  binary via a package-level `sshBin` var or test helper) asserting lines
  arrive in order and stderr is interleaved.

### `internal/repl/link.go` + wiring

- `runLink` mirroring `runI18nPull`: `startLog`, stats-wrapped stream,
  `finalize`/`commandFailureLog`.
- `Registry`, `dispatchNames` (one-shot eligible), `commandFlags["link"] =
  {"--show", "--rm"}`, `helpSections` (connect group), and
  `projectlessOneShot` in `main.go` extended to include `link`.

## Dependencies

- none (reuses `internal/cmd` connect helpers, `internal/config`,
  `cmd.PickOne`).

## Verify when done

- [ ] From a plain addons repo (no compose file), `echo link <target>`
      writes `[connect]` into the cwd-keyed project toml; a later
      `echo i18n-pull <mod>` from the same cwd uses that binding with no
      `--from`.
- [ ] `link` with no args opens the target picker on a TTY and fails
      closed (exit 2) without one; unknown target name errors listing the
      available names.
- [ ] Probe failure (unreachable host) still saves the binding and exits 0
      with a `WARNING`; `link --show` reports binding + remote profile and
      streams the remote `compose ps` live.
- [ ] `link --rm` clears the section; `--show` afterwards says "not
      linked".
- [ ] `runSSHStream` delivers lines as they are produced (verified by the
      unit test), and remote lines render through `emitStreamLine` with
      log-level coloring.
- [ ] `parseLinkArgs` and the target-resolution paths are unit-tested.
- [ ] `go build/vet/test ./...` pass; `registry`/`commandhl` cross-checks
      stay green; `CHANGELOG.md` `[Unreleased]` gets an `Added` entry.
