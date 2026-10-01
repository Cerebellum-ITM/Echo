# Unit 78: `deploy` headless — selección no-interactiva de módulos

## Goal

Hacer que `deploy` (Unit 61) corra **headless**, sin el multi-select
picker de commits, para poder disparar el despliegue completo desde un
script o CI en un solo comando. Hoy `deploy` resuelve los módulos abriendo
un picker sobre los commits recientes; bajo el guard de la Unit 31 (script
mode), sin TTY ese picker **falla cerrado** con `ErrNonInteractive`, así
que `deploy` no es invocable sin terminal. El chain manual
`stop → up → update <mods> → restart` ya es headless (Unit 31), pero pierde
la resolución commit→módulo e install-vs-update que hace `deploy`.

Esta unidad añade dos vías de selección **explícitas** que saltan el
picker, reusando el motor de resolución y ejecución remota de la Unit 61:

- `deploy --modules m1,m2` — lista explícita de módulos; se salta la
  resolución desde commits por completo.
- `deploy --auto` — auto-selecciona los commits **pendientes** (no
  enviados / dirty, reusando Unit 69 *deploy-dirty-modules*) y los mapea a
  módulos sin abrir el picker, en paralelo conceptual a `teleport beam -a`.

Más un resumen `--json` opcional del despliegue para que el llamador
parsee el resultado por módulo.

```
deploy --auto                      # despliega lo pendiente, sin picker
deploy --modules ventas,contabilidad
deploy --auto --json               # resultado parseable por módulo
deploy --auto --dry-run            # plan sin ejecutar (ya existe --dry-run)
```

## Design

**Reutilización, no reimplementación.** El núcleo de la Unit 61 —resolver
`install` vs `update` consultando los módulos instalados del remoto, y
ejecutar `stop → up -d → una corrida de Odoo con -i/-u` por el transporte
de la Unit 60— queda intacto. Esta unidad solo cambia **de dónde sale la
lista de módulos**: hoy del picker; ahora, además, de flags.

### `--modules m1,m2,…`

- Parsea una lista separada por comas en `[]string`, dedup, orden estable.
- Salta commit-log y picker por completo. Alimenta directo la fase de
  resolución install-vs-update de la Unit 61.
- Módulo inexistente en el repo local (no hay carpeta con `__manifest__.py`)
  → error claro de uso (exit `2`), antes de tocar el remoto.

### `--auto`

- Sin picker: toma el mismo conjunto que el picker ofrecería marcado por
  defecto — commits ahead-of-upstream + working tree dirty (reusa el
  detector de la Unit 69) — y lo mapea a módulos con el mapper existente
  de la Unit 61.
- Conjunto vacío (nada pendiente) → mensaje `nothing to deploy` y exit `0`
  (no es error).
- `--auto` y `--modules` son mutuamente excluyentes; pasarlos juntos →
  error de uso (exit `2`).

### Guard de interactividad (coherente con Unit 31)

- El picker interactivo sigue siendo el default cuando hay TTY y no se
  pasó `--auto`/`--modules` (cero regresión para el humano).
- Sin TTY (script/CI) y **sin** `--auto`/`--modules` → el picker cae en el
  `ErrNonInteractive` de la Unit 31 con hint específico:
  `"deploy needs a selection without a TTY: pass --auto or --modules"`.
- Las confirmaciones de stage prod (`confirmProd`) siguen su regla de la
  Unit 31: sin TTY exigen `--force`.

### `--json`

- Con `--json`, en vez del stream Odoo-style decorado, emitir **un** objeto
  JSON al final a `stdout` (logs a `stderr`), reutilizando los contadores
  que `finalize`/`runStats` ya llevan:

```json
{
  "target": "staging",
  "db": "mydb",
  "modules": [
    {"name": "ventas", "action": "update", "ok": true},
    {"name": "contabilidad", "action": "install", "ok": true}
  ],
  "errors": 0,
  "warnings": 1
}
```

- `--dry-run --json` emite el mismo shape con `"planned": true` y sin
  ejecutar nada remoto (el `--dry-run` de la Unit 61 ya resuelve el plan).
- Exit codes: los de la Unit 31 —`0` ok, `1` error / ERROR-lines, `2` uso
  / `ErrNonInteractive`, `3` cancelado (solo con TTY).

## Implementation

### `internal/cmd/deploy.go` — flags y selección

