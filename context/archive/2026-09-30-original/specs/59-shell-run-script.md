# Unit 59: `shell-run` — run a `.py` through Odoo shell and copy the output

## Goal

Add a `shell-run [<file>]` command that runs a local Python script through the
Odoo shell by piping it to stdin —the equivalent of
`odoo-bin shell -d <db> --no-http < investigar_2131.py`— with a file picker to
choose the `.py` and auto-copy of the captured output. Fills the gap between the
interactive `shell` (PTY) and the need to run an ad-hoc investigation/migration
script in one shot and grab its output.

## Decisions

1. **New command `shell-run [<file>]`** — leaves `shell` (interactive) intact.
   No positional file → opens a `.py` picker.
2. **Source of `.py`:** a top-level `scripts/` folder is auto-detected (no
   config); the project key `scripts_dir` overrides with a custom path
   (relative to the project, or absolute); otherwise the project root. Always
   top-level, non-recursive (so the addons' thousands of `.py` aren't scanned).
3. **Auto-copy the output** on completion (`copied N lines to clipboard`).
   Copies **only the script's stdout** — the `print(...)` results — dropping
   the Odoo shell's boot/init log lines (filtered by "is this an Odoo-format
   log line"). The full transcript (boot included) stays available via
   `copy-last`. `--no-copy` opts out.

## Design

Flow: `shell-run` → resolve script (arg or picker) → prod confirm → `docker
compose exec -T <odoo> odoo shell -d <db> --no-http < script.py` → stream each
line through `emitStreamLine` (Odoo coloring, same as `update`) → on EOF, copy
`lastOutput` to the clipboard.

Runs **without a TTY** (`exec -T`): the stdin pipe works and output arrives
plain (and `emitStreamLine` strips any residual ANSI from Unit 58).

## Implementation

- `internal/config/config.go`: add `ScriptsDir string` to `Config` and
  `projectFile` (`toml:"scripts_dir"`); map in `Load`, persist in `SaveProject`.
- `internal/odoo/cmd.go`: new `Shell(c Conn) Cmd` = `odoo shell` + `c.flags()` +
  `--no-http`; `RunOdooShell` reuses it.
- `internal/docker/exec.go`: new `ExecWithStdin(ctx, composeCmd, dir, container,
  argv, stdinPath, onLine)` — opens `stdinPath`, sets `cmd.Stdin`, `exec -T`,
  combines stdout/stderr, streams via `streamLines` (pattern from `RestoreSQL`).
- `internal/cmd/shellrun.go`: `ShellScriptOpts` + `RunShellScript` (validates
  odoo config, `maybeConfirmProd(..,"shell-run")`, builds `Conn` like
  `RunOdooShell`, `argv := odoo.Shell(conn)`, calls `docker.ExecWithStdin`).
- `internal/repl/shellrun.go`: `pythonScriptsIn` (clones `echoRecipesIn` for
  `*.py`, newest-first), `pickScriptFile`, `resolveScriptArg` (name→ScriptsDir,
  path→projectDir, must be an existing `.py`), `scriptsDir()` resolver (explicit
  `scripts_dir` → `scripts/` if present → root), `scriptOutputLines` (filters
  the Odoo-log boot lines out of the copied payload via `lineLevel`), and
  `runShellRun` (picker/arg → stream → auto-copy script output unless
  `--no-copy` → finalize).
- `internal/repl/repl.go`: dispatch `case "shell-run"` + add to `dispatchNames`.
- `internal/repl/commands.go`: add to `Registry` and `commandFlags`
  (`--no-copy`).
- `internal/repl/repl.go` help: add `shell-run` entries under the Shell section.

## Dependencies

- Reuses `emitStreamLine`, `lastOutput`/`clipboard.WriteAll`, `cmd.PickOne`,
  `recipeEntry`/`sortRecipesByCreation`/`fileCreated`, `maybeConfirmProd`.

## Verify when done

- [ ] `go build/vet/test ./...` green; registry cross-check (Registry ⇄ help ⇄
      dispatch) stays green.
- [ ] `shell-run` with no arg lists the `.py` in ScriptsDir (default root) and
      runs the picked one; `shell-run x.py` runs directly.
- [ ] Output streams Odoo-colored like `update`; on completion `copied N lines`
      appears and the clipboard holds **only the script's stdout** (no boot/init
      logs); `copy-last` still recovers the full transcript.
- [ ] A `scripts/` folder in the project root is picked up with no config;
      `scripts_dir` overrides it.
- [ ] `shell-run --no-copy x.py` does not copy. Prod-stage DB prompts first.
- [ ] A missing file / non-`.py` arg fails with a clear line, not a crash.
