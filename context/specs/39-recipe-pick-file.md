# Unit 39: `echo run --pick` recipe file selector

## Goal

Add `echo run --pick`: instead of typing a recipe path, open a fuzzy
picker listing the `*.echo` recipe files in the current directory and run
the chosen one. So you can discover and launch a recipe without
remembering its filename.

## Design

`echo run` resolves its recipe from a positional path (or stdin via `-`)
in `parseRecipeArgs` ([recipe.go](../../internal/repl/recipe.go)). This
unit adds a `--pick` flag that, instead, scans the working directory for
`*.echo` files and offers a single-select picker (the same
`runSingleFuzzyPicker` the module/db commands use), then feeds the chosen
file into the existing `readRecipe` → `runRecipeSteps` path unchanged.

- **Scan scope:** the directory `echo run` operates in (the `cwd` passed to
  `RunRecipe` — the project root, or `-C <dir>`), **top-level only**, no
  recursion. Only regular files whose name ends in `.echo` are listed,
  sorted.
- **Picker:** single-select, reused from the `cmd` package via a thin
  exported `cmd.PickOne` wrapper (the picker + its TTY guard live there).
  Esc cancels (exit 3); a non-TTY caller gets the usual `ErrNonInteractive`
  (exit 2) — `--pick` is interactive by nature.
- **No matches:** a clear `no .echo recipes found in <dir>` error (exit 2),
  not a silent empty picker.
- **Mutual exclusion:** `--pick` takes no positional path and isn't
  combined with stdin (`-`); passing both is a usage error. `--pick`
  composes with `--continue-on-error` and `--log` (pick the file, then run
  it with those options).

`.echo` is already the recipe convention (recipes are named `*.echo`; see
`recipeLabel`/the existing `update.echo` test fixtures), so this only adds
discovery, no new file format.

## Implementation

### Flag parsing (`internal/repl/recipe.go`)

`parseRecipeArgs` gains a `pick bool` return and a `--pick` case (before
the generic unknown-flag guard). After the loop, reject `--pick` combined
with a positional path or stdin `-`:

```go
func parseRecipeArgs(args []string) (path string, continueOnError bool, logDest string, logEnabled bool, pick bool, err error) {
    for _, a := range args {
        switch {
        case a == "--continue-on-error":
            continueOnError = true
        case a == "--pick":
            pick = true
        case a == "--log":
            logEnabled = true
        case strings.HasPrefix(a, "--log="):
            logEnabled = true
            logDest = strings.TrimPrefix(a, "--log=")
        case a == "-":
            path = a
        case strings.HasPrefix(a, "-"):
            return "", false, "", false, false, fmt.Errorf("unknown flag: %s", a)
        default:
            if path != "" && path != "-" {
                return "", false, "", false, false, fmt.Errorf("multiple recipe files given")
            }
            path = a
        }
    }
    if pick && path != "" {
        return "", false, "", false, false, fmt.Errorf("--pick takes no recipe path")
    }
    return path, continueOnError, logDest, logEnabled, pick, nil
}
```

### File scan + picker (`internal/repl/recipe.go`)

```go
// pickRecipeFile lists the *.echo recipes in dir (top-level, no recursion)
// and opens a single-select picker, returning the absolute path of the
// chosen recipe. ErrCancelled on Esc; a clear error when none are found.
func pickRecipeFile(dir string, p theme.Palette) (string, error) {
    entries, err := os.ReadDir(dir)
    if err != nil {
        return "", err
    }
    var names []string
    for _, e := range entries {
        if !e.IsDir() && strings.HasSuffix(e.Name(), ".echo") {
            names = append(names, e.Name())
        }
    }
    if len(names) == 0 {
        return "", fmt.Errorf("no .echo recipes found in %s", dir)
    }
    sort.Strings(names)
    name, err := cmd.PickOne("Recipe to run", names, p)
    if err != nil {
        return "", err
    }
    return filepath.Join(dir, name), nil
}
```

In `RunRecipe`, after parsing, resolve the path via the picker when
`--pick` is set, mapping cancel/non-interactive to the right exit code:

```go
path, continueOnError, logDest, logEnabled, pick, err := parseRecipeArgs(args)
if err != nil {
    fmt.Fprintln(os.Stderr, "echo run: "+err.Error())
    return exitUsage
}
if pick {
    selected, perr := pickRecipeFile(cwd, p)
    if perr != nil {
        fmt.Fprintln(os.Stderr, "echo run: "+perr.Error())
        if errors.Is(perr, cmd.ErrCancelled) {
            return exitCancelled
        }
        return exitUsage
    }
    path = selected
}
steps, err := readRecipe(path)
```

### Exported picker wrapper (`internal/cmd/picker.go`)

```go
// PickOne opens a single-select fuzzy picker over options and returns the
// chosen value. Esc / empty list → ErrCancelled; a non-TTY caller →
// ErrNonInteractive. Exported for callers outside cmd (the recipe runner's
// --pick selector).
func PickOne(title string, options []string, palette theme.Palette) (string, error) {
    return runSingleFuzzyPicker(title, options, palette)
}
```

### Help (`internal/repl/repl.go`)

Add to the Scripting footer, under `echo run <file>`:

```go
{"  --pick", "Pick a .echo recipe from the current directory"},
```

### Tests

- `internal/repl/recipe_test.go`: extend `TestParseRecipeArgs` for the new
  `pick` return — `--pick` alone (`pick=true`, no path), `--pick` with
  `--log`/`--continue-on-error`, and `--pick recipe.echo` → error
  (`--pick` + path). Update the existing rows to the new 6-tuple.
- A `pickRecipeFile` no-match test: a temp dir with no `.echo` files
  returns the "no .echo recipes found" error; a dir with two `.echo`
  files (plus a non-`.echo` file and a subdir) lists exactly the two
  sorted names. (The picker itself needs a TTY, so assert on the scan via
  a small refactor — factor the scan into a pure `echoRecipesIn(dir)
  ([]string, error)` that `pickRecipeFile` calls and the test targets.)

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `echo run --pick` opens a
  selector of `*.echo` recipes in the current directory.
- `context/progress-tracker.md` → mark Unit 39 done.
- `context/specs/00-build-plan.md` → add the Unit 39 row.

## Dependencies

None new. Reuses `runSingleFuzzyPicker` and `os.ReadDir`.

## Verify when done

- [ ] `echo run --pick` in a dir with `*.echo` files opens a single-select
      picker; choosing one runs it (same output/summary as a path run).
- [ ] `--pick` composes with `--continue-on-error` and `--log`.
- [ ] Esc cancels with exit 3; a non-TTY `echo run --pick` exits 2.
- [ ] A directory with no `.echo` files prints `no .echo recipes found in
      <dir>` and exits 2.
- [ ] `--pick recipe.echo` (path too) is a usage error; non-`.echo` files
      and subdirectories are not listed.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
