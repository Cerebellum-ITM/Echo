# Unit 52: `echo run` — picker por fecha de creación y `--last`

## Goal

Hacer que el picker de `echo run --pick` liste los recipes `*.echo`
ordenados por fecha de creación (más reciente primero), y agregar
`echo run --last` para ejecutar directamente el recipe más reciente sin
abrir el picker.

## Design

Hoy `echoRecipesIn` ([recipe.go](../../internal/repl/recipe.go)) ordena
alfabéticamente. El flujo típico es "acabo de escribir/copiar un recipe y
quiero correrlo", así que el orden útil es temporal, no alfabético:

- **Orden:** fecha de creación descendente (el más nuevo arriba). En
  macOS la fecha de creación real (birthtime) está disponible vía
  `syscall.Stat_t.Birthtimespec`; en otras plataformas Go no la expone,
  así que se cae a la fecha de modificación (`ModTime`). Empates se
  rompen alfabéticamente para que el orden sea determinista.
- **Picker (`--pick`):** sin cambios de UX — mismo single-select fuzzy;
  solo cambia el orden inicial de la lista. El fuzzy filtering sigue
  funcionando igual sobre los nombres.
- **`--last`:** resuelve el `*.echo` más reciente del directorio de
  trabajo (mismo scan top-level, sin recursión, que `--pick`) y lo corre
  directo. No necesita TTY — es apto para scripts/CI, a diferencia de
  `--pick`.
  - Sin recipes en el directorio → el mismo error
    `no .echo recipes found in <dir>` (exit 2).
  - Mutuamente excluyente con `--pick`, con un path posicional y con
    stdin (`-`): cualquier combinación es usage error (exit 2).
  - Compone con `--continue-on-error` y `--log`, igual que `--pick`.
  - Al iniciar el run se emite una línea en estilo Odoo
    (`INFO echo.run: latest recipe → <nombre>`) para que el transcript
    deje claro qué archivo se resolvió.

El nombre `--last` es consistente con el comando `last` ya existente
(recall del último update): "lo más reciente" como concepto de la CLI.

## Implementation

### Tiempo de creación por plataforma (`internal/repl/`)

Dos archivos nuevos con build tags, un solo símbolo:

`filetime_darwin.go`:

```go
//go:build darwin

package repl

import (
    "io/fs"
    "syscall"
    "time"
)

// fileCreated returns the file's birth time on Darwin, falling back to
// ModTime if the syscall info is unavailable.
func fileCreated(info fs.FileInfo) time.Time {
    if st, ok := info.Sys().(*syscall.Stat_t); ok {
        return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
    }
    return info.ModTime()
}
```

`filetime_other.go`:

```go
//go:build !darwin

package repl

import (
    "io/fs"
    "time"
)

// fileCreated approximates creation time with ModTime on platforms where
// Go does not expose the birth time.
func fileCreated(info fs.FileInfo) time.Time {
    return info.ModTime()
}
```

### Scan ordenado por creación (`internal/repl/recipe.go`)

`echoRecipesIn` pasa de `sort.Strings` a recolectar pares
nombre/creación y ordenar descendente. Para mantenerlo testeable sin
depender de birthtimes reales, el orden vive en una función pura:

```go
type recipeEntry struct {
    name    string
    created time.Time
}

// sortRecipesByCreation orders entries newest-first, breaking ties
// alphabetically so the result is deterministic.
func sortRecipesByCreation(entries []recipeEntry) {
    sort.SliceStable(entries, func(i, j int) bool {
        if !entries[i].created.Equal(entries[j].created) {
            return entries[i].created.After(entries[j].created)
        }
        return entries[i].name < entries[j].name
    })
}
```

`echoRecipesIn` construye `[]recipeEntry` (vía `e.Info()` por entrada;
una entrada cuyo `Info()` falle usa zero time y queda al final), llama
`sortRecipesByCreation` y devuelve los nombres en ese orden. El comentario
del doc se actualiza: "sorted by creation time, newest first".

### Flag `--last` (`internal/repl/recipe.go`)

`parseRecipeArgs` gana un retorno `last bool` y su case, con las
exclusiones después del loop:

```go
case a == "--last":
    last = true
...
if pick && path != "" {
    return ..., fmt.Errorf("--pick takes no recipe path")
}
if last && path != "" {
    return ..., fmt.Errorf("--last takes no recipe path")
}
if last && pick {
    return ..., fmt.Errorf("--last and --pick are mutually exclusive")
}
```

(Los retornos existentes se amplían a la nueva tupla; actualizar los
callers y tests al nuevo aridad.)

### Resolución en `RunRecipe`

Después del branch de `--pick`, antes de `readRecipe`:

```go
if last {
    names, lerr := echoRecipesIn(cwd)
    if lerr == nil && len(names) == 0 {
        lerr = fmt.Errorf("no .echo recipes found in %s", cwd)
    }
    if lerr != nil {
        fmt.Fprintln(os.Stderr, "echo run: "+lerr.Error())
        return exitUsage
    }
    path = filepath.Join(cwd, names[0])
}
```

Tras crear la sesión, cuando `last` está activo, registrar la
resolución: `sess.runLog("INFO", "latest recipe → "+recipeLabel(path))`.

### Help (`internal/repl/repl.go`)

En el footer de Scripting, junto a `--pick`:

```go
{"  --last", "Run the most recently created .echo recipe"},
```

### Tests

- `internal/repl/recipe_test.go`:
  - `TestParseRecipeArgs`: filas nuevas — `--last` solo (`last=true`,
    sin path), `--last` con `--log`/`--continue-on-error`,
    `--last recipe.echo` → error, `--last --pick` → error. Filas
    existentes migradas a la nueva tupla.
  - `TestSortRecipesByCreation`: entradas con tiempos distintos quedan
    newest-first; empates de tiempo quedan alfabéticos; zero time al
    final. (Puro, sin filesystem.)
  - Extender el test de `echoRecipesIn`: con dos `.echo` creados con
    `os.Chtimes` a mtimes distintos (en plataformas sin birthtime el
    fallback es ModTime; en darwin el test fija los mtimes y solo
    asegura que ambos aparecen — el orden exacto se cubre con el test
    puro de `sortRecipesByCreation`, evitando flakiness por birthtime).

### Docs

- `CHANGELOG.md` → `[Unreleased]`:
  - `Added`: `echo run --last` ejecuta el recipe `.echo` más reciente
    sin abrir el picker.
  - `Changed`: el picker de `echo run --pick` ordena los recipes por
    fecha de creación (más reciente primero) en lugar de alfabético.
- `context/progress-tracker.md` → marcar Unit 52.
- `context/specs/00-build-plan.md` → agregar la fila de Unit 52.

## Dependencies

Ninguna nueva. `syscall` (stdlib, solo en el archivo darwin) y los
helpers existentes del runner.

## Verify when done

- [ ] `echo run --pick` lista los `.echo` con el más recientemente
      creado primero (verificable creando dos recipes en orden y
      abriendo el picker).
- [ ] `echo run --last` corre el `.echo` más reciente directamente, sin
      picker ni TTY, y el transcript muestra
      `echo.run: latest recipe → <nombre>`.
- [ ] `--last` compone con `--continue-on-error` y `--log`.
- [ ] `--last` en un directorio sin `.echo` imprime
      `no .echo recipes found in <dir>` y sale 2.
- [ ] `--last recipe.echo` y `--last --pick` son usage errors (exit 2).
- [ ] El orden es determinista ante empates de fecha (alfabético).
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pasan.
