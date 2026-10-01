# Unit 51: build-mode — `--build` / `-b` interactive command composer

> **Estado: implementado (Unit 51).** Engine en `internal/cmd/build.go`,
> wrapper en `internal/repl/build.go`, interception + help en `repl.go`,
> highlight/Tab en `commandhl.go`. Tests verdes, gofmt/vet limpios.
>
> Decisiones menores tomadas al implementar:
> - El sentinel se exporta como `cmd.ErrNothingToBuild` (no minúscula) para
>   que el repl pueda mapearlo con `errors.Is` desde otro paquete.
> - El guard de TTY corre **antes** del check "nothing to build", así que
>   `bash --build` sin TTY sale 2 por `ErrNonInteractive`, no por
>   `ErrNothingToBuild` (en TTY sí da "nothing to build").
> - Un input de valor vacío (tras trim) se trata igual que cancelar: la
>   flag se descarta con la WARNING `flag --x skipped (no value)`.
> - Formas verificadas contra los parsers: `--level`/`--out`/`--from`/
>   report `--step`/`--level`/`--min-level` → `=`; `test --tags` y
>   `db-restore --as` aceptan ambas, se usa `=`; `logs -t` solo espacio →
>   dos tokens.
>
> **Follow-up (post-Unit 51, pedido por el usuario):** `i18n-pull --build`
> ahora tiene un builder remote-aware dedicado (`internal/cmd/build_i18npull.go`,
> `runI18nPullBuild`) en vez de quedar solo-flags. Como sus candidatos
> (módulos) viven en el remoto, resuelve primero el connect target (reusa
> `pickPullTarget`: 1 → auto, varios → picker; si no hay nombrados cae al
> `[connect]` del proyecto), **hornea `--from=<target>`** en la línea
> compuesta, lista los módulos del remoto (`fetchRemoteProfile` +
> `listRemoteConfModules`) para el picker, y pide el lang — componiendo
> `i18n-pull <módulo> <lang> --from=<target>`. Los round-trips SSH se
> reportan como líneas INFO `echo.build` vía el nuevo callback
> `BuildOpts.Infof` (adaptador `i18nPullBuildOpts` que enruta el `Log` de
> `I18nPullOpts`). `--all`/`--installed` no se ofrecen (ignorarían el módulo
> elegido). `connect` se dejó como estaba (solo-flags): su positional es un
> login que requiere mint/probe remoto y connect ya es interactivo al
> ejecutar. Decisión del usuario vía formulario: solo i18n-pull + hornear
> `--from`.

## Goal

A universal `--build` flag (short form `-b`) accepted by every REPL command:
`<cmd> --build` walks the user through (1) the command's positional
picker(s) — modules, database, backup file, service — then (2) a
multi-select of the command's known flags, prompting for a value on each
flag that takes one (picker when the options are known, free input
otherwise), and finally (3) shows the composed command line and asks what
to do with it: **Run** (dispatch it now), **Copy** (clipboard, e.g. to
paste into a `.echo` recipe), or **Cancel**.

```
> update --build
  [multi picker: modules]           → sale, account
  [multi picker: flags]             → --level, --i18n
  [picker: value for --level]       → debug
  Composed: update sale account --level=debug --i18n
  [select] Run it now / Copy to clipboard / Cancel
```

Decisiones del usuario (form, NO re-preguntar):
- Acción final: **mostrar y preguntar** (Run / Copy / Cancel).
- Flags con valor: **pedir el valor** — picker si las opciones se conocen,
  input de texto si no.
- Nombre: **`--build` y `-b`** (ninguno choca con flags existentes; `-b` no
  aparece en `commandFlags`).

## Design

### Interception point

`--build`/`-b` is not a per-command flag — it is intercepted in
`dispatchParsed` (internal/repl/repl.go) **before** the command switch, the
same pattern as recipe `--silent` (which is stripped by the runner, not the
commands). New helper in `internal/repl/build.go`:

```go
// stripBuildFlag removes --build / -b from args, reporting presence.
func stripBuildFlag(args []string) (clean []string, build bool)
```

In `dispatchParsed`, right after the `lastOutput.Reset()` block:

```go
if clean, build := stripBuildFlag(args); build {
    sess.runBuild(ctx, cmd, clean)
    return
}
```

**v1 scope:** `--build` must be the only argument. If `clean` is non-empty
(`update sale --build`), emit a WARNING `echo.build` line
(`--build takes no other arguments`) and set `exitCode = exitUsage`. (Seeding
the builder with pre-typed args is a possible v2, out of scope.)

The interception must only fire for commands the switch actually routes
(`cmd` ∈ `dispatchNames`); for an unknown command let the normal
unknown-command path handle it (i.e. do the interception inside
`dispatchParsed` where the name is already about to be routed — checking
membership in `dispatchNames` before intercepting).

