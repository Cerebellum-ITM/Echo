# odev — Design Tokens

CLI/TUI para administrar entornos de desarrollo Odoo.
Stack: **Go + Charmbracelet** (`bubbletea`, `lipgloss`, `bubbles`).

---

## 1. Paletas (4 temas)

Cada tema define los mismos slots semánticos para que los componentes
sean theme-agnostic. Los hex son truecolor (24-bit) — `lipgloss.Color("#…")`.

### `charm` — morado/rosa, vibe Charm default
| token   | hex       | uso                               |
|---------|-----------|-----------------------------------|
| bg      | `#13111c` | fondo de la terminal              |
| fg      | `#e8e3f5` | texto principal                   |
| dim     | `#8b80a8` | texto secundario / metadata       |
| faint   | `#5a5074` | bordes muy sutiles, separadores   |
| accent  | `#b794f6` | identidad / banner / highlights   |
| accent2 | `#f687b3` | secundario / acentos en banner    |
| success | `#68d391` | OK, dev stage, ✔                  |
| warning | `#f6ad55` | warnings, staging stage           |
| error   | `#fc8181` | errores, prod stage, destructivo  |
| info    | `#63b3ed` | comandos ejecutados, $ prompts    |

### `hacker` — verde clásico CRT
| bg `#0a0e0a` · fg `#d4f4d4` · dim `#7a9a7a` · faint `#4a5a4a`
| accent `#39ff14` · accent2 `#00d9ff`
| success `#39ff14` · warning `#ffd700` · error `#ff4444` · info `#00d9ff`

### `odoo` — morado oficial Odoo
| bg `#1a1322` · fg `#f0e9f5` · dim `#a094b3` · faint `#6b5e7d`
| accent `#a47bc4` · accent2 `#e8a87c`
| success `#7bcf9f` · warning `#e8a87c` · error `#e87878` · info `#7ba3c4`

### `tokyo` — Tokyo Night
| bg `#1a1b26` · fg `#c0caf5` · dim `#7982a9` · faint `#565f89`
| accent `#7aa2f7` · accent2 `#bb9af7`
| success `#9ece6a` · warning `#e0af68` · error `#f7768e` · info `#7dcfff`

---

## 2. Color del prompt según stage

Convención clásica de servers — el color sale del tema activo:

| stage     | usa token   | razón                       |
|-----------|-------------|-----------------------------|
| `dev`     | `success`   | seguro, todo verde          |
| `staging` | `warning`   | cuidado, ambiente compartido|
| `prod`    | `error`     | peligro, revisa dos veces   |

Aplica al **nombre del proyecto**, al **bracket `[stage/version]`** y se
mantiene en cada eco histórico del prompt (no solo el activo).

```
{project}-{id} [{stage}/{version}.0]:~$
^^^^^^^^^^^^^^^ ^^^^^^^^^^^^^^^^^^^   ^
stageColor      stageColor            tilde: info
```


---

## 4. Niveles de output (kinds)

Cada línea impresa es de un tipo. Mapean directo a un `lipgloss.Style`:

| kind     | color        | ejemplo                                    |
|----------|--------------|--------------------------------------------|
| `out`    | `fg`         | output neutro                              |
| `dim`    | `dim`        | metadata, paths, hints                     |
| `faint`  | `faint`      | separadores, decoración                    |
| `info`   | `info`       | `$ docker compose up -d` (comandos)        |
| `ok`     | `success`    | `✔ container started`                      |
| `warn`   | `warning`    | `WARNING cron took 11.4s`                  |
| `err`    | `error`      | `ERROR column does not exist`              |
| `accent` | `accent`     | identidad, headlines                       |
| `label`  | `warning`    | títulos de bloque ("Overview of commands") |
| `banner` | multi (grad) | logo ASCII con gradiente                   |

---

## 5. Comandos (alfabético, agrupados)

Auto-detectar version desde `docker-compose.yml` o `.odev.toml` del proyecto.
Soporta Odoo 17 / 18 / 19.

### Docker
- `up` — `docker compose up -d`
- `down` — `docker compose down`
- `restart [svc]` — restart odoo (default) o servicio dado
- `ps` — `docker compose ps`
- `logs [svc]` — `docker compose logs -f --tail 30`

### Módulos
- `install <mod>...` — `odoo -i <mods> --stop-after-init`
- `update <mod>...` — `odoo -u <mods> --stop-after-init`
- `uninstall <mod>...` — vía `module.button_immediate_uninstall()`
- `modules` — listar instalados

### i18n
- `i18n-export <mod> [<lang=es_MX>]` — `--i18n-export=i18n/<lang>.po`
- `i18n-update <mod> [<lang=es_MX>]` — `--i18n-overwrite -l <lang> -u <mod>`

### Base de datos
- `db-backup [<db>]` — pg_dump + filestore → `./backups/<db>-<date>.zip`
- `db-restore <file> [<db>]` — descomprime, neutraliza crons/emails
- `db-drop <db> --force` — confirma con `--force`
- `db-list` (alias `psql -l`) — listar bases

