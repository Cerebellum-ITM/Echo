# Unit 127 · config-load-safety
> Spec for one unit of [the plan](../../work/build/plan.md). Implement exactly this: no more, no less. Last verified: 2026-09-30.

## Goal

A config file Echo cannot parse is never silently replaced, and a remote target whose stage is not declared is
treated as production. Today a syntax error in `global.toml` loads defaults and the next save overwrites the
file, and a server profile without `stage` is never prod-gated.

## Design

### A. Local config files fail loudly

The two files the user edits by hand, `~/.config/echo/global.toml` and the project profile
`~/.config/echo/projects/<key>.toml`, are read by `Load`, `LoadGlobal`, `loadGlobalFile` and the read half of
`SaveProject` (`internal/config/config.go`); all of them do `_ = toml.Unmarshal(...)`.

- A new error type `config.ParseError{Path string; Err error}` whose message names the file and, when the TOML
  library gives one, the line and column: `cannot parse ~/.config/echo/global.toml: line 12, column 3: …`.
  A missing file stays what it is today (empty, defaults applied).
- `Load` and `LoadGlobal` return `ParseError` instead of continuing.
- Every writer that reads before writing (`SaveGlobal`, `SaveProject`, `SaveConnectTarget`, and any other
  caller of `loadGlobalFile`) refuses to write when the existing file does not parse, returning the same
  `ParseError`. Nothing ever writes over a file it could not read.
- `main.go`: a `ParseError` at startup prints one `ERROR echo.config: …` line (with the hint `fix the file or
  move it aside`) and exits 2, in the REPL and in one-shot mode alike. No REPL starts on half a config.
- `ParseProjectInfo` (server profiles listed while registering a target) returns the error too; the caller
  skips that profile with a WARNING naming it instead of listing it with empty fields.

### B. State files keep the corrupt copy

The state files Echo owns (`deploy-history/`, `checkpoints/`, `last-updates/`, `last-sequences/`,
`connect-sessions/`) stay best-effort: a corrupt one still reads as empty so a command never fails on it. What
changes is the write: before writing over a file that exists but does not parse, Echo renames it to
`<name>.corrupt-<YYYYMMDD-HHMMSS>` and logs a WARNING with the new name. The checkpoint index is the one that
matters (it is how `deploy --rollback` finds restore points), but the rule is shared: one helper in
`internal/config` used by every state writer that loads first.

### C. Remote profile parse errors fail the command

`ParseRemoteProfile` returns `(RemoteProfile, error)`. `fetchRemoteProfile` turns a parse error into
`server profile <path> on <host> does not parse: line N: …` and the command fails (exit 1) before touching the
server. Today the broken file reads as an empty profile: empty container names, no stage, no actions.

### D. An undeclared remote stage is production

The stage gates (remote prod confirm, checkpoint default, test-on-prod, `watch` on prod, `db-admin` risk) all
read `connectTarget.stage`, which every remote path builds in `remoteConnectTarget` (`internal/cmd/db_remote.go`).
That function normalizes it:

- `dev`, `staging`, `prod` (case-insensitive, trimmed) are kept as they are.
- Anything else, including empty, becomes `prod` for every gate, and the target carries `stageDeclared=false`.
- A command on such a target logs one WARNING right after the system status line:
  `target stage is not declared on the server (stage="") — treated as prod; set stage in the server profile`.
- Display keeps the raw value: the system status line and pickers still show what the server says.

Local config keeps its rule (missing stage means `dev`): the user's own machine is not the risk this covers.
Reverb environments are unaffected: Reverb always writes `stage` (`dev` or `staging`) in the profiles it
manages.

## Implementation

### internal/config

- `ParseError` type; a `decodeTOML(path, data, v) error` helper that wraps library errors into it.
- `Load`, `LoadGlobal`, `loadGlobalFile` (returns `(globalFile, error)`), `SaveGlobal`, `SaveProject`,
  `SaveConnectTarget`, `ParseProjectInfo`, `ParseRemoteProfile` use it and propagate.
- `preserveCorrupt(path, log)` helper; called by the writers of deploy history, checkpoints, last updates, last
  sequences and connect sessions before they overwrite a file whose load failed to parse.

### internal/cmd

- `remoteConnectTarget` normalizes the stage and sets `stageDeclared`; `connectTarget` gains the field.
- One helper `warnUndeclaredStage(target, log)` called where the system status line is printed for a remote
  target (`deploy`, `i18n-pull`, `link --show`, `resolveRemoteShell`).
- `fetchRemoteProfile` returns the parse error with host and path.
- `registerTarget` skips unparseable server profiles with a WARNING.

### main.go and internal/repl

- Startup handles `config.ParseError`: one error line, exit 2.

### Docs

- `knowledge/architecture-and-traps.md`: replace the "parse errors are swallowed" trap with the new rule.
- `knowledge/remote-targets.md`: the undeclared-stage rule next to the prod-gate text.
- `CHANGELOG.md` `[Unreleased]`: Fixed (config never overwritten after a parse error; remote parse errors fail)
  and Changed (undeclared remote stage is treated as prod).
- `work/build/plan.md`: drop the TOML row from "Small fixes".

## Dependencies

- None new. The TOML library's `ParseError` already carries the position.

## Verify when done

- [ ] A `global.toml` with a syntax error makes `echo_cli ps` and the REPL exit 2 with the file and line;
      the file is byte-identical afterwards.
- [ ] Any `--set-*` or `link` that saves config refuses to write over an unparseable file.
- [ ] A corrupt `checkpoints/<key>.toml` reads as empty, and the next `AddCheckpoint` leaves
      `<key>.toml.corrupt-<ts>` next to the new file.
- [ ] A server profile that does not parse makes `deploy --from <t> --dry-run` fail before any remote change.
- [ ] A server profile with no `stage` makes a remote `restart` ask for prod confirmation (fails closed without
      a TTY unless `--force`), turns the deploy checkpoint on by default, and prints the undeclared-stage WARNING
      once; a profile with `stage = "dev"` behaves as today.
- [ ] `go build ./...`, `go vet ./...` and `go test ./...` pass; new tests cover each item above with the
      existing seams (temp `HOME`, fake `ssh` on `PATH`).
