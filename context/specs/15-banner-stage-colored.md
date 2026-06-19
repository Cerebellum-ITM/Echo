# Unit 15: banner-stage-colored

> Refina y reemplaza la entrada original del plan ("15 · banner-ascii — los 4
> logos ASCII con coloreado por segmento"). Esta unidad cubre **solo el
> wordmark `echo`** con dos estilos figlet seleccionables y coloreado por
> stage. Los logos `planet`/`python`/`anchor` quedan **fuera de alcance** aquí
> (futuro, atados al comando `logo` de Unit 14).

## Goal

Reemplazar el box `ECHO` hardcodeado de `buildLeft` (`internal/banner/header.go:59`)
por un banner del wordmark `echo` renderizado en uno de **dos estilos figlet**:

- **B · soundwave** — Calvin S (doble trazo `╔═╗`) + una onda `▁▂▃▅▇▅▃▂▁` debajo.
- **D · shadow** — ANSI Shadow (bloque `█` con sombra `╗`) con degradado vertical.

El **color principal sale del stage activo** (`PromptColor(stage)` del tema en
uso: dev→`success`, staging→`warning`, prod→`error`), y el degradado/onda se
**derivan en código aclarando/oscureciendo ese color base** — sin hardcodear
paletas por tema. Al abrir el REPL el estilo se elige **al azar** entre B y D,
con opción de fijarlo por config y un **guard de ancho** que cae a B cuando la
terminal es muy angosta para D.

## Design

Referencias de token en `context/ui-context.md`.

- **Color base por stage** = `palette.PromptColor(theme.StageFromString(opts.Stage))`.
  Mapeo ya existente: dev→`success`, staging→`warning`, prod→`error`. Esto hace
  que cada environment luzca distinto y consistente con el prompt y los pickers.
  Vale en los 4 temas porque cada uno define esos tokens (invariante #1).
- **No se usa `accent`/`accent2`** en el banner; manda el stage. (El resto del
  header —welcome, tips, bordes— no cambia.)
- **Estilo B (soundwave)**: 3 líneas del wordmark en el color base, **bold**;
  4ª línea con la onda en una versión **aclarada** del base (`Lighten(base,0.35)`).
  Footprint: 4 líneas × ~12 cols. Cabe siempre.
- **Estilo D (shadow)**: 6 líneas, cada una en un escalón de un **ramp de 6
  pasos** derivado del base (de claro arriba a oscuro abajo), **bold**. A la
  derecha, un ripple `·`/`)))`/`·` (3 líneas) en `Lighten(base,0.35)`. Footprint:
  6 líneas × 32 cols (+ ripple).
- **Ramp de 6 pasos** (factores aplicados al color base, arriba→abajo):
  `Lighten 0.45`, `Lighten 0.22`, `base`, `Darken 0.12`, `Darken 0.24`, `Darken 0.36`.
- **Indentación**: B mantiene la sangría de 3 espacios del header actual. D usa
  sangría 0–1 para no desbordar la columna a 80 cols.

### Arte (constantes en `internal/banner/echo.go`)

```
Calvin S "echo" (B), 3 líneas:
╔═╗╔═╗╦ ╦╔═╗
║╣ ║  ╠═╣║ ║
╚═╝╚═╝╩ ╩╚═╝
onda: ▁▂▃▅▇▅▃▂▁▂▃▁

ANSI Shadow "ECHO" (D), 6 líneas:
███████╗ ██████╗██╗  ██╗ ██████╗
██╔════╝██╔════╝██║  ██║██╔═══██╗
█████╗  ██║     ███████║██║   ██║
██╔══╝  ██║     ██╔══██║██║   ██║
███████╗╚██████╗██║  ██║╚██████╔╝
╚══════╝ ╚═════╝╚═╝  ╚═╝ ╚═════╝
ripple (3 líneas): "·" / ")))" / " ·"
```

## Implementation

### 1. Helper de derivación de color — `internal/theme/theme.go`

Añadir funciones puras que mezclan un `lipgloss.Color` hex hacia blanco/negro:

- `func Lighten(c lipgloss.Color, t float64) lipgloss.Color` — mezcla con
  `#ffffff` por `t∈[0,1]`.
- `func Darken(c lipgloss.Color, t float64) lipgloss.Color` — mezcla con
  `#000000` por `t`.

Parsean el hex `#rrggbb`, interpolan por canal, reemiten `lipgloss.Color`. No
es "hex hardcodeado" — deriva del color del tema activo, así que cumple el
invariante #1. Cubrir con test (round-trip de canales, t=0 → identidad,
t=1 → blanco/negro).

### 2. Selección de estilo + guard de ancho — `internal/banner/echo.go` (nuevo)

```go
type bannerStyle int
const (
    styleSoundwave bannerStyle = iota // B
    styleShadow                       // D
)

// resolveBannerStyle decide el estilo a partir del modo configurado, el ancho
// disponible de la columna izquierda y una fuente de aleatoriedad inyectable.
// mode: "auto" (default) | "soundwave" | "shadow".
// shadowFits = leftW >= shadowWidth (32). Si el modo pide shadow pero no cabe,
// cae a soundwave (regla "solo banners que quepan").
func resolveBannerStyle(mode string, shadowFits bool, coin func() bool) bannerStyle
```

- `mode=="soundwave"` → B.
- `mode=="shadow"` → D si `shadowFits`, si no B.
- `mode=="auto"` (o vacío/desconocido) → `coin()` elige B/D; si sale D y
  `!shadowFits`, B.
- En `header.go`, `coin` se construye con `math/rand` (auto-seed en Go ≥1.20);
  `resolveBannerStyle` recibe `coin` para ser testeable de forma determinista.

Función `renderEchoBanner(p theme.Palette, stage theme.Stage, style bannerStyle) []string`
que devuelve las líneas ya estilizadas (cada línea = `lipgloss` Foreground del
escalón correspondiente + `Bold(true)`), usando `Lighten/Darken` sobre
`p.PromptColor(stage)`.

### 3. Integración en el header — `internal/banner/header.go`

- `Render` ya recibe `p theme.Palette`; pasar `p` y el modo a `buildLeft`.
- `buildLeft`: sustituir el slice `logo` hardcodeado (líneas 60–65) por
  `renderEchoBanner(p, theme.StageFromString(opts.Stage), style)`, con
  `style := resolveBannerStyle(opts.Banner, leftW >= shadowWidth, coin)`.
  `leftW` se calcula en `Render`; pasarlo a `buildLeft` (o mover el cálculo del
  estilo a `Render` y pasar las líneas listas).
- Para D, unir el ripple a la derecha por línea (índices 0–2 del bloque) o como
  segunda "mini-columna" análoga al patrón actual de segmentos.
- Añadir `Banner string` a `banner.Opts`.

### 4. Config — `internal/config/config.go`, `defaults.go`

- `globalFile`: nuevo campo `Banner string `toml:"banner"`` (junto a `Logo`).
- `Config`: nuevo campo `Banner string`; cargar/guardar en los puntos donde hoy
  se maneja `Logo` (`config.go:181`, `:288`, `:410`).
- Default `auto` en `defaults.go`.

### 5. Wiring REPL — `internal/repl/repl.go`

- En la construcción de `banner.Opts` (`repl.go:122`), setear
  `Banner: cfg.Banner`. `RunOnce`/`RunRecipe` no muestran header → sin cambios.

### Fuera de alcance (esta unidad)

- Logos `planet`/`python`/`anchor` y el comando `logo` (van con Unit 14).
- Banner ancho full-width arriba del header (se descartó: header de dos columnas
  se mantiene).

## Dependencies

- `math/rand` (stdlib) — selección aleatoria del estilo. Ningún paquete nuevo.

## Verify when done

- [ ] Con `stage=dev` el banner sale en verde (`success`), `staging` en
      `warning`, `prod` en rojo (`error`), en los 4 temas.
- [ ] Repetir el arranque varias veces produce **ambos** estilos (B y D) cuando
      `banner=auto` y la terminal es suficientemente ancha.
- [ ] En terminal angosta (< 32 cols de columna izquierda) **nunca** sale D: el
      guard cae a B sin desbordar el header.
- [ ] `banner=soundwave` y `banner=shadow` en `global.toml` fijan el estilo
      (shadow sigue respetando el guard de ancho).
- [ ] El degradado de D y la onda de B se ven como escalones del color del
      stage (claro→oscuro), no como colores fijos.
- [ ] Ningún hex literal en el código de render del banner: todo deriva de
      `palette.PromptColor(stage)` vía `Lighten/Darken` (invariante #1).
- [ ] Tests nuevos: `resolveBannerStyle` (modos + guard, con `coin` inyectado) y
      `Lighten/Darken` (límites y round-trip).
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` verdes; cross-check de
      Registry/help intacto.