- Agregar flags al comando `deploy`:
  ```go
  deployCmd.Flags().StringSlice("modules", nil,
      "explicit module list; skip the commit picker")
  deployCmd.Flags().Bool("auto", false,
      "auto-select pending commits/dirty modules; skip the picker")
  deployCmd.Flags().Bool("json", false,
      "emit a machine-readable deploy summary")
  ```
- En `RunDeploy`, antes de la resolución de módulos, ramificar:
  1. `--modules` set → validar contra el repo local (`__manifest__.py`
     por módulo) y usar esa lista.
  2. `--auto` set → reusar el detector de pendientes/dirty (Unit 69) y el
     mapper commit→módulo (Unit 61); lista vacía → `nothing to deploy`,
     exit `0`.
  3. ninguno → comportamiento actual: si `interactive()` (helper de la
     Unit 31) abrir el picker; si no, devolver `ErrNonInteractive`
     envuelto con el hint de arriba.
  4. `--auto` + `--modules` juntos → error de uso.
- La fase de resolución install-vs-update y la ejecución remota **no se
  tocan**: reciben la `[]string` de módulos venga de donde venga.

### `internal/cmd/deploy.go` — salida `--json`

- Reusar el `runStats`/contadores existentes. Al finalizar, si `--json`,
  serializar el struct de resumen y escribirlo a `stdout`; el stream de
  logs va a `stderr` (mismo patrón de separación que otros `--json` del
  repo, p. ej. `modstate --json`).

### Reuso explícito

- Detector de pendientes/dirty: **Unit 69** (`deploy-dirty-modules`).
- Mapper commit→módulo y resolución install/update + ejecución remota:
  **Unit 61** (`deploy-command`).
- Guard TTY, `ErrNonInteractive`, exit codes: **Unit 31** (`script-mode`).
- `--force` para confirmaciones de prod: comportamiento existente.

### Tests (`internal/cmd/*_test.go`)

- `--modules a,b` produce exactamente esa lista y valida contra el repo;
  módulo inexistente → error de uso.
- `--auto` con pendientes simulados mapea al set esperado; sin pendientes
  → `nothing to deploy`, exit `0`.
- `--auto` + `--modules` → error de uso (exit `2`).
- Sin TTY (seam `interactive()` de la Unit 31 forzado a false) y sin
  selección → `ErrNonInteractive` con el hint de `deploy`.
- `--json` emite el shape con `modules[]`, `errors`, `warnings`; con
  `--dry-run` añade `planned:true` y no ejecuta remoto.

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: `deploy --auto` / `--modules`
  (selección headless, sin picker) y `deploy --json` (resumen parseable).
- `context/architecture.md`: anotar las dos vías de selección de `deploy`
  y que la resolución/ejecución de la Unit 61 se reusa sin cambios.
- `context/progress-tracker.md`: marcar Unit 78 al cerrarla.

## Dependencies

Ninguna nueva. Reusa el guard TTY (`golang.org/x/term`, ya dependencia
directa por la Unit 31), el detector de la Unit 69, el motor de la Unit 61
y el patrón `--json` ya presente en el repo.

## Verify when done

- [ ] `deploy --modules ventas,contabilidad` despliega exactamente esos
      módulos sin abrir el picker y termina `0`.
- [ ] `deploy --modules noexiste` sale `2` con error de uso antes de tocar
      el remoto.
- [ ] `deploy --auto` selecciona los commits pendientes + módulos dirty
      (Unit 69), los despliega sin picker, y con nada pendiente imprime
      `nothing to deploy` y exit `0`.
- [ ] `deploy --auto --modules x` (ambos) sale `2` como error de uso.
- [ ] Sin TTY y sin `--auto`/`--modules`, `deploy` sale `2` con
      `ErrNonInteractive` nombrando `--auto`/`--modules`, sin desplegar.
- [ ] En una terminal real y sin flags, `deploy` sigue abriendo el picker
      de commits como hoy (cero regresión).
- [ ] `deploy --auto --json` emite un objeto con `modules[]`
      (name/action/ok), `errors`, `warnings` en `stdout`.
- [ ] `deploy --auto --dry-run --json` emite `planned:true` y no ejecuta
      nada remoto.
- [ ] `deploy` contra un `stage=prod` sin TTY exige `--force` (regla
      Unit 31); con `--force` procede.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pasan.