### TTY guard

Build mode is interactive by definition. `cmd.RunBuild` calls
`requireTTY("build mode is interactive; run it from a terminal")` first
thing — a non-TTY invocation (recipe, CI) fails closed with
`ErrNonInteractive` → exit 2, consistent with Invariant 9.

### Architecture / package split

The pickers (`runFuzzyPicker`, `runSingleFuzzyPicker`) and the data
providers (`resolveModules`, `docker.ListDatabases`, `listBackupFiles`) are
unexported in `internal/cmd`, while the flag registry (`commandFlags`)
lives in `internal/repl`. Split:

- **`internal/cmd/build.go`** — the engine. Exports:

```go
type BuildAction int
const (
    BuildRun BuildAction = iota
    BuildCopy
    BuildCancel
)

type BuildOpts struct {
    Cfg     *config.Config
    Root    string
    Command string
    Flags   []string      // the command's user-facing flags, supplied by the repl
    Palette theme.Palette
}

type BuildResult struct {
    Args   []string    // composed argv (positionals first, then flags)
    Action BuildAction
}

func RunBuild(ctx context.Context, opts BuildOpts) (BuildResult, error)
```

- **`internal/repl/build.go`** — `stripBuildFlag`, `runBuild` (session
  wrapper): supplies `commandFlags[cmd]` (alias-filtered, see below) to
  `cmd.RunBuild`, renders progress/result as `echo.build` Odoo-style lines
  via `emitOdooLog`, and acts on the result.

### Step 1 — positionals (`buildPositionals` registry, in cmd)

```go
type positionalSpec struct {
    title string                                              // picker title
    multi bool                                                // multi vs single select
    list  func(ctx context.Context, o BuildOpts) ([]string, error)
}
var buildPositionals = map[string]positionalSpec{ ... }
```

| Command(s) | multi | provider (reuse existing helpers) |
|---|---|---|
| `install`, `update`, `uninstall`, `test` | yes | `resolveModules(ctx, ModulesOpts{Cfg, Root, Palette})` — conf-aware |
| `modinfo`, `view` | no | same `resolveModules` |
| `i18n-export`, `i18n-update` | no | `resolveModules`; **plus** a second step: lang text input (huh `NewInput`, prefilled `es_MX` via `defaultI18nLang`) appended as the 2nd positional |
| `db-backup`, `db-drop`, `db-neutralize` | no | `docker.ListDatabases(ctx, cfg.ComposeCmd, root, cfg.DBContainer, env.Load(root)["POSTGRES_USER"])` |
| `db-restore` | no | `listBackupFiles(root)` (returns filenames in `./backups/`); empty → `ErrNoBackups` |
| `logs`, `restart` | no | `docker.ListContainers(ctx, cfg.ComposeCmd, root)` → the `Service` field of each container (mirror `containerOptions` in init.go) |

For the i18n lang input: implement as an optional `extra` field on
`positionalSpec` (`extra func(o BuildOpts) (string, error)`) appended after
the picked module; only i18n-export/i18n-update set it. Use
`cmd.BuildHuhTheme(palette)` for any huh form, `WithInput(os.Stdin)` /
`WithOutput(os.Stdout)` like init.go does.

Commands **not** in the map simply skip the positional step (e.g.
`modules`, `modstate`, `up`, `down`, `stop`, `ps`, `report`, `connect`,
`i18n-pull` — i18n-pull's candidates live on the remote and its own runtime
picker already handles that flow; the builder only assembles its flags).

**"Nothing to build" rule:** if a command has no positional spec AND
`len(opts.Flags) == 0` (e.g. `help`, `clear`, `bash`, `psql`, `shell`,
`init`, `reset`, `ps`, `up`...), `RunBuild` returns an error
`errNothingToBuild` (new, wrapped message `nothing to build for "<cmd>" —
it takes no picker or flags`); the repl maps it to a WARNING line + exit 2.

A cancelled picker (Esc) anywhere → return `ErrCancelled` (exit 3 via the
normal mapping).

### Step 2 — flag multi-select

Run `runFuzzyPickerCore` (multi-select, Tab to toggle, **Enter with zero
selected = no flags**, Esc = cancel) over the alias-filtered flag list.
Title: `Flags for <cmd> (Tab to toggle, Enter to confirm)`.

**Alias filter (repl side, before calling RunBuild):** drop `-c` from
`logs` (alias of `--copy`, both are in `commandFlags["logs"]`; offering
both invites a duplicate). Implement as a small map
`buildFlagAliases = map[string][]string{"logs": {"-c"}}` in
internal/repl/build.go with a comment, filtering `commandFlags[cmd]`.

