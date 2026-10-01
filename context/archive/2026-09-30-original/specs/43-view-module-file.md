# Unit 43: `view` — pick and display a module file

## Goal

A new command `view [<mod>]` that opens a fuzzy picker of the files inside
a module and displays the chosen one. Display goes through `bat` (or
`batcat`) for syntax highlighting + paging when it is on `PATH`; otherwise
it falls back to printing the file internally through the REPL's themed
`Line` channel (which also captures it for `copy-last`). A `--copy` flag
copies the file's contents to the clipboard instead of displaying it. Files
are read from the host in host mode, or from inside the Odoo container in
conf mode — matching `resolveModules`' source of truth.

When no module is given, a single-select module picker chooses one first,
then the file picker opens.

## Design

`view` is a read-only inspection command, sibling of `modinfo` (Unit 42).
It lives in the `cmd` layer (reuses `resolveModules`, `readContainerFile`,
the pickers) and is wired into the REPL like the other commands.

The display fallback chain — settled with the user — is **`bat` → internal
print**, deliberately *not* `cat`: Echo already holds the file content in
memory (it must, to support `--copy` and to read container files), so the
only meaningful tiers are a real highlighter/pager (`bat`) and Echo's own
themed, captured print. `cat` would render worse than the internal print
*and* bypass Echo's output capture, so it is skipped.

`bat` knowledge stays in the `cmd` layer (`ShowWithBat`); the themed
fallback stays in the REPL (it uses `sess.print`). Output is consistent
with the rest of Echo: a trailing `echo.view` log line frames the action.

## Implementation

### `internal/cmd/view.go` (new)

```go
type ViewOpts struct {
    Cfg     *config.Config
    Root    string
    Args    []string
    Palette theme.Palette
}

type ViewResult struct {
    Module  string
    RelPath string // path within the module, e.g. "models/sale.py"
    Content string
    Copy    bool
}
```

`RunView(ctx, opts) (ViewResult, error)`:

1. Parse `--copy` out of `opts.Args`; first remaining positional = module
   name.
2. Resolve the module: if none given, single-select picker over
   `resolveModules` (reuse `cmd.PickOne` / `runSingleFuzzyPicker`). Non-TTY
   with no module → `ErrNonInteractive`.
3. List the module's files (`moduleFiles`, below) → relative paths, sorted.
   Empty → `fmt.Errorf("no files found for module %q", module)`.
4. Pick a file: single-select picker over the relative paths (reuse
   `runSingleFuzzyPicker`, title `"File in <mod>"`). Cancel → `ErrCancelled`.
5. Read the chosen file's content (`readModuleFile`, below).
6. Return `ViewResult{Module, RelPath, Content, Copy}`.

#### `moduleFiles` + `readModuleFile` (addons-mode aware)

A shared locator finds the module's base directory, honoring the mode:

```go
// moduleBase returns the addons path that holds <module>/__manifest__.py
// and whether it lives inside the container (conf mode) or on the host.
func moduleBase(ctx context.Context, opts ViewOpts, module string) (base string, inContainer bool, err error)
```

- conf mode (`cfg.AddonsMode == addonsModeConf`): iterate `cfg.AddonsPaths`
  (container paths); the first where `test -f <path>/<module>/__manifest__.py`
  succeeds (via `docker.Exec`) wins → `inContainer=true`.
- host mode: iterate `cfg.AddonsPaths` (or the `.`/`addons`/`custom`
  defaults); the first where `os.Stat(filepath.Join(root, path, module,
  "__manifest__.py"))` succeeds wins → `inContainer=false`.

`moduleFiles`:
- host: `filepath.WalkDir` under the module dir, collect files relative to
  it. Skip `__pycache__`, `.git`, and `*.pyc`.
- container: `docker.Exec` a `find <base>/<module> -type f` (filter the same
  noise), strip the `<base>/<module>/` prefix to get relative paths.

`readModuleFile(ctx, opts, base, inContainer, module, rel)`:
- host: `os.ReadFile(filepath.Join(root, base, module, rel))`.
- container: `readContainerFile(<base>/<module>/<rel>)` (the existing
  `docker exec cat` helper).

#### `ShowWithBat` (`internal/cmd/view.go`)

