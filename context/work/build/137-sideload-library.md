# Unit 137 · sideload-library
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-14.

## Goal

`sideload` with no arguments opens a picker over the user's own module library, already filtered to what fits the
target, so shipping a private module is `echo_cli sideload --from <t>`, pick, Enter. A library module can also be
named directly (`sideload forced_config_parameter --from <t>`); a path keeps working as today.

## Design

**The library is a list of local roots**, in the global config (it describes this machine, never a server):

```toml
[sideload]
library = ["~/Documents/Projects/odoo-forced-config-parameter", "~/Documents/Projects/odoo-modules"]
```

Edited with config-only setters, same pattern as `push --set-dest` (persist, log, exit; no SSH):
`sideload --library` (print the entries), `--library-add <path>`, `--library-rm <path>`. A path is stored
absolute with `~` kept; a missing directory is an `ErrUsage` on add and a WARNING when listing.

**Two kinds of root, decided per root:**

| Kind | Detected when | Content shipped |
|---|---|---|
| **versioned repo** | a git repository with at least one local branch named `<major>.0` (`17.0` … `20.0`) | the branch matching the target's series, read with `git archive <branch>` into a scratch dir: committed content only |
| **folder** | anything else (a plain folder, or a repo without series branches) | the working tree, as today |

Why: the module repos already follow the Odoo convention of one branch per series
(`odoo-forced-config-parameter`: `17.0`, `18.0`, `19.0`, `20.0`). Reading the branch by series means the right
code for the target with no checkout, no stash, and no risk of shipping an uncommitted experiment. A versioned repo
whose checkout of that branch is dirty logs a WARNING (`uncommitted changes are not shipped`).

**Discovery:** a module is a directory with `__manifest__.py` at depth 1 or 2 under the root (or in the archived
branch). Directories named `.git`, `__pycache__`, `node_modules` and dot-directories are skipped.

**Resolution order for a positional:** contains a path separator or names an existing directory → path (today's
behaviour); otherwise a module name looked up in the library for the target's series. Not found → `ErrUsage`
listing where it looked. Found in two roots → `ErrUsage` naming both (the picker shows both, with their root).
Several names: comma-separated or repeated positionals.

**The picker** (TTY only; no TTY and no positional → `ErrNonInteractive`, exit 2). It needs the target, so it runs
after `resolveRemoteShell` and the lock read (both read-only). Multi-select, tinted by stage, one row per module:

```
  forced_config_parameter   19.0.1.0.0   odoo-forced-config-parameter@19.0   sideloaded · current
  web_kiosk_tools           19.0.2.1.0   odoo-modules                        new
  hr_bonus_rules            19.0.1.0.0   odoo-modules                        sideloaded · changed
```

- Only modules whose series fits the target are listed (versioned repo: a branch exists; folder: a five-part
  manifest version of another series is hidden, shorter versions shown). A count of hidden modules is logged.
- Status from the target's lock (the server is the truth, not this machine's history): `new`,
  `sideloaded · current` (hash equal), `sideloaded · changed`, or `on target · <source>` when a deploy shipped a
  module of that name. Current rows are muted, as already-deployed commits are in the deploy picker, but still
  selectable.
- Esc cancels (exit 3), nothing touched.

**`sideload --available [--from t] [--json]`**: the same list without the picker, for scripts and for the
`odoo-probe` skill. Read-only.

**Lock provenance:** a module from a versioned repo records `ref=<branch>` and `sha=<commit>` in its lock entry,
next to the content hash, so `deploy --lock` says which commit of the library is running there.

Everything after the selection is Unit 133 unchanged: gates, lint, sync, override, `-i`/`-u`, lock.

## Implementation

- `internal/config/config.go`: `Sideload.Library []string` (`toml:"library"`) on the global config only; load
  expands `~`.
- `internal/cmd/sideload_library.go` (new): `libraryRoots`, `rootKind`, `discoverLibrary(ctx, roots, serie)`
  returning `[]libraryModule{name, version, root, branch, sha, dir}`; the `git archive` extraction into a scratch
  dir removed at the end of the run; the setters.
- `internal/cmd/sideload.go`: positional resolution (path or library name), picker entry when no positional,
  `--available`, `--library*` flags; `collectAddons` stays for paths.
- Picker: reuse the multi-select core in `internal/cmd/picker.go` (`pickerItem`, muted rows).
- `internal/repl`: flags, help, Tab completion of library module names after `sideload`.
- Tests: root kind detection, discovery depth and skips, series filtering for both kinds, archive of the right
  branch, ambiguity and not-found errors, status computation, setters with a temp `HOME`, a fake-remote run that
  ships `forced_config_parameter@19.0` from a temp versioned repo.
- Docs: README Sideload section, [deploy](../../knowledge/deploy.md#sideload), `CHANGELOG.md` `[Unreleased]`.

## Dependencies

- Unit 133.

## Verify when done

- [ ] Unit tests above; done gate from [code-standards](../../knowledge/code-standards.md#done-gate).
- [ ] `sideload --library-add ~/Documents/Projects/odoo-forced-config-parameter`, then
      `sideload --available --from fuentebuena-dev` lists `forced_config_parameter 19.0.1.0.0 …@19.0` with the
      status its content hash gives against the copy running there since 2026-10-14 (a scratch edit with the same
      pinned key, expected `sideloaded · current`).
- [ ] `sideload --from fuentebuena-dev --resync` in a TTY opens the picker; selecting the module ships the `19.0`
      branch content, runs `-u`, and `deploy --lock` shows `ref=19.0` with the commit.
- [ ] No TTY, no positional: exit 2 with a hint naming `--available` and the positional form.
