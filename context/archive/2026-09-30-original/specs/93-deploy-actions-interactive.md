# Unit 93: `actions` — interactive management + exec_path for deploy actions

## Goal

Two tightly-coupled additions to the Unit 92 deploy actions. (1) A new
`exec_path` field on every action: the directory its `run` command
executes in — the compose root (where the `.yml` lives; today's implicit
behavior), the addons directory, or any other path. (2) A new `actions`
command with a full interactive management flow (`list`/`add`/`edit`/
`rm`): a `huh` wizard that walks name → phase → where → exec_path →
run, validated with `ValidateDeployActions`, where the exec_path step
offers a **directory picker matched to the action's `where`** — the
Unit 91 remote FS browser (`pickRemoteDir`, over SSH) for remote
actions, a local equivalent for local ones. Persists to the local
`[[deploy.actions]]`, with an optional confirmed upload of the section
to the server's project profile.

## Design

### `exec_path` — where an action runs

```toml
[[deploy.actions]]
name      = "build-image"
phase     = "post_push"
where     = "remote"
exec_path = "docker"          # optional; default "" = project root
run       = "docker build -t myodoo:latest ."
```

Semantics (mirrors the Unit 91 `[push] path` rules):

- **Empty / absent** → today's behavior, unchanged: remote actions run
  at `remotePath` (the compose root, where the `.yml` lives), local
  ones at the project root.
- **Relative** → joined under that root (`path.Join(remotePath, p)`
  remote / `filepath.Join(opts.Root, p)` local).
- **Absolute** → used as-is (a build context outside the compose tree).

No `path_addons` keyword in the stored config — the stored value is
always a plain path, so the executor stays trivial and the TOML remains
self-describing. "Addons dir" is a **wizard preset**, not a config
enum: when the user picks it, the wizard resolves the actual addons
path (first entry of the profile's/config's addons paths) and stores
the resolved literal. Presets offered in the wizard:

1. **Project root** (default) — stores `""` (omits the key).
2. **Addons directory** — remote: first relative entry of
   `prof.AddonsPaths` (else `addons`); local: first entry of
   `cfg.AddonsPaths` (else `addons`). Stored resolved.
3. **Pick a directory…** — opens the picker matched to `where` (below).
4. **Type a path** — free-text input, validated non-empty.

**Wizard mechanics.** `huh` forms and the bubbletea picker are separate
programs, so the wizard runs **field by field** (one form per step, the
established confirm→picker chaining), not as one monolithic form: name →
phase → where → exec_path → run. Choosing "Pick a directory…" closes the
exec_path form, runs the picker program, and resumes with the selection;
`esc` in the picker returns to the preset select (it does not abort the
wizard). The remote target is resolved **lazily, once** — only when a
remote picker/preset first needs it.

**Normalization on save** (the `pickAndMaybePersist` rule from Unit 91):
the picker returns an absolute path; if it falls **under** the
corresponding root (`remotePath` / project root — `underPath`), it is
stored **relative** (portable across servers with different layouts);
outside the root it stays absolute. In `edit`, the preset select arrives
pre-selected: `""` → Project root; a value matching the resolved addons
dir → Addons directory; anything else pre-fills "Type a path" with the
picker still available.

Validation: `ValidateDeployActions` stays as is (exec_path has no
invalid values — any path string is legal; empty means root). The
executor is where resolution happens.

### Directory pickers by `where`