### Shells
- `shell` — `odoo shell -d <db>`
- `bash` — `docker compose exec odoo bash`
- `psql` — `psql -U odoo <db>`

### Tests
- `test <mod>...` — `odoo -i <mods> --test-enable --stop-after-init`

### Meta
- `version [17|18|19]` — switch active version
- `theme [charm|hacker|odoo|tokyo]`
- `logo [odev|planet|python|anchor]`
- `help` · `clear` (también `Ctrl+L`) · `exit`

### Atajos de teclado
- `↑` / `↓` — historial
- `Tab` — autocompletar comando
- `Ctrl+L` — clear screen

---

## 6. Tokens en Go (lipgloss)

```go
package theme

import "github.com/charmbracelet/lipgloss"

type Palette struct {
    Bg, Fg, Dim, Faint                     lipgloss.Color
    Accent, Accent2                        lipgloss.Color
    Success, Warning, Error, Info          lipgloss.Color
}

var Charm = Palette{
    Bg:      "#13111c",
    Fg:      "#e8e3f5",
    Dim:     "#8b80a8",
    Faint:   "#5a5074",
    Accent:  "#b794f6",
    Accent2: "#f687b3",
    Success: "#68d391",
    Warning: "#f6ad55",
    Error:   "#fc8181",
    Info:    "#63b3ed",
}

// Hacker, Odoo, Tokyo definidos igual…

type Stage string
const (
    StageDev     Stage = "dev"
    StageStaging Stage = "staging"
    StageProd    Stage = "prod"
)

// PromptColor returns the semantic token used for project name + bracket.
func (p Palette) PromptColor(s Stage) lipgloss.Color {
    switch s {
    case StageProd:
        return p.Error
    case StageStaging:
        return p.Warning
    default:
        return p.Success
    }
}

// Styles derived from a palette
type Styles struct {
    Out, Dim, Faint, Info, Ok, Warn, Err, Accent, Label lipgloss.Style
    Project, Bracket, Tilde, Dollar                     lipgloss.Style
}

func New(p Palette, stage Stage) Styles {
    pc := p.PromptColor(stage)
    base := lipgloss.NewStyle().Background(p.Bg)
    return Styles{
        Out:    base.Foreground(p.Fg),
        Dim:    base.Foreground(p.Dim),
        Faint:  base.Foreground(p.Faint),
        Info:   base.Foreground(p.Info),
        Ok:     base.Foreground(p.Success),
        Warn:   base.Foreground(p.Warning),
        Err:    base.Foreground(p.Error),
        Accent: base.Foreground(p.Accent).Bold(true),
        Label:  base.Foreground(p.Warning).Bold(true),

        Project: base.Foreground(pc).Bold(true),
        Bracket: base.Foreground(pc).Bold(true),
        Tilde:   base.Foreground(p.Info),
        Dollar:  base.Foreground(p.Fg),
    }
}
```

### Construir el prompt

```go
func RenderPrompt(s Styles, project, stage, version string) string {
    return s.Project.Render(project) +
           s.Out.Render(" [") +
           s.Bracket.Render(stage + "/" + version + ".0") +
           s.Out.Render("]:") +
           s.Tilde.Render("~") +
           s.Dollar.Render("$ ")
}
```

---

## 7. Arquitectura sugerida (Bubble Tea)

```
internal/
  theme/        ← este archivo + paletas
  cmd/          ← un archivo por comando (up.go, install.go, …)
                  cada uno expone Run(ctx, args) (<-chan Line, error)
  detect/       ← parser de docker-compose.yml + .odev.toml
  tui/
    model.go    ← Model { lines []Line, input textinput.Model, history, busy }
    view.go     ← render del buffer + prompt
    update.go   ← Update(msg) — keys, command output stream, tick
  banner/       ← ascii art con segmentos coloreados
main.go
```

**Modelo mental**: el TUI es un *único `viewport.Model`* con un buffer
de `Line` que crece. El prompt es la última línea del view, no un panel
separado. Igual que bash. Igual que odoo.sh.

Cada comando devuelve un `tea.Cmd` que produce mensajes `LineMsg` en
streaming (no esperar a que termine) — así `up`, `logs`, `test`
imprimen progresivamente.

---

## 8. Banner ASCII

Los 4 logos están en `term/data.jsx` del prototipo. Cada banner es un
arreglo de líneas, donde cada línea puede ser:
- `string` → un solo color (accent)
- `[[text, colorKey], ...]` → segmentos coloreados independientes

`colorKey` ∈ `{accent, accent2, info, success, warning, error, fg, dim}`

Para Go: porta cada banner como `[]struct{Text string; Token string}` y
renderea con el `Styles` correspondiente al theme activo.

---

## 9. Detección de versión Odoo

Orden de precedencia (primero que matchee gana):

1. `.odev.toml` en cwd → `[project] version = "18"`
2. `docker-compose.yml` → buscar `image: odoo:NN` o `image: odoo:NN.0`
3. `Dockerfile` → `FROM odoo:NN`
4. Preguntar interactivamente la primera vez, persistir en `.odev.toml`
