# Unit 77: multi-module `i18n-pull` in the sequence / build-mode builder

## Goal

Make the `i18n-pull` build-mode flow (`runI18nPullBuild`, used by
`i18n-pull --build` and by every `i18n-pull` step in `sequence`) pick
**several modules at once** instead of one, matching the direct-command
capability added in Unit 76. It composes
`i18n-pull <mod1> <mod2> … --lang=<lang> [--from=<name>]` so a remote
`deploy → i18n-pull` sequence pulls every deployed module's `.po` in a
single step, chosen up front in the builder's review.

## Design

**Only the module picker changes.** Everything else in `runI18nPullBuild`
stays: remote-target resolution (baking `--from=<name>`), the remote module
listing over SSH, the language prompt, and the return-only `BuildResult`. The
single-select `runSingleFuzzyPicker` becomes the shared **multi-select**
`runFuzzyPickerCore` (the picker `install`/`update`/`test` and the Unit 76
empty-args `i18n-pull` use), stage-colored by the **remote** profile's stage
so the build picker reads as "remote". Esc or an empty selection returns
`ErrCancelled`, exactly as the single-select did.

**Language is baked as `--lang=<lang>`, not a trailing positional.** With
multiple module positionals, a trailing locale-shaped token would be
ambiguous to re-parse (is the last one a module or the language?). Baking the
explicit `--lang=<lang>` flag (Unit 76) makes **every** positional
unambiguously a module and keeps the composed line self-describing and
`--last`-replayable. When the language prompt returns empty, no `--lang` is
baked (the command defaults to `es_MX`).

Resulting composed line, e.g. `i18n-pull sale account purchase --lang=es_MX
--from=prod`.

**Stage for the picker color.** `remoteI18nModules` already fetches the
remote profile (which carries `Stage`) while listing modules; it now returns
that stage alongside the module list so the picker can color its left bar by
it (falling back to the default when unknown), instead of the build flow
refetching the profile.

**`--all` / `--installed` still not offered here** (unchanged): once you're
hand-picking modules they're meaningless. The bulk flow stays on the direct
command.

**Sequence integration is already done.** `i18n-pull` is already in
`sequenceCommands` / `remoteSequenceCommands` and is `mustBuildInSequence`, so
it always routes through `runI18nPullBuild`. No change to `sequence.go` — this
unit only widens what that builder captures. A remote `deploy → i18n-pull`
sequence therefore works today; this makes its pull step cover many modules.

## Implementation

### `internal/cmd/build_i18npull.go`

- `remoteI18nModules(ctx, pullOpts, sshHost, remotePath)` → change the return
  to `([]string, string, error)` (modules, stage, err); return `prof.Stage`
  as the second value (empty on the error paths).
- `runI18nPullBuild`:
  - `modules, stage, err := remoteI18nModules(...)`.
  - Replace the single picker with:
    ```go
    picked, _, canceled, err := runFuzzyPickerCore(
        "Modules to pull translations for", modules, nil, nil, nil, opts.Palette, stage)
    if err != nil {
        return BuildResult{}, err
    }
    if canceled || len(picked) == 0 {
        return BuildResult{}, ErrCancelled
    }
    ```
  - Positionals become all picked modules; language moves to a flag:
    ```go
    positionals := picked
    var flags []chosenFlag
    if lang != "" {
        flags = append(flags, chosenFlag{name: "--lang", value: lang, sep: "="})
    }
    if fromName != "" {
        flags = append(flags, chosenFlag{name: "--from", value: fromName, sep: "="})
    }
    ```
  - Update the doc comment (composes `i18n-pull <mod...> --lang=<lang>
    [--from=<name>]`, multi-select).

### Tests

`internal/cmd/build_i18npull_test.go` (new or extend): the network/SSH parts
aren't unit-testable, but the arg composition is. Add a focused test that
`composeArgs([]string{"sale","account"}, []chosenFlag{{"--lang","es_MX","="},
{"--from","prod","="}})` yields `["sale","account","--lang=es_MX",
"--from=prod"]`, and that the resulting `BuildLine("i18n-pull", …)` re-parses
through `parseI18nPullArgs` to modules=`[sale account]`, lang=`es_MX`,
from=`prod` (round-trip guard tying Unit 76's parser to Unit 77's composer).

## Dependencies

None new.

## Verify when done

- [ ] `i18n-pull --build` opens a **multi-select** remote module picker;
      picking several composes `i18n-pull mod1 mod2 … --lang=<lang>
      [--from=<name>]`; Esc / empty selection cancels.
- [ ] In `sequence` (local or `--remote`), an `i18n-pull` step lets you pick
      several modules up front; the review shows the full composed line and
      it is replayable via `sequence --last`.
- [ ] The composed line re-parses (Unit 76) to the same modules + lang +
      target (round-trip).
- [ ] Single-module selection still works (composes one positional).
- [ ] `--from` baking unchanged (named target baked; project `[connect]`
      leaves `--from` off).
- [ ] `go build ./...`, `go vet ./...`, and
      `go test ./internal/cmd/... ./internal/repl/...` all pass.
