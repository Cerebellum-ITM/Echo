# Unit 73: sequence-builder — `sequence` interactive multi-command composer + runner

> **Estado: implementado (Unit 73).** Picker tri-estado en
> `internal/cmd/sequence_picker.go`, builder return-only (`BuildOpts.SkipDecide`),
> orquestación + `--last` en `internal/repl/sequence.go`, persistencia en
> `internal/config/last_sequence.go`. build/vet/test/gofmt verdes;
> cross-checks Registry/help/dispatch verdes. Verificación EN VIVO (TTY +
> contenedor/remoto) pendiente del usuario.
>
> Decisiones tomadas al implementar:
> - `--last` añadido (no `-r`): repite la última secuencia **ejecutada**
>   (solo la acción Run persiste `config.LastSequence`); no exige TTY.
> - Resolución remota simplificada: el modo remoto se entra **explícito** con
>   `--remote` / `--from <name>` y se **hornea** el flag en cada paso (no se
>   alcanza `pickPullTarget` desde `internal/repl`; el picker de target sin
>   flags explícito queda fuera de v1). Cada comando usa su propio
>   code-path remoto al ejecutar.
> - El runner de pasos se factorizó: `sess.runStepCaptured` (en `recipe.go`)
>   lo comparten `echo run` y `sequence`; la secuencia tiene su propio loop
>   (summary `echo.sequence: sequence complete` antes del follow) en vez de
>   reusar `runRecipeSteps`, para controlar el wording y el paso terminal.

## Goal

A new interactive command, `sequence`, that lets the user quickly assemble
**several commands in an exact order** from a single list and run them
back-to-back with Echo's Odoo-style streaming logs. The list uses a
**tri-state Tab cycle** per item: not-included → included (run as-is) →
included + **builder mode** (collect flags via the existing `--build`
engine). Run order = selection order. After an optional per-command builder
pass and a review screen, the assembled steps execute through the existing
recipe step runner (`runRecipeSteps`), fail-fast by default, closing with an
`echo.sequence` summary. Works **local and remote**: `sequence --remote` /
`sequence --from <target>` resolves one remote target and runs the whole
sequence against it.

```
> sequence
  [tri-state picker]   ⟦1⟧ update  → builder      ⟦2⟧ test  → builder
                       ⟦3⟧ i18n-pull (run as-is)  ⟦4⟧ logs (forced last)
  [builder 1/2: update] → --all --level=warn
  [builder 2/2: test]   → sale --tags=/sale
  [review] 4 steps · dev/18.0 · local → Run / Save .echo / Copy / Cancel
  → echo.sequence: running steps=4 mode=local
    … each step streams, per-step recap …
    echo.sequence: sequence complete ok=3 failed=0 took=1m57s
    echo.sequence.step: step 4/4 → logs (follow, ^c to stop)
```

### `sequence --last` — repetir la última secuencia

Pedido del usuario tras el diseño: poder **repetir la última secuencia
ejecutada** con un flag, consistente con `update --last` y `run --last`. El
flag es **`--last`** (no `-r`: chocaría conceptualmente con `--remote`).

- Cada **ejecución** (acción Run de la revisión) persiste la secuencia en
  `~/.config/echo/last-sequences/<projectKey>.toml` (`config.LastSequence`:
  `Steps` ya compuestos con el flag remoto horneado, `Remote`/`From` solo
  para la línea de label, `SavedAt`). Save/Copy/Cancel **no** persisten —
  "la última realizada" = la última corrida.
- `sequence --last` carga ese registro, salta el picker/builder/revisión y
  re-ejecuta `Steps` verbatim (el flag remoto ya está horneado, no se
  re-resuelve nada). Sin registro previo → WARNING + exit 2.
- `--last` **no** exige TTY (no abre pickers), así que `echo sequence --last`
  corre headless igual que `echo run --last`. `--continue-on-error` compone
  con `--last`.

### Decisiones del usuario (form + mockup — NO re-preguntar)

1. **Marcado**: un solo **Tab tri-estado** por ítem (off → run → build).
2. **Orden**: **orden de selección** (badge numérico `⟦n⟧`). Sin pantalla
   de reordenar.
3. **`logs`**: **forzado al último paso**; la secuencia emite su línea de
   cierre `echo.sequence: sequence complete` **antes** de entrar al follow,
   para que el `^c` no parezca un fallo. Pasos colocados después de `logs`
   se reubican (logs queda al final).
4. **`--remote`/`--from`**: aplica a **toda la secuencia, un solo target**
   (resuelto una vez). En modo remoto la lista solo ofrece comandos
   remote-capables.
