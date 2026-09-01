# Unit 115: finding the addon, wherever it lives

## Goal

A repo that keeps its modules in a subfolder — `oehealth_modules_19/`, one of
several `addons_path` entries the server already loads — must work from the repo
root, with no configuration. Today `push` and `deploy` refuse it, `deploy` refuses
it even when it *is* configured, and one spelling of the argument makes `push`
report success while syncing to a directory Odoo shadows. This unit collapses the
four independent one-level lookups into a single resolver, gives that resolver a
bounded recursive fallback, and stops the silent case.

## Why this shape and not "configure it"

The concept already exists: `AddonsPaths` + `AddonsMode` in the project config,
and `modules --config` to pick the folders. The bug report's repo can't reach any
of it — `modules` is not in `projectlessOneShot`, so a link-only repo with no
`docker-compose.yml` gets `not inside a project` and there is no non-interactive
flag to write the key instead. Fixing only that would leave every new repo needing
a setup step before the first `push`.

Odoo itself needs no such step: it is handed several `addons_path` entries and
looks in all of them. Discovery matches that, and it is the only option that makes
the *first* command someone runs in a repo work. Configuration still wins when
present — discovery is the fallback, never the default path — so no project that
works today changes behavior, and the scan is only paid in the case that currently
fails outright.

The four call sites are the deeper defect. `push` resolves through
`resolveModuleDir` (config-aware), `deploy` through `isAddonDir` (config-blind),
`lint` through its own `hostAddonsRoots`/`moduleDir` pair, and the picker through
`listAvailableModules`. They disagree, and Case C below is precisely two of them
disagreeing. One resolver is what keeps them from drifting apart again.

## Behavior

### A. Discovery as a fallback

`resolveAddon(cfg, root, name)` tries, in order:

1. `cfg.AddonsPaths` when set and `AddonsMode != conf` (conf-mode paths are
   container paths and cannot be walked on the host — the existing
   `hostAddonsRoots` rule).
2. The conventional default `[".", "addons", "custom"]`.
3. **New:** a bounded walk from `root`, depth 3, keeping directories that contain
   `__manifest__.py`, skipping hidden directories and the existing `skipDirs` set
   (`node_modules`, `__pycache__`, `.git`, `vendor`, `.venv`, `bin`, …). The walk
   does not descend into a directory that is itself an addon.

Nothing is persisted. Discovery runs per invocation and leaves the config alone:
writing `AddonsPaths` as a side effect of a `push` would silently pin a snapshot
of the tree and change what every later command sees.

**Ambiguity is an error.** Two directories under different roots holding the same
module name → `ErrUsage` naming both paths. Picking one silently is the class of
bug this unit exists to remove.

### B. `deploy --modules` resolves like `push`

`isAddonDir(root, name)` is deleted. [deploy.go:930](../../internal/cmd/deploy.go)
validates through the shared resolver, so a module in a subfolder deploys, and a
configured `AddonsPaths` finally applies to `deploy` at all.

### C. A path argument resolves to its module name

`push oehealth_modules_19/oehealth_consultation_extra` currently passes validation
(`filepath.Join(root, ".", "a/b", "__manifest__.py")` exists) and then flows the
slash into `path.Join(destBase, m)`, producing
`addons/oehealth_modules_19/oehealth_consultation_extra` — a path shadowed by the
earlier `/mnt/extra-addons` entry, so the push reports success and the change
never loads.

An argument containing a separator is now interpreted as a path relative to `root`:
if it holds a `__manifest__.py`, the resolver returns its **basename** as the
module name and its parent as the addons dir. Otherwise `ErrUsage`. The module
name that reaches `pushModuleSet`, the logs and the remote destination never
contains a separator.

This keeps shell tab-completion usable — the likely origin of the argument in the
report — while making the resolved name explicit in the `syncing module=…` line.

### D. Declaring addons paths without a compose project

- `modules --addons-path <a,b,c>` — non-interactive, writes `AddonsPaths` and pins
  `AddonsMode = host`, then reports what it saved. `--addons-path ""` clears the
  key and returns the project to defaults + discovery.
- `modules` joins `projectlessOneShot` in [main.go:214](../../main.go), so both the
  listing and the config write work in a link-only repo.

