# Unit 99: `i18n-pull --to-worktree` — land the pulled `.po` in another worktree

## Goal

Add a `--to-worktree` flag to `i18n-pull` that **redirects where the pulled
`.po` files are written**: instead of the current worktree, into a **sibling
git worktree** of the same repo — chosen from a picker (bare `--to-worktree`)
or named directly (`--to-worktree=<branch>`).

Nothing else about `i18n-pull` changes: it still exports each module's
translations from the remote Odoo instance over SSH (read-only on the remote)
and writes `<addons>/<mod>/i18n/<lang>.po` locally. Only the **local root** the
files land in is switched.

### Why this exists (the hole it closes)

The instance is fed from a **single deploy branch** (see Unit 97). The dev
runs `i18n-pull` **from the deploy-branch worktree** — that is where the live
instance / DB lives, so that is where the translations can be exported from.
But the module *source* the dev edits and commits lives in a **feature
worktree** at a different path.

Without this flag the pulled `.po` lands in the deploy worktree, and the dev
has to hand-move a single file across worktrees — exactly the friction Unit 97
removed for module code. `promote` is the wrong tool here: it moves whole dirty
modules the other direction (feature → deploy). For "one generated file, deploy
→ feature worktree" a redirect on `i18n-pull`'s own output is far simpler.

## Behavior

- **Default (no flag)**: unchanged — files land in the current worktree
  (`opts.Root`), via `pullDest(cfg, opts.Root, mod, lang)`.
- **`--to-worktree` (bare)**: opens a picker (`PickOne`) over the repo's other
  worktrees (`git worktree list`), excluding the current worktree and detached
  checkouts. The chosen worktree's path becomes the local root; each `.po`
  lands at `<that-worktree>/<addons>/<mod>/i18n/<lang>.po`. TTY-only.
- **`--to-worktree=<branch>`**: no picker — resolve the worktree that has
  `<branch>` checked out (matched by branch, then by path). Headless-friendly.
- **Fail-closed**: a bare `--to-worktree` without a terminal errors asking for
  `--to-worktree=<branch>`; an unknown branch / no other worktree → `ErrUsage`.

The redirect is resolved **up front** (right after arg parse, before any SSH
work) so a bad target fails fast. An INFO line (`writing into worktree
path=…`) is emitted when the destination differs from the current worktree.

## Design

- `i18nPullArgs` gains `toWorktree string` + `pickWorktree bool`. The bare
  `--to-worktree` sets `pickWorktree`; the `=` form sets `toWorktree`. The bare
  form deliberately does **not** consume the next token (it stays a module
  positional) — an explicit branch must use `--to-worktree=<b>`.
- `resolvePullWorktree(ctx, opts, p)` returns the destination root: `opts.Root`
  when neither is set; otherwise `gitToplevel` + `gitWorktrees` (reused from the
  promote unit, same package) → branch/path match or `pickPullWorktree`.
- `pickPullWorktree` reuses the promote picker shape (label `<path>  (<branch>)`,
  current + detached excluded) but is `i18n-pull`-local (no promote-branch
  persistence — this is a one-off redirect, not a saved default).
- The pull loop writes to `pullDest(cfg, destRoot, mod, lang)` instead of
  `opts.Root`. `pullDest`'s existing host-vs-fallback logic is unchanged and
  now resolves relative to `destRoot`.
- Flag registered in `commandFlags["i18n-pull"]` and documented in the REPL
  help (`--to-worktree[=<branch>]`).

## Out of scope

- The interactive **build-mode** composer (`runI18nPullBuild`) does not offer
  `--to-worktree` (like `--all`/`--installed`, it is a direct-invocation flag).
- No change to `i18n-export` / `i18n-update` (local commands; `i18n-export`
  already has `--out <path>` for arbitrary destinations).

## Tests

- `TestParseI18nPullToWorktree`: bare → `pickWorktree`; `=<branch>` →
  `toWorktree`; bare form keeps the following token as a module positional.