Preserve `commandFlags` order in the picker (it's the help order).

### Step 3 — values for flags that take one (`buildFlagValues`, in cmd)

```go
type flagValueSpec struct {
    kind    string   // "pick" | "input"
    options func(o BuildOpts) []string // kind=pick
    prompt  string                     // input title / placeholder
    def     string                     // prefill for input
    sep     string                     // how to join: "=" or " " (see below)
}
var buildFlagValues = map[string]map[string]flagValueSpec{ /* cmd → flag → spec */ }
```

| cmd | flag | kind | source / notes | join form |
|---|---|---|---|---|
| install/update/uninstall | `--level` | pick | `odoo.LogLevels` | `--level=<v>` (extractLevel acepta `=`) |
| test | `--tags` | input | placeholder `:TestX.test_y,-external` | **verificar** qué forma parsea `tests` (espacio vs `=`) y usar esa |
| logs | `-t` | input | default `100` (tail lines) | **verificar** en RunLogs (`-t <n>` con espacio) |
| i18n-export | `--out` | input | placeholder `path/to/file.po` | `--out=<v>` (parseI18nArgs acepta `=`) |
| i18n-pull | `--from` | pick | `cfg.ConnectTargets` → `Name` de cada target | `--from=<v>` (parseI18nPullArgs acepta `=`) |
| report | `--step` | input | número de paso | `--step=<v>` (parseReportArgs usa `=`) |
| report | `--level` / `--min-level` | pick | `{debug, info, warn, error, critical}` | `=` (verificar en parseReportArgs) |
| db-restore | `--as` | input | nombre de la DB destino | **verificar** forma en parseDBArgs |

> **Instrucción al implementador:** para cada flag marcada "verificar",
> leer el parser real del comando y usar la forma (espacio vs `=`) que ese
> parser acepta — el argv compuesto se despacha tokenizado, así que una
> forma no soportada rompería el comando. Si el parser solo acepta espacio,
> el composer emite dos tokens (`"-t", "100"`).

Every flag not in the table is boolean — selected = appended as-is.

A flag whose value input/picker is cancelled → treat as "flag dropped"
(emit a WARNING `echo.build` line `flag --x skipped (no value)`), do not
abort the whole build.

**No exclusion logic:** the builder does NOT encode mutual exclusions
(`--all` + módulos, `--level` + `--min-level`, `--last` + módulos). Los
comandos ya validan en ejecución con errores claros; documentar esta
limitación en el spec/changelog. (Única consecuencia rara conocida:
`update <mods> --all` ejecuta `--all` ignorando los módulos — aceptable v1.)

### Step 4 — compose, show, decide

Compose `Args = positionals… + flags…` (flags in picker order). Render the
full line `cmd + " " + strings.Join(args, " ")` and show a huh
`NewSelect[string]` (theme `BuildHuhTheme`):

- Title: the composed line (styled by the repl when echoing, see below).
- Options: `Run it now`, `Copy to clipboard`, `Cancel`.

Return the matching `BuildAction` + `Args`. Esc on this form →
`huh.ErrUserAborted` (repl maps to cancel/exit 3).

### repl wrapper (`runBuild`)

```go
func (sess *session) runBuild(ctx context.Context, name string, rest []string)
```

1. `rest` non-empty → WARNING + `exitUsage` (v1 rule above).
2. Filter aliases; call `cmd.RunBuild`.
3. Error mapping: `errNothingToBuild` → WARNING `echo.build` + `exitUsage`;
   `ErrCancelled`/`huh.ErrUserAborted` → `sess.finalize("build", 0, 0, err)`
   (exit 3); `ErrNonInteractive` → finalize (exit 2); other → finalize
   (exit 1).
4. `BuildRun` → emit INFO `echo.build: running composed command
   cmd="update sale --level=debug"`, then
   `sess.dispatchParsed(ctx, name, res.Args)` — the executed command goes
   through its normal start/finalize frame and sets the session exit code.
5. `BuildCopy` → `clipboard.WriteAll(name + " " + strings.Join(res.Args, " "))`
   (la línea SIN prefijo `echo `, formato receta), INFO `echo.build: command
   copied cmd="…"`, `exitCode = exitOK`. Copy failure → ERROR + `exitError`.
6. `BuildCancel` → `sess.finalize("build", 0, 0, cmd.ErrCancelled)`.

All `echo.build` lines via `emitOdooLog(level, "echo.build", msg, fields,
sess.styles, sess.palette, sess.cfg.DBName)`.

### Flag highlight + Tab completion

`--build`/`-b` deben resaltarse como flags conocidas en TODOS los comandos
sin ensuciar `commandFlags` (que alimenta el help por comando). En
`internal/repl/commandhl.go`:

- Nuevo `var universalFlags = []string{"--build", "-b"}` con comentario.
- `classifyFlag`/`flagStyle`: una flag en `universalFlags` es conocida
  (accent) para cualquier comando del Registry.
- `flagsWithPrefix`: incluir `universalFlags` en los candidatos de Tab.
- Actualizar los tests de `commandhl_test.go` (la tabla de `classifyFlag` y
  el guard `commandFlags↔Registry` NO cambia — universalFlags es aparte).

### Help

Una línea en el footer de `runHelp` (FUERA de `helpSections`, como el
footer "Scripting", para no romper el cross-check Registry↔help):
`<cmd> --build   Interactively compose the command (pickers + flags), then run/copy it`.

### One-shot / recipes

- `echo update --build` funciona naturalmente (RunOnce → dispatchParsed →
  interception; hay TTY si se corre desde terminal).
- En recetas (`echo run`) no hay TTY → `requireTTY` falla closed, exit 2.
  No se necesita código extra; añadir un caso al smoke test.

## Files (resumen)

| File | Change |
|---|---|
| `internal/cmd/build.go` | new — engine: RunBuild, registries, compose |
| `internal/cmd/build_test.go` | new — tests (ver abajo) |
| `internal/repl/build.go` | new — stripBuildFlag, runBuild, buildFlagAliases |
| `internal/repl/build_test.go` | new — stripBuildFlag table + alias filter |
| `internal/repl/repl.go` | interception en dispatchParsed + help footer |
| `internal/repl/commandhl.go` | universalFlags (highlight + Tab) |
| `internal/repl/commandhl_test.go` | casos universalFlags |
| `CHANGELOG.md` | entrada `Added` en `[Unreleased]` |
| `context/progress-tracker.md` | entrada Unit 51 al completar |

## Tests

- `stripBuildFlag`: tabla — `--build` solo, `-b` solo, con otros args
  (build=true + clean conserva los demás), sin flag, `-b` en medio.
- `composeArgs` (extraer la composición como función pura
  `composeArgs(positionals, flags []chosenFlag) []string` para testearla):
  posicionales+booleans, flag con `=`, flag con espacio (dos tokens),
  orden estable.
- Registries cross-check (en `cmd/build_test.go`): toda key de
  `buildPositionals` y de `buildFlagValues` debe ser un comando conocido —
  exportar `func BuildCommands() []string` o validar contra una lista local
  duplicada mínima; y en `repl/build_test.go`: cada flag con value-spec
  (expuesta vía un export de prueba `cmd.BuildValueFlags(cmd) []string`)
  debe existir en `commandFlags[cmd]` — guard contra typos.
- `requireTTY` fail-closed: añadir `RunBuild` a la tabla de
  `interactive_test.go` si el patrón existente lo permite.
- Smoke (manual del implementador, binario real): `echo update --build`
  en TTY (picker visible), `echo update --build </dev/null` → exit 2,
  `update sale --build` → exit 2 usage, receta con `update --build` →
  paso falla con exit 2.

## Verify when done

- [ ] `update --build` en el REPL: picker de módulos (multi) → picker de
      flags → valor para `--level` (lista de LogLevels) → línea compuesta →
      Run la ejecuta con el frame normal / Copy la deja en el clipboard
      sin `echo ` / Cancel sale con warning.
- [ ] `db-restore --build` lista los backups de `./backups/`;
      `logs --build` lista los services del compose.
- [ ] `i18n-export --build` pide módulo y luego lang (prefill es_MX).
- [ ] Comando sin pickers ni flags (`bash --build`) → "nothing to build",
      exit 2.
- [ ] Sin TTY → exit 2 (fail closed); `update sale --build` → exit 2.
- [ ] `--build`/`-b` se pintan accent y completan con Tab en cualquier
      comando; cross-checks `registry`/`commandhl` verdes.
- [ ] `go build/vet/test ./...` verdes; gofmt limpio.
- [ ] CHANGELOG `[Unreleased]` con la entrada; progress-tracker actualizado.

## Notas para el agente implementador

- Rama: continuar en `feat/project-aliases` (el usuario la renombrará al
  final). Commit atómico vía skill `commitcraft` (tag ADD, scope `build`),
  SIN trailer de co-autor de IA — regla absoluta del usuario.
- Las esperas largas aquí son locales (pickers); no se necesita logging de
  progreso tipo connect, solo las líneas `echo.build` indicadas.
- No tocar el comportamiento de ningún comando existente: el builder solo
  compone argv y delega en `dispatchParsed`.
- Verificar las formas espacio-vs-`=` marcadas en la tabla ANTES de
  escribir el composer; anotar lo encontrado en el progress-tracker.