5. **Indicador de builder**: glyph Nerd Font **`cod-tools` `` (U+EB6D)**
   en `accent` — misma familia Codicons que `cod-package` () de `modules`.
   **No emoji.**

## Design

### Relación con lo existente (reuso, poco código nuevo)

La secuencia es un **builder de recetas interactivo** que pega tres piezas
que ya existen:

- **Picker visual** — chrome de `fuzzyPicker` (barra `│` por stage, línea
  `filter ›`, `splitLabel`). Se crea un **modelo nuevo** tri-estado en vez
  de tocar el `fuzzyPicker` compartido (mismo criterio que separó
  `renderModuleList` de `renderMatchList`).
- **Builder por comando** — `cmd.RunBuild` (Unit 51) en un nuevo modo
  *return-only* (sin el `decideAction` final).
- **Ejecución** — `runRecipeSteps` (Unit 32/37) con un `runStep`/`log`
  modelados sobre `RunRecipe`, pero con logger `echo.sequence` /
  `echo.sequence.step` y un summary propio.

### Invocación y dispatch

`sequence` es un comando del `Registry` (interactivo, no un orquestador de
`main.go` como `run`). Se añade a `Registry`, `dispatchNames` y se enruta en
`dispatchParsed` (internal/repl/repl.go) a `sess.runSequence(ctx, args)`.

`commandFlags["sequence"] = {"--remote", "--from", "--last", "--continue-on-error"}`.

- `echo sequence` desde terminal (one-shot con TTY) → RunOnce →
  dispatchParsed → runSequence: arma y corre.
- Sin TTY (receta/CI) → `requireTTY("sequence is interactive; run it from a
  terminal")` falla closed → exit 2 (Invariante 9). No hay recursión:
  `sequence` no aparece en su propia lista de comandos secuenciables (v1).

### Comandos secuenciables (allowlist)

En internal/repl, ordenado por categoría (mismo orden del help):

```go
// sequenceCommands lists the commands offerable in a local sequence.
// Excludes interactive/PTY and meta commands that make no sense batched:
// shell, bash, psql, shell-run, connect, init, reset, clear, help,
// exit, quit, and sequence itself.
var sequenceCommands = []string{
    "up", "down", "stop", "restart", "ps", "logs",
    "install", "update", "uninstall", "test",
    "modules", "modinfo", "modstate", "view",
    "i18n-export", "i18n-update", "i18n-pull",
    "db-backup", "db-restore", "db-drop", "db-neutralize", "db-list", "db-use", "db-admin",
    "deploy", "report", "copy-last", "alias", "link",
}

// remoteSequenceCommands: subset valid when the sequence targets a remote.
// Only commands that accept --from/--remote, minus interactive shells.
var remoteSequenceCommands = []string{"restart", "logs", "i18n-pull", "deploy"}
```

> **Instrucción al implementador:** validar `remoteSequenceCommands` contra
> `commandFlags` — cada uno debe contener `"--from"` (hoy: `i18n-pull`,
> `restart`, `logs`, `deploy` lo cumplen; `shell`/`shell-run` se excluyen a
> propósito por ser PTY interactivos). Añadir un guard de test.

### Modo remoto (resolución única)

`runSequence` parsea `--remote` / `--from <name>` (reusar el mismo patrón de
parseo que `restart`/`logs`; ver `remoteRunFlags`). Resolución, **una sola
vez**, espejando el builder de i18n-pull (`runI18nPullBuild` en
`internal/cmd/build_i18npull.go`):

- `--from <name>` → usar ese target; **hornear `--from=<name>`** en cada
  paso ensamblado.
- `--remote` (sin nombre) → usar el `[connect]` del proyecto; **hornear
  `--remote`** en cada paso.
- `sequence` remoto sin `--from`/`--remote` explícito: si hay un solo target
  nombrado, auto; si hay varios, picker (reusar `pickPullTarget`); hornear
  `--from=<elegido>`.

En modo remoto la lista del paso 1 = `remoteSequenceCommands`. En local =
`sequenceCommands`, sin hornear flag remota.

> El "hornear" significa: tras componer el argv de cada paso (sea run-as-is
> o builder), se le **append** del token remoto (`--from=<t>` o `--remote`)
> si no lo trae ya. Esto reusa los code-paths remotos que cada comando ya
> tiene; `sequence` no abre SSH por su cuenta.

### Paso 1 — picker tri-estado + orden

Nuevo modelo bubbletea en `internal/cmd/sequence_picker.go` (reusa
`splitLabel`, el cómputo de `accent`/stage y el chrome del `fuzzyPicker`):

