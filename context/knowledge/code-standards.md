# Code Standards
> Project-specific implementation conventions: command shape, the new-command wiring checklist, error and exit conventions, test seams, the done gate, dependency and protected-file rules. Generic Go style is not repeated. Last verified: 2026-09-30.

## Command shape

- A command is a function `cmd.RunXxx(ctx, XxxOpts) (Result, error)` in `internal/cmd/` (`RunDeploy`, `RunLink`, `RunConnect`…). It returns a result struct and reports progress through callbacks on the opts: `Log func(level, sub, msg, db string, fields ...[2]string)`, `StreamOut`/`OnLine` for raw lines, plus command-specific hooks (`OnPS`). It does not print and does not import `repl` ([boundary](architecture-and-traps.md#packages-and-boundaries)).
- The REPL side is a handler `sess.runXxx(ctx, args)` in `internal/repl/<name>.go`: it builds the renderers (`logColorer`, `runStats`), calls `cmd.RunXxx`, then maps the error. Reference pattern: `internal/repl/link.go`. Rendering rules: [ui](ui.md).
- Opts carry `Cfg`, `Root`, `Args`, `Palette`; a command that needs a user decision takes it from `Args` first and falls back to a picker only behind `requireTTY` ([invariant 2](architecture-and-traps.md#invariants)).
- Validate arguments before launching any subprocess or SSH call. Subprocess failure becomes an `err` log line (`sess.commandFailureLog`), never a crash of the REPL. An unknown REPL command prints a `warn` pointing at `help` and sets exit 2 (`dispatchParsed` default).
- The REPL layer holds no command business logic; version-specific Odoo argv lives in `internal/odoo` builders.

## New command wiring

Every new command (or user-facing flag) must touch all of these, in the same commit:

1. `cmd.RunXxx` plus its REPL handler (above).
2. `Registry` in `internal/repl/commands.go`: the order is the help order and the double-Tab list order.
3. `dispatchNames` (`internal/repl/repl.go`) and a `case` in `dispatchParsed`. `exit`/`quit` are in `Registry` only. `repl.IsScriptCommand` derives from `dispatchNames`, so a command is one-shot capable automatically.
4. `commandFlags` for every user-facing flag (not internal ones such as `-e` or `--no-http`): it powers live flag highlight (known = accent, unknown = faint, never red), Tab flag completion and build mode.
5. `helpSections()` entry. Rows starting with a space or `-` are flag rows, not command names; footer blocks (scripting, build mode, Reverb) are deliberately outside `helpSections()` so the cross-check keeps passing. The sequence picker reuses the help description.
6. Exit mapping in the handler: route `ErrCancelled`, `huh.ErrUserAborted`, `ErrNonInteractive`, `ErrUsage` through `sess.finalize(...)`, and for `ErrUsage` also `sess.exitCode = exitUsage`. `scriptExitCode` does not know `ErrUsage` ([trap](architecture-and-traps.md#cross-cutting-traps)).
7. Remote-capable? Add the name to `projectlessOneShot` in `main.go` (no test covers this). Selectable in a sequence? Add to `sequenceCommands` and, if it accepts `--from`, `remoteSequenceCommands` (`internal/repl/sequence.go`). About the REPL itself rather than the project (copy-last, help, clear, logview)? Add to `isMetaCommand` so it does not overwrite "the last command".
8. A value-taking remote flag? The parser must consume the value, not only read it ([trap](architecture-and-traps.md#cross-cutting-traps)). Build mode offers value pickers only where `buildPositionals`/`buildFlagValues` in `internal/cmd/build.go` define them.
9. `CHANGELOG.md` entry under `[Unreleased]`, same commit ([operations](operations.md#release-and-versioning)).

What enforces it: `internal/repl/registry_test.go` (Registry unique; Registry == help command names; Registry minus `exit`/`quit` == `dispatchNames`); the `init()` in `commands.go` (panics on a duplicate `Registry` name or a `commandFlags` key not in `Registry`, the one sanctioned `init()`); `commandhl_test.go` (flag keys are real commands); `sequence_test.go` (sequence lists are known commands and remote ones accept `--from`); `build_test.go` (build value-flags exist; `-E` and `--env` are never offered together). **Not enforced**: a parser accepting a flag missing from `commandFlags`, and `projectlessOneShot` membership.

## Errors and exits

- Wrap with `fmt.Errorf("context: %w", err)`. Sentinels in `internal/cmd`: `ErrUsage` (caller mistake, exit 2), `ErrNonInteractive` (exit 2), `ErrCancelled` (exit 3), `ErrQuit` (exit 0, Ctrl+X even inside pickers), `ErrNotConfigured` (promote). Wrap validation failures with `ErrUsage` so handlers can tell them from execution failures (exit 1). Exit-code table: [architecture-and-traps](architecture-and-traps.md#invariants).
- Failures that matter close with the Odoo-style `echo.<cmd>.error` line and auto-copy the failing output; usage errors, cancellations and read-only commands do not auto-copy.
- A destructive step guarded by a confirm takes `--force` to skip it and still fails closed without a TTY. Which stage decides: [prod-gate rule](architecture-and-traps.md#prod-gate-rule).
- Best-effort metadata (lock write, provenance stamp, deploy-history, cmd-log) must not fail the operation it annotates: warn and continue. Owner of the deploy case: [deploy-safety](deploy-safety.md).

## Subprocesses

- Use `exec.CommandContext` so Ctrl+C and timeouts propagate. Five sites use plain `exec.Command` today: `internal/cmd/view.go:315` (`bat` pager), `internal/cmd/connect_cdp.go:184` (Chrome launch), `internal/odoolint/xmllint.go:93`, `internal/project/root.go:35` (`git rev-parse`), `internal/clipboard/clipboard.go:77`.
- **Unresolved:** whether those five are intentional exceptions (Chrome and the pager are plausibly meant to outlive the context; the other three are short) or debt. Settle by reading each site and either adding a short comment explaining the exception or converting it to `CommandContext`; until then new code uses `CommandContext`.
- Remote commands: build the string with every token shell-quoted and the compose command left raw (so `docker compose` splits in two); transports and their buffering rules: [remote-targets](remote-targets.md).

## Config and state in code

- Config writers are load-modify-write and always assign owned fields; a new section follows the checklist in [architecture-and-traps](architecture-and-traps.md#config-and-state-storage). State files are written with `writeAtomic` after `preserveCorrupt`, and loaded best-effort; config files are read with `loadTOMLFile` and a parse error is returned, never swallowed ([architecture-and-traps](architecture-and-traps.md#config-and-state-storage)).
- Never write into the user's project directory except for outputs the user asked for ([invariant 4](architecture-and-traps.md#invariants)).
- Styling goes through `internal/theme`: tokens and `Lighten/Darken` for derived shades, no new raw hex. This is a goal; 154 ad-hoc `lipgloss.NewStyle()` calls exist outside the theme package. Rules: [ui](ui.md).
- Odoo version differences: check how 17/18/19 differ before assuming 18; encode them in `internal/odoo`. Do not hardcode `docker compose` ([invariant 5](architecture-and-traps.md#invariants)).
- If the exact Docker/Odoo invocation of a command is ambiguous, record an open question and ask before implementing.

## Test seams

Package-level vars so tests script the transport instead of running real tools (all in `internal/cmd` unless noted):

- `stdinIsTTY` (`interactive.go`): force the non-TTY path.
- `gitRunSSH` and `gitPushCommand` (`deploy_git.go`), `lockRunSSH` (`deploy_lock.go`), `doctorRunSSH` (`doctor.go`), `ckptRunSSH`/`ckptRunSSHStream` (`checkpoint_remote.go`), `actionsRunSSH` (`actions_wizard.go`), `actionRunLocal`/`actionRunRemote` (`deploy_actions.go`), `sshStreamCommand` (`remote.go`), `watchLogStream` (`watch.go`), `rsyncCommand` (`push.go`), `listRemoteDirs` (`push_dest.go`), `lookPath` (`view.go`); `dockerInspectFn` in `internal/repl/health.go`.
- **End-to-end pattern** (`newFakeRemote`, `internal/cmd/deploy_source_test.go`): put a fake `ssh` shell script first on `PATH` that runs each "remote" command locally against a temp directory (real `rsync`, real `git push`), records compose calls in a log file instead of running them, and answers the module-state query; `t.Setenv("HOME", tmp)` isolates `~/.config/echo`. Tests skip when `git` or `rsync` is missing. Used by the deploy source, code-snapshot and watch tests; extend it rather than adding a new fake layer.
- A seam is added only where a test needs one; production code keeps calling the default (`= runSSH`).
- Interactive and TTY behaviour is verified by driving the real binary in tmux (tui-probe skill), not by unit tests; live checks against real servers are tracked in the [ledger](operations.md#live-verification-ledger).

## Done gate

A unit is done when, in order: `go build ./...`, `go vet ./...` and `go test ./...` pass; changed files are `gofmt`-clean (`gofmt -l` already lists 8 pre-existing files, e.g. `internal/cmd/actions_test.go`; do not reformat files you did not change); the Registry/help/flag cross-checks above pass; `CHANGELOG.md` has the entry; and the behaviour was exercised for real where it can be (a real terminal for TTY work, a real target for remote work). The usual closing line of a unit is "build/vet/test green; live verification pending": register the pending part in the [ledger](operations.md#live-verification-ledger); never claim "verified" without a source. No invariant in [architecture-and-traps](architecture-and-traps.md#invariants) may be violated; if a change alters boundaries, scope or conventions, update the owning knowledge file in the same change (boundaries -> architecture-and-traps, tokens and rendering -> ui, conventions -> this file, scope -> product).

## Dependencies and protected files

- No new Go dependency unless the unit requires it. Precedents: `go-difflib` became direct for `compare`; `crypto/pbkdf2` is stdlib since Go 1.24, so the `db-admin` hash needed none; `coder/websocket` (CDP) and `creack/pty` (shell capture) were added for their units.
- `go.sum` is managed by `go mod tidy`; never edit it by hand. `bin/` (build output) and `.unverified/` are git-ignored.
