# Unit 80: `compare` — local file vs its Docker copy

## Goal

New `compare [<mod>] [--from <t>|--remote] [--copy]` command: pick a
module file from the **local checkout** (same module→file picker chain as
`view`) and diff it against **the copy running inside a Docker
container** — the local Odoo container by default, or a remote target's
container with the shared `--from`/`--remote` flags. The unified diff is
computed in Go (no external `diff` binary, local or remote) and rendered
through bat (`--language=diff`) with the internal plain fallback; `--copy`
puts the raw diff on the clipboard. Read-only on both sides.

## Design

**Semantics: deployed = old, local = new.** The container copy is the
diff's left/`---` side and the local checkout file the right/`+++` side,
so `+` lines read as "my local changes not yet in the container" — the
question the command exists to answer (pre-`deploy` sanity check, or "did
my volume mount actually pick this up?"). Labels:
`--- <target>/<mod>/<rel>` and `+++ local/<mod>/<rel>`, where `<target>`
is the resolved connect-target name for remote runs and `docker` for the
local container.

**Local side is always the host checkout.** The file is resolved via the
host-mode addons search (`resolveModuleDir`-style: configured
`AddonsPaths`, falling back to `.`/`addons`/`custom`), never the
container — a module with no host copy is an error
(`module %q not found in local addons paths`), because there is nothing
to compare *from*.

**Container side follows the transport convention.** Without flags, the
counterpart is read from the **local** Odoo container via `docker.Exec`
`cat` (works in both addons modes; in mount mode the two sides are the
same bind-mounted file and the diff is trivially empty — that's still a
useful "the mount is live" confirmation, reported as `identical`).
`--from <t>` / `--remote` reads the counterpart from the **remote**
container/host using Unit 79's `resolveRemoteView` +
`remoteReadModuleFile` primitives — same probe order (remote hostFS →
container), same profile resolution, no prod gate (read-only, like remote
`view`/`logs`).

**Pickers = view's, sourced from the local checkout.** No positional →
the standard single-select module picker (`"Module to compare"`). File
picker (`"File in <mod>"`) lists the **local** module's files
(host `moduleFiles` walk + `skipViewPath`), because the local file is the
subject being compared. A file that exists locally but not in the
container diffs against **empty** with a WARNING
(`file missing in container — showing full file as added`), which is
exactly what an undeployed new file should look like.

**Diff engine: `go-difflib`, already in the tree.**
`difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: container, B: local,
FromFile: <label>, ToFile: <label>, Context: 3})` — promotes
`github.com/pmezard/go-difflib` (already in `go.sum` via testify) to a
direct dependency. Empty diff → no pager; a single
`echo.compare … result=identical` INFO line. Non-empty → render through
`ShowWithBat(name+".diff", diffText)` so bat picks diff highlighting and
pages; fallback prints through the themed Line channel (captured for
copy-last) with no manual coloring (standards: no raw escape codes;
bat does the coloring, internal fallback is plain).

**Outcome frame.** `echo.compare` log lines mirror `echo.view`:
`module=… file=… [from=…]` plus `result=identical|different` and
`via=bat|internal` when displayed. Exit 0 in both outcomes (it is a
viewer, not a CI check); errors exit 1 as usual.

## Implementation

### `internal/cmd/compare.go` — new file

- `CompareOpts{Cfg, Root, Args, Palette}` (mirror `ViewOpts`).
- `CompareResult{Module, RelPath, From, Diff string, Identical, Copy bool}`.
- `RunCompare(ctx, opts)`:
  1. `remoteFlagsIn` + strip (`--from`/`--from=`/`--remote`), parse
     `--copy`; unknown flags error (view's rule).
  2. Resolve module (positional or picker) and the **local** base dir;
     list local files, pick one.
  3. Read local content (`os.ReadFile`).
  4. Counterpart: local branch → `catContainer` against the first
     conf/container addons path holding the module (reuse `moduleBase`'s
     conf probe); remote branch → Unit 79's
     `resolveRemoteView`/`remoteReadModuleFile`. Missing file → empty
     content + `Warning` field on the result.
  5. `unifiedDiff(containerContent, localContent, fromLabel, toLabel)`
     small wrapper over `difflib` (pure, unit-testable).

### `internal/repl/compare.go` — new file

- `runCompare` mirroring `runView`: cancellation/non-TTY close through
  `sess.finalize("compare", …)`; `--copy` → clipboard write of the raw
  diff (INFO `copied to clipboard`); identical → INFO line, no pager;
  different → `ShowWithBat` / internal fallback, then the INFO frame with
  `result`/`via` fields.

### Registration — `internal/repl/commands.go` / `repl.go`

- Add `compare` to `Registry`, `dispatchNames`, and the dispatch `case`.
- `commandFlags["compare"] = {"--copy", "--from", "--remote"}`.
- Help, Modules section (right after the `view` block):
  `{"compare [<mod>]", "Diff a local module file against its Docker copy"}`,
  `{"  --from <t>", "Compare against a remote target (or --remote for the link binding)"}`,
  `{"  --copy", "Copy the diff to the clipboard"}`.
- One-shot eligible with a remote flag (Unit 72 pattern); local mode needs
  the project's `[docker]` config as usual.

### Tests

`internal/cmd/compare_test.go`:

- `unifiedDiff` golden cases: change, identical (empty string), and
  empty-container side (full file as `+`), asserting the
  `<target>/…` / `local/…` labels and hunk headers.
- Flag parse: `compare sale --from prod --copy` → module `sale`,
  copy=true, from=`prod`; `--remote` variant.
- Missing-in-container path yields `Identical=false` + warning, not an
  error.
- Registry/help/dispatch consistency tests (`registry_test.go`) updated
  to include `compare`.

## Dependencies

- `github.com/pmezard/go-difflib` — unified diff in-process; already a
  transitive dependency (testify), promoted to direct. No other
  additions.

## Verify when done

- [ ] `compare sale` (conf-mode project) diffs
      `<host>/sale/<file>` against the local container's copy and shows a
      bat-highlighted unified diff with `--- docker/sale/<file>` /
      `+++ local/sale/<file>` labels.
- [ ] Identical contents (e.g. mount-mode project) print a single
      `result=identical` INFO line, no pager, exit 0.
- [ ] `compare sale --from prod` diffs the local file against the copy on
      the `prod` target (hostFS or container per Unit 79's probe), label
      `--- prod/sale/<file>`.
- [ ] `compare --remote` resolves the linked target and walks the
      module → file pickers from the local checkout.
- [ ] A file present locally but absent in the container renders as an
      all-`+` diff after a WARNING line.
- [ ] `--copy` copies the raw unified diff and skips the pager.
- [ ] No prod confirm fires on any stage (read-only).
- [ ] A module with no host checkout copy errors clearly instead of
      comparing container-to-container.
- [ ] `help` lists `compare` with its three flag rows; Tab completion and
      `--build` pick it up via the registry; consistency tests green.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/cmd/...
      ./internal/repl/...` pass.