```go
type seqItem struct {
    name  string
    desc  string // hint de la 2ª columna (dim), p.ej. "every installed module"
    state int    // 0 off · 1 run · 2 build
}

type sequencePicker struct {
    filter  textinput.Model
    items   []seqItem
    visible []int
    order   []int   // índices de items en orden de selección (state>0)
    cursor, offset, height int
    palette theme.Palette
    accent  lipgloss.Color // por stage (verde dev / amarillo staging / rojo prod)
    canceled, quit bool
}

// RunSequencePicker corre el picker y devuelve las selecciones en orden.
type SeqPick struct{ Command string; Build bool }
func RunSequencePicker(title string, items []seqItem, palette theme.Palette, stage string) ([]SeqPick, error)
```

Interacción:

- **Tab** sobre el ítem bajo el cursor: cicla `state` 0→1→2→0.
  - 0→1: se agrega a `order` (al final) → badge `⟦n⟧`.
  - 1→2: conserva posición en `order`; muestra glyph builder.
  - 2→0: se quita de `order` y **se renumera** el resto (los posteriores
    bajan uno).
- **↑↓ / Ctrl+P/N**: mover cursor. **PgUp/PgDn**: paginar. Filtro siempre
  activo (substring, case-insensitive) como en `fuzzyPicker`.
- **Enter**: confirmar → devuelve `[]SeqPick` en el orden de `order`
  (solo state>0). Enter con cero seleccionados → `ErrCancelled` (nada que
  hacer).
- **Esc / Ctrl+C**: `canceled=true`. **Ctrl+X**: `quit=true` (cierra Echo,
  como los otros pickers vía `cmd.ErrQuit`).

Render de cada fila (tokens de `ui-context.md`):

| Estado | Badge | Glyph build | Color |
|---|---|---|---|
| off | `⟦ ⟧` | — | `faint` |
| run | `⟦n⟧` | — | número `success`, nombre `fg` |
| build | `⟦n⟧` | `` | número `success`, glyph `accent`, nombre `fg` |

- Cursor `❯` en `accent`; barra izquierda `│` y prompt `filter ›` en el
  color del stage (`accent` del modelo).
- 1ª columna = nombre (resaltado `fg`/bold bajo cursor), 2ª columna = `desc`
  en `dim` (vía `splitLabel`/`pad`).
- Footer de hints (`faint`): `Tab cycle · ↑↓ move · ⏎ continue · esc cancel · ^x quit`.

### Paso 2 — builder por comando (return-only)

Para cada `SeqPick{Build:true}`, en orden, correr el builder existente en
modo *return-only*. Extender `cmd.BuildOpts`:

```go
type BuildOpts struct {
    // … campos actuales …
    SkipDecide bool // true → no mostrar Run/Copy/Cancel; devolver Args directo
}
```

En `RunBuild`, si `SkipDecide` → tras componer `Args` (pasos 1–3), retornar
`BuildResult{Args: args, Action: BuildRun}` **sin** llamar `decideAction`.

`runSequence` (repl) llama `cmd.RunBuild` con `SkipDecide:true`, pasando
`commandFlags[cmd]` (alias-filtrado igual que el `runBuild` actual). El
header de progreso se emite como línea INFO `echo.sequence.build: building
step <i>/<n> command=<cmd>` antes de cada builder.

Casos borde:
- Comando marcado build pero **sin posicionales ni flags** (`errNothingToBuild`
  de Unit 51): degradar a *run-as-is* silenciosamente (no abortar) — el
  usuario lo marcó build pero no hay nada que construir. Emitir DEBUG
  `echo.sequence.build: nothing to build for "<cmd>", running as-is`.
- Builder cancelado (Esc) en un paso → cancela **toda** la secuencia
  (`ErrCancelled`, exit 3): el usuario aún no ejecutó nada.

El argv resultante de cada paso (build o run-as-is) se guarda como una
**línea de receta** `name + " " + strings.Join(args, " ")`; si hay target
remoto, se hornea el token remoto aquí.

### Paso 2.5 — `logs` siempre al final

Tras ensamblar las líneas, si alguna es un `logs` **follow** (es decir,
`logs` sin `--no-follow`), moverla al final del slice. Solo se permite un
paso `logs` follow; si hubiera más de uno, conservar el primero como
terminal y degradar los demás emitiendo WARNING `echo.sequence: multiple
follow logs steps; only the last follows` (raro, defensivo). Helper puro
testeable:

```go
// reorderLogsLast mueve el (único) paso `logs` follow al final, conservando
// el orden relativo del resto. Devuelve (steps reordenados, hasFollowLogs).
func reorderLogsLast(steps []string) (out []string, followLogs bool)
```

### Paso 3 — revisión