- **`where = "remote"`** → reuse Unit 91's `pickRemoteDir` verbatim
  (level-by-level SSH browser, stage-tinted, `· use this directory` /
  `.. (up)`, starts at `remotePath`, can climb above it). Needs a
  resolved target: the wizard resolves it once via
  `resolveRemoteShell` (using the directory's link / `--from`) when the
  user first enters a remote-picker step; if no remote target resolves,
  the picker option is disabled with a note and the user types the path.
- **`where = "local"`** → new `pickLocalDir(startDir)`: the same
  level-by-level UX over the local filesystem (`os.ReadDir`, dirs only,
  same synthetic rows, same fuzzy picker core, tinted by the local
  stage). Starts at the project root, can climb to `/`. Shares
  `dirPickerEntries` from Unit 91 — only the listing source differs.

Both pickers are TTY-only (`requireTTY`); the wizard itself already
requires a TTY, so headless is fail-closed at the command level with
the standard hint.

### The `actions` command

```
actions                → styled list (default subcommand)
actions list           → same
actions add            → wizard (create)
actions edit [<name>]  → wizard pre-filled (picker over names when omitted)
actions rm [<name>]    → confirm + delete (picker when omitted)
actions --json         → machine-readable list (with `list`)
```

- **`list`** — a styled table in the `modstate`/`ps` pattern: columns
  `name · phase · where · exec_path · run` (run middle-truncated to the
  width), header in accent, plus a `source` footer line naming where the
  effective list came from (`server` — read-only note — or `local`).
  When the resolved list is the **server's**, the table renders it but
  `add`/`edit`/`rm` warn that local edits won't take effect until the
  server list is removed or the local one is uploaded (the Unit 92
  wholesale rule, restated interactively).
- **`add`** — `huh` wizard, one group per field:
  1. `name` (input, validated non-empty + unique against the local list),
  2. `phase` (select of the four, with one-line descriptions),
  3. `where` (select local/remote),
  4. `exec_path` (select of the four presets above; picker/typed input
     per choice),
  5. `run` (input, validated non-empty).
  On confirm: append to `cfg.DeployActions`, `ValidateDeployActions`
  on the whole list, `config.SaveProject`, close with
  `echo.actions: action added name=<n> phase=<p>`.
- **`edit <name>`** — same wizard pre-filled with the existing values;
  saves in place (order preserved). Editing is local-list only.
- **`rm <name>`** — red-styled confirm (`BuildHuhTheme`), removes and
  saves; `--force` skips the confirm (headless-friendly like the other
  destructive verbs); non-TTY without `--force` fails closed.
- **Upload to server (optional).** After a successful `add`/`edit`/`rm`,
  if the project has a resolvable remote target, offer (confirm,
  default **No**): "Upload the local actions to the server profile?" —
  on yes, rewrite the `[[deploy.actions]]` section of the server's
  `projects/<key>.toml` over SSH (read file → replace/append section →
  write back via `sh -c 'cat > …'`, the `writeContainerConf` stdin
  pattern), preserving every other key. A prod-stage target gets the
  red confirm first. This is the explicit, gated exception to
  "Echo doesn't mutate remote config" — it exists because the wholesale
  rule makes the server list authoritative, and hand-syncing TOML over
  SSH is exactly the toil Echo removes.

### Executor changes (Unit 92 core)

`runDeployActions` honors `exec_path`:

- Remote: `cd <resolveActionDir(remotePath, a.ExecPath)>` instead of
  the fixed `remotePath` in `actionRunRemote`.
- Local: `c.Dir = resolveActionDir(opts.Root, a.ExecPath)` in
  `actionRunLocal` (filepath variant).
- New pure helper `resolveActionDir(root, execPath)` (path/filepath
  pair or a single slash-path helper used carefully) with the
  empty/relative/absolute rules above.
- The per-action `running` log line gains `dir=<resolved>` when
  exec_path is set, so a wrong path is diagnosable from the stream.

## Implementation

### `internal/config/config.go`

- `DeployAction` gains `ExecPath string`; `deployActionFile` gains
  `ExecPath string \`toml:"exec_path"\`` (decode + `deployActionsFrom`).
- New `SaveDeployActions(cfg)` isn't needed — `SaveProject` gains the
  `[[deploy.actions]]` serialization (emit only when the list is
  non-empty, like `[push]`/`[connect]`), so the wizard persists through
  the standard path.

### `internal/cmd/deploy_actions.go`

- `actionRunRemote`/`actionRunLocal` take the resolved dir (or read
  `a.ExecPath` + root themselves via `resolveActionDir`); `running`
  frame logs `dir=` when non-root. Seams unchanged shape-wise (tests
  updated for the extra data).

### `internal/cmd/actions.go` — new file

- `ActionsOpts{Cfg, Root, Args, Palette, Log}` (ModulesOpts shape).
- `parseActionsArgs(args)` → subcommand (`list` default, `add`,
  `edit`, `rm`), optional name positional, `--json`, `--force`, plus
  `--from <t>`/`--remote` consumed for the upload/remote-picker target.
- `RunActions(ctx, opts)`:
  - `list`: `resolveDeployActions(prof-if-resolvable, cfg, false)`
    (target resolution best-effort — a projectless/unlinked dir just
    shows the local list with `source=local`); emit table or `--json`.
  - `add`/`edit`: wizard (`actionWizard(existing *DeployAction)`), the
    exec_path step calling `pickRemoteDir` (resolving the target
    lazily, once) or `pickLocalDir` per `where`; validate; save;
    optional upload offer (`offerUploadActions`).
  - `rm`: name from positional or single-select picker over local
    names; confirm; save; optional upload offer.
- `pickLocalDir(startDir, palette, stage)` — local mirror of
  `pickRemoteDir` sharing `dirPickerEntries`; listing via `os.ReadDir`
  filtered to dirs (skip dotdirs).
- `resolveActionDir(root, p string) string` + slash/filepath handling.
- `offerUploadActions(ctx, rsc, actions)` — render the actions to a
  TOML fragment (`toml.Marshal` of a `{Actions []deployActionFile}`
  wrapper), read the remote `projects/<key>.toml`, splice the section
  (drop existing `[[deploy.actions]]` blocks, append the new ones),
  write back over SSH; red confirm on prod.

### `internal/repl/actions.go` — new file

- `runActions` wrapper: start frame → `RunActions` → finalize
  (ErrUsage→usage exit, cancel path, standard failure) — the
  `checkpoint` wrapper shape. Table rendering via the shared `pad`
  helpers; `run` column truncated with the middle-ellipsis helper.

### Registration

- `Registry`/`dispatchNames`/dispatch case; help — Docker section next
  to `deploy`: `{"actions [list|add|edit|rm]", "Manage [[deploy.actions]]
  interactively"}` + rows for `--from <t>`/`--remote`, `--json`,
  `--force`.
- `commandFlags["actions"] = {"--from", "--remote", "--json", "--force"}`.
- `main.go` `projectlessOneShot`: `actions` returns false (it reads and
  writes the project profile).
- README: extend the "Deploy actions" section — `exec_path` semantics +
  the `actions` command table + upload note. CHANGELOG `[Unreleased]`
  `### Added` (actions command + exec_path).

## Dependencies

- Unit 91 (`pickRemoteDir`, `dirPickerEntries`) and Unit 92
  (`DeployAction`, `resolveDeployActions`, executor) — both landed.
- No new packages (`huh`, `toml`, SSH plumbing already present).

## Verify when done

- [ ] `actions add` walks name→phase→where→exec_path→run; picking
      "Addons directory" stores the resolved literal path; "Pick a
      directory…" opens the remote SSH browser for `where=remote` and
      the local browser for `where=local`; the result lands in the
      project's `[[deploy.actions]]` via `SaveProject`.
- [ ] An action with `exec_path = "docker"` runs its command in
      `<root>/docker` (local) / `<remotePath>/docker` (remote); absolute
      paths run as-is; absent exec_path is byte-identical to Unit 92
      behavior; the `running` frame logs `dir=` when set.
- [ ] `actions` / `actions list` renders the styled table with the
      correct `source` (server list shown read-only with the wholesale
      warning); `--json` emits the machine shape.
- [ ] `actions edit <name>` pre-fills and saves in place preserving
      order; `actions rm <name>` confirms (red), `--force` skips,
      non-TTY without `--force` fails closed.
- [ ] After a local mutation, the upload offer (default No) rewrites
      only the `[[deploy.actions]]` section of the server profile over
      SSH, preserving the rest of the file; prod targets get the red
      confirm; declining leaves the server untouched.
- [ ] Tests: `parseActionsArgs`, `resolveActionDir`
      (empty/relative/absolute × local/remote roots), wizard field
      validation helpers, `pickLocalDir` entry building (shared
      `dirPickerEntries`), TOML section splice for the upload
      (drop-and-append preserving unrelated keys), executor honoring
      `ExecPath` via the existing seams, round-trip
      `SaveProject`/`Load` with `[[deploy.actions]]` + `exec_path`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
