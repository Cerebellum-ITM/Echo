# Unit 103: `promote --show-branch` — query the configured promote destination

## Goal

Add a `--show-branch` flag to `promote` that **reports the effective promote
destination branch without mutating anything**: which branch is configured,
where that value comes from (project config vs global config vs not
configured), and whether a worktree currently has it checked out.

It is the read counterpart of `--set-branch` (Unit 97). Today the only ways to
learn the configured branch are grepping `~/.config/echo/global.toml` /
`projects/<key>.toml` by hand or provoking a `--dry-run` and reading its
`dest=` field / usage error — both workarounds. Headless agents (the
`odoo-probe` skill) need a first-class, read-only, no-TTY answer to "where
would `promote` land right now?".

## Behavior

```
promote --show-branch          # report and exit — no git mutation, no picker, no SSH
```

- **Configured** → one INFO line with structured fields and exit `0`:

  ```
  INFO … promote branch  branch=develop source=project worktree=/path/to/dvz-develop
  ```

  - `source=` is `project` when `projects/<key>.toml` set it, `global` when it
    came from `global.toml` (project overrides global, as loaded today).
  - `worktree=` is the path of the worktree that has the branch checked out;
    when no worktree does, the field is `worktree=none` and a second INFO line
    hints the fix (`git worktree add <path> develop` / `--create-dest`).

- **Not configured** → a WARNING line and exit `1`:

  ```
  WARNING … no promote branch configured  hint=promote --set-branch <name> | promote --to <branch>
  ```

  Exit `1` (not `2`/`ErrUsage`) so scripts can branch on
  `if echo_cli promote --show-branch`: the invocation is valid, the answer is
  "nothing configured". This is a deliberate, documented deviation from the
  usual "1 = execution error" reading.

- **Standalone flag**: like `--set-branch`, `--show-branch` rejects every
  other promote argument (`--dirty`, positionals, `--to`, `--commits`,
  `--create-dest`, `--dry-run`, `--force`, and `--set-branch` itself) with
  `ErrUsage`.
- **Projectless and headless**: works from any git worktree (it only needs the
  config plus `git worktree list`; run it outside a repo and the `worktree=`
  lookup degrades to `worktree=none` — the config half still answers). No TTY
  required, ever. Fully read-only: no config write, no git mutation, no
  cmd-log record (it is a query, mirroring `logview`).

## Design

- `promoteArgs` gains `showBranch bool`; `parsePromoteArgs` accepts
  `--show-branch` and, symmetric to the existing `--set-branch` guard, errors
  with `ErrUsage: --show-branch takes no other arguments` when anything else
  is present (including `--set-branch` — the two config verbs are mutually
  exclusive).
- **Provenance**: `config.Config` today collapses global/project into a single
  `PromoteBranch` string, losing the source. Add
  `PromoteBranchSource string` (`"global"` / `"project"` / `""`) set at the two
  assignment points in `Load` (`config.go:446` global, `config.go:520`
  project). No re-read of the TOML files at query time — the loaded config is
  the truth, same as `promote` itself uses.
- `RunPromote` short-circuits on `p.showBranch` right next to the
  `--set-branch` branch, before `gitToplevel`:
  - `branch := opts.Cfg.PromoteBranch`; empty → WARNING + sentinel error that
    maps to exit `1` (a distinct `ErrNotConfigured = errors.New("not
    configured")`, translated by the dispatcher like `ErrUsage`→2 is today —
    reuse the existing exit-mapping seam).
  - Non-empty → best-effort worktree lookup: `gitToplevel(ctx, opts.Root)`;
    on success `gitWorktrees` + `worktreeForBranch` (all Unit 97 helpers,
    same package) fill `worktree=`; any git error (not a repo) degrades to
    `worktree=none` without failing the command.
- REPL: add `--show-branch` to `commandFlags["promote"]`
  ([commands.go:55](internal/repl/commands.go:55)) and one line to the promote
  help text.
- Log lines follow the Odoo-style structured `key=value` idiom (no new output
  format, no `--json` — the fields are already machine-parseable).

## Out of scope

- Exposing the promote branch in `link --show` / any status surface (this
  unit is the dedicated query; composing it elsewhere is a later decision).
- A generic `config show` command.
- Any change to `--set-branch`, destination resolution, or the promote modes.

## Tests

- `TestParsePromoteShowBranch`: bare `--show-branch` parses; combined with
  `--dirty` / a positional / `--to x` / `--set-branch x` → `ErrUsage`.
- `TestPromoteShowBranch` (table, seam-injected config + worktrees):
  - project-sourced branch with a checked-out worktree → INFO with
    `source=project`, `worktree=<path>`, nil error.
  - global-sourced branch, no worktree → `worktree=none` + hint line.
  - unconfigured → WARNING + `ErrNotConfigured`.
  - git failure (not a repo) with configured branch → still reports,
    `worktree=none`.

## Verify when done

- [ ] `echo_cli promote --show-branch` in a repo with `[promote] branch` set
      prints the INFO line with correct `branch=`/`source=`/`worktree=` and
      exits 0.
- [ ] Same command with no configured branch prints the WARNING + hint and
      exits 1 (scriptable in `if`).
- [ ] `promote --show-branch --dirty` (and each other combination) fails with
      usage exit 2; plain `promote` flows are untouched.
- [ ] Works headless (`</dev/null`) with no picker and no SSH traffic.
- [ ] `go build ./...` and `go test ./...` pass; CHANGELOG `[Unreleased]`
      gains the `Added` entry in the same commit.