`modules --config` (the huh form) is unchanged and stays the interactive path.

### E. The project root resolves to the git root

[project.FindRoot](../../internal/project/root.go) gains a step before its `cwd`
fallback: when no `docker-compose.yml` exists in any parent, ask
`git rev-parse --show-toplevel` and use that. A subdirectory of a link-only repo
then hashes to the same `ProjectKey` as its root, so `link`, `AddonsPaths`, the
push destination and the deploy history survive a `cd`.

**Migration.** All per-project state is `<dir>/<key>.toml` except `cmd-logs/<key>/`,
so a rename covers it. When the git-root step fires, `cwd != root`, and
`projects/<key(root)>.toml` does **not** exist, walk from `cwd` up to `root` for the
first directory whose key does have one, and rename its entries across `projects`,
`deploy-history`, `last-updates`, `last-sequences`, `checkpoints` and `cmd-logs`.
One `INFO` line names the source and destination.

The guard conditions are what make it safe: it only fires when the destination is
empty, so a repo already linked from its root is untouched; it only searches the
`cwd → root` chain, so it never guesses a project; and it is idempotent, because
after the first run the destination exists. Two separately-linked subdirectories of
one repo is the uncovered case — the deepest from `cwd` wins and the log line says
which moved.

## Implementation

### `internal/cmd/addons.go` (new)

The single resolver. `addonsRoots(cfg, root)` for the configured/default roots,
`discoverAddonsRoots(root)` for the bounded walk, `resolveAddon(cfg, root, name)`
returning `(dir, module, error)`, and `listAddons(cfg, root)` for the picker.
Errors: `ErrModuleNotFound`, and `ErrUsage` for ambiguity and for a bad path
argument.

### Rewiring

- [i18n.go:84](../../internal/cmd/i18n.go) — `resolveModuleDir` becomes a thin
  wrapper over `resolveAddon`, keeping its signature for `i18n`/`push` callers.
- [modules.go:624](../../internal/cmd/modules.go) — `listAvailableModules` delegates
  to `listAddons`.
- [deploy.go:2011](../../internal/cmd/deploy.go) — `isAddonDir` removed; the
  validation loop at 930 uses `resolveAddon` and rewrites `p.modules[i]` to the
  resolved name.
- [push.go:154](../../internal/cmd/push.go) — same normalization, in place, before
  `--dirty` merges and before any SSH.
- [lint.go:92-118](../../internal/cmd/lint.go) — `hostAddonsRoots`/`moduleDir` fold
  into the shared roots; the conf-mode rule moves into `addonsRoots`.

### `modules --addons-path`

Parsed in `RunModules` alongside `--config`, before `resolveModules`. Splits on
comma, trims, rejects absolute paths and anything escaping `root` with `ErrUsage`.

### Root and migration

`internal/project/root.go` gains `FindRootGit`; `internal/config/migrate.go` (new)
holds `MigrateProjectKey(oldRoot, newRoot)` over the six locations. Called from
`main.go` right after the root resolves and before `config.Load`.

## Dependencies

None. `git rev-parse` is already a hard requirement of `deploy`/`promote`; its
absence just means the `cwd` fallback stands.

## Verify when done

- [ ] From the repo root, `push <mod> --from <target> --dry-run` finds a module in
      a subfolder and targets `addons/<mod>` — not the mirrored subpath — because
      the remote probe still resolves the existing location first.
- [ ] `deploy --modules <mod>` resolves identically; a configured `AddonsPaths`
      applies to `deploy`.
- [ ] `push sub/<mod>` logs `module=<mod>` with no separator, and a `sub/` that is
      not an addon is `ErrUsage`.
- [ ] The same module name under two roots fails naming both paths.
- [ ] `modules --addons-path a,b` persists and works in a repo with no
      `docker-compose.yml`.
- [ ] `link --show` from a subdirectory reports the root's binding, and the
      migration moves deploy history and checkpoints, not just the project toml.
- [ ] Migration is a no-op when the destination toml exists; running it twice
      changes nothing.
- [ ] A project with `docker-compose.yml` and configured `AddonsPaths` resolves
      through the configured roots without the walk running.
- [ ] `go build ./...` and `go test ./...` pass.