```go
// ShowWithBat displays content through bat/batcat when available, piping
// the content on stdin and letting bat handle highlighting + paging.
// Returns shown=false when neither binary is on PATH, so the caller can
// fall back to an internal print. name is passed as --file-name so bat
// picks the syntax from its extension.
func ShowWithBat(name, content string) (shown bool, err error) {
    bin := batBinary() // exec.LookPath "bat", then "batcat"
    if bin == "" {
        return false, nil
    }
    c := exec.Command(bin, "--style=plain,header", "--paging=auto",
        "--file-name="+name)
    c.Stdin = strings.NewReader(content)
    c.Stdout = os.Stdout
    c.Stderr = os.Stderr
    return true, c.Run()
}
```

### REPL wiring (`internal/repl/view.go`, new)

`runView(ctx, args)`:

1. Build `cmd.ViewOpts` from the session, call `cmd.RunView`.
2. On error: cancel / non-interactive → `finalize("view", …)` (WARNING /
   the non-interactive path); other errors → `finalize` ERROR. (Reuse the
   existing terminal helpers so exit codes stay consistent.)
3. On success:
   - `res.Copy` → `clipboard.WriteAll(res.Content)`, then
     `emitOdooLog("INFO", "echo.view", "copied to clipboard",
     []logField{{"module", res.Module}, {"file", res.RelPath}}, …)`.
   - else → `shown, err := cmd.ShowWithBat(res.RelPath, res.Content)`:
     - `shown` → emit a trailing `echo.view` INFO line
       (`{"module",…},{"file",…},{"via","bat"}`).
     - `!shown` → print the content internally line by line via
       `sess.print(Line{Kind: "out", Text: line})` (captured for
       `copy-last`), then the same INFO line with `{"via","internal"}`.
4. Set `sess.exitCode` (OK on success) so one-shot mode reports correctly.

### `--last` (session-only)

`view --last` replays the last file viewed **in this session**, skipping
both pickers — so a file first reached interactively can be copied (`view
--last --copy`). The target lives only in memory
(`session.lastViewModule` / `lastViewFile`), never persisted. A dedicated
`cmd.RunViewLast(ctx, opts, module, file, copy)` re-locates the base dir
and reads the file fresh (no pickers). The REPL strips `--last` (shared
`stripFlag`); an empty store warns `no previous view this session` (exit
2). Every successful `view` updates the stored module + file. `--last` is
added to `commandFlags["view"]` with a help sub-row.

### Registry / flags / help (`internal/repl/`)

- `commands.go` `Registry`: add `"view"` (next to `modinfo`).
  `commandFlags["view"] = []string{"--copy"}`.
- `repl.go` `dispatchParsed`: `case "view": sess.runView(ctx, args)`.
- `helpSections()` Modules section:
  `{"view [<mod>]", "Pick a module file and view it (bat, else plain)"}`
  and `{"  --copy", "Copy the file to the clipboard instead"}`.
- `IsScriptCommand` picks it up from `dispatchNames` automatically. (With a
  module + `--copy` it is fully headless; a bare `view` without a TTY fails
  closed via the picker guard.)

### Tests

- `internal/cmd/view_test.go`:
  - `batBinary` honors a stubbed `exec.LookPath` seam (found → name, absent
    → "").
  - parsing: `view sale --copy` → module `sale`, `Copy=true`; `view --copy`
    with no module leaves module empty (picker would run).
  - `moduleFiles` host-mode walk over a temp dir (skips `__pycache__` /
    `.pyc`, returns sorted relatives).
- `internal/repl/commandhl_test.go` `commandFlags`↔`Registry` guard and
  `registry_test.go` cross-check stay green (add `view`).

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `view [<mod>]` — pick a module
  file and display it (bat with internal fallback), `--copy`.
- `context/progress-tracker.md` → mark Unit 43 done with a session note.

## Dependencies

None new. Optional runtime tool `bat`/`batcat` (degrades gracefully to
internal print). Reuses `docker.Exec`, the pickers, `internal/clipboard`.

## Verify when done

- [ ] `view sale` opens the file picker for module `sale`; choosing a file
      displays it through `bat` with highlighting when `bat` is installed.
- [ ] With `bat` uninstalled (PATH stub), the file prints internally,
      themed, and is captured (`copy-last` after a `view` copies it).
- [ ] `view` with no module opens the module picker first, then the file
      picker; cancelling either warns (not ✗); non-TTY with no module fails
      closed (exit 2), no hang.
- [ ] conf mode lists + reads files from inside the container; host mode
      from disk — both resolve the same module.
- [ ] `view sale --copy` after picking a file copies its contents and logs
      `echo.view copied`.
- [ ] `view` highlights as a known command, `--copy` as a known flag.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