`huh.NewSelect[string]` (tema `cmd.BuildHuhTheme(palette)`), título = lista
numerada de los pasos ya compuestos (estilizada: comando en `accent`, flags
resaltadas). Opciones (sin "Reorder" — el orden ya quedó en el paso 1):

- `Run it now` → ejecutar.
- `Save as recipe (.echo)` → pedir nombre con `huh.NewInput` (placeholder
  `my-flow`), escribir las líneas (una por línea, sin prefijo `echo`) a
  `<cwd>/<name>.echo`; INFO `echo.sequence: recipe saved file=<name>.echo`.
  Reusa el formato que `echo run` ya consume.
- `Copy to clipboard` → `clipboard.WriteAll(strings.Join(steps, "\n"))`;
  INFO `echo.sequence: sequence copied steps=<n>`.
- `Cancel` → `finalize` exit 3.

Esc en el select → cancelar (exit 3).

### Paso 4 — ejecución

Reusar `runRecipeSteps(steps, continueOnError, runStep, log)`:

- `runStep` modelado sobre el de `RunRecipe`: parsea la línea, llama
  `sess.dispatchParsed(ctx, name, args)`, captura `sess.lastErrors` /
  `sess.lastWarnings` y devuelve `stepOutcome`. Reusar tal cual el del
  recipe runner si es factible (idealmente extraer el closure a un helper
  compartido `sess.recipeRunStep(ctx)`); si no, replicarlo.
- `log` emite con logger `echo.sequence.step` (no `echo.run.step`).
- `continueOnError` viene de `--continue-on-error` (default false →
  fail-fast, igual que recetas).

Líneas de log (todas vía `emitOdooLog(level, logger, msg, fields, styles,
palette, dbName)`):

```
echo.sequence       : running steps=<n> mode=<local|remote target=<t>>
echo.sequence.step  : step <i>/<n> → <command line>
echo.sequence.step  : step <i>/<n> ok|failed warnings=<w> errors=<e> took=<d>   (recap por paso)
echo.sequence       : sequence complete ok=<k> failed=<f> skipped=<s> took=<d>  (summary)
```

Colores por outcome (igual que el recap de recetas): ok=success,
failed=error, skipped/cancelled=warning.

#### Manejo del paso terminal `logs`

Si `reorderLogsLast` reportó `followLogs == true`:

1. Ejecutar `steps[:n-1]` con `runRecipeSteps`.
2. Si fail-fast cortó antes del final → emitir summary **failed** y **no**
   entrar a logs (return con exit≠0).
3. Si todo ok → emitir el summary `echo.sequence: sequence complete` **ya**,
   luego INFO `echo.sequence.step: step n/n → logs (follow, ^c to stop)` y
   despachar el último paso (`logs …`) directo con `dispatchParsed`, que
   bloquea en el follow hasta `^c` (su propio manejo de Ctrl+C/contexto).
   El exit de la secuencia es el del bloque previo (0), no el del `^c`.

Si no hay `logs` follow terminal: ejecutar todos los pasos y luego el
summary, como una receta normal.

### TTY / one-shot / recetas

- `requireTTY` al entrar (interactivo por definición).
- `echo sequence` desde terminal funciona (one-shot con TTY).
- Dentro de `echo run` (sin TTY) → exit 2 fail-closed; no se necesita código
  extra (el guard basta). Añadir un caso al smoke test.

### Help

Una línea en `helpSections` (categoría Scripting/Utility, junto a `run`):
`sequence            Pick several commands in order and run them (local/remote)`.
Mantener el cross-check Registry↔help verde.

## Implementation (resumen de archivos)

| File | Change |
|---|---|
| `internal/cmd/sequence_picker.go` | **new** — `sequencePicker` tri-estado, `RunSequencePicker`, `SeqItem`/`SeqPick` |
| `internal/cmd/sequence_picker_test.go` | **new** — cycle/order/renumber, picks vacíos |
| `internal/cmd/build.go` | `BuildOpts.SkipDecide` + early-return en `RunBuild` |
| `internal/config/last_sequence.go` | **new** — `LastSequence`, `Load/SaveLastSequence` (para `--last`) |
| `internal/repl/sequence.go` | **new** — `runSequence` (+ `--last`), `buildSequenceSteps`, `executeSequence`, `sequenceReview`, `saveSequenceRecipe`, allowlists, `reorderLogsLast`/`isFollowLogs`/`bakeRemote`, `helpDescByName` |
| `internal/repl/recipe.go` | extrae `sess.runStepCaptured` (compartido con `echo run`) |
| `internal/repl/sequence_test.go` | **new** — `isFollowLogs`, `reorderLogsLast`, `bakeRemote`, allowlist↔commandFlags guard |
| `internal/repl/commands.go` | `Registry` + `commandFlags["sequence"]` |
| `internal/repl/repl.go` | `dispatchNames` + case `"sequence"` + entradas de help |
| `CHANGELOG.md` | entrada `Added` en `[Unreleased]` |
| `context/progress-tracker.md` | entrada Unit 73 al completar |

## Dependencies

- none — todo es Charm (`huh`, `bubbles/textinput`, `lipgloss`) y stdlib, ya
  en el go.mod. El glyph `` es texto Nerd Font (sin dependencia de runtime).

## Tests

- `sequence_picker`: ciclo Tab 0→1→2→0; `order` se mantiene y **renumera**
  al quitar; Enter devuelve `[]SeqPick` en orden con `Build` correcto; Enter
  con cero → cancel; filtro restringe `visible`.
- `reorderLogsLast`: `logs` follow al final; `logs --no-follow` **no** se
  mueve; sin logs → sin cambios; dos logs follow → solo uno terminal +
  warning path.
- Guard allowlist: cada `remoteSequenceCommands` ∈ `commandFlags` con
  `"--from"`; cada `sequenceCommands` ∈ `Registry` (cross-check contra typos).
- `BuildOpts.SkipDecide`: `RunBuild` con SkipDecide no invoca el select y
  devuelve `Args` compuesto (reusar la maquinaria de `build_test.go`).
- TTY fail-closed: `runSequence` sin TTY → exit 2 (tabla de
  `interactive_test.go` si aplica).
- Smoke (binario real, manual del implementador): `sequence` en TTY arma 3
  pasos (uno con builder) y corre; `sequence --remote` filtra la lista y
  hornea `--from`; secuencia que termina en `logs` muestra el summary antes
  del follow; `echo sequence </dev/null` → exit 2; `sequence` dentro de una
  receta → exit 2.

## Verify when done

- [ ] `sequence` (REPL): picker tri-estado, Tab cicla off→run→build con
      badge `⟦n⟧` y glyph `` (accent) en build; orden = orden de selección.
- [ ] Cada comando marcado build pasa por el builder (return-only) y queda
      con sus flags; los run-as-is quedan sin flags.
- [ ] Revisión muestra los pasos numerados; Run ejecuta con logs estilo
      Odoo (`echo.sequence` / `echo.sequence.step`), fail-fast por default.
- [ ] Un paso `logs` se mueve al final y el summary `sequence complete`
      sale **antes** de entrar al follow; `^c` no marca la secuencia como
      fallida.
- [ ] `sequence --remote` / `--from <t>`: lista filtrada a remote-capables,
      `--from`/`--remote` horneado en cada paso, ejecución por SSH.
- [ ] `Save as recipe` deja un `.echo` que `echo run` corre; `Copy` deja las
      líneas en el clipboard.
- [ ] `sequence --last` repite la última secuencia ejecutada (salta
      picker/builder/revisión); sin registro previo → exit 2; `echo sequence
      --last` corre headless.
- [ ] Sin TTY → exit 2 (fail closed); dentro de `echo run` → exit 2.
- [ ] `go build/vet/test ./...` verdes; gofmt limpio; cross-checks
      Registry/help/commandFlags verdes.
- [ ] CHANGELOG `[Unreleased]` con la entrada; progress-tracker actualizado.

## Notas para el agente implementador

- Rama: seguir la rama de trabajo única vigente (no abrir una por feature
  salvo indicación). Commit atómico vía skill `commitcraft` (tag ADD, scope
  `sequence`), mensaje en **inglés**, **sin** trailer de co-autor de IA.
- No alterar el comportamiento de ningún comando existente: `sequence` solo
  compone argv (reusando `RunBuild`) y delega en `dispatchParsed` vía
  `runRecipeSteps`. Cero SSH propio — se hornea el flag remoto y cada
  comando usa su code-path remoto ya probado.
- Reusar el chrome visual del `fuzzyPicker` (no duplicar estilos): extraer a
  helpers compartidos si hace falta, pero **no** convertir el picker binario
  en tri-estado (riesgo de regresión en todos sus callers).
- Verificar la forma exacta del flag remoto que acepta cada
  `remoteSequenceCommands` (espacio vs `=`) leyendo su parser, igual que la
  tabla de Unit 51; `--from=<t>` es la forma esperada (precedente i18n-pull).
- Confirmar la firma real de `runRecipeSteps`/`stepOutcome`/`logField` y el
  closure `runStep` de `RunRecipe` antes de reutilizarlos; preferir extraer
  un helper compartido a copiar.
