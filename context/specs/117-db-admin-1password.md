# Unit 117: `db-admin --save` guarda la credencial en 1Password

## Goal

Que la contraseña que `db-admin` genera aterrice en la bóveda del usuario
en vez de en el scrollback de la terminal: `--save` crea (o actualiza) un
ítem Login en 1Password con la URL real de la instancia, leída de
`web.base.url`, para que 1Password la ofrezca sola al abrir el back
office.

## El hueco que cierra

La Unit 116 dejó la contraseña donde debía —fuera de la base— pero la
dejó en un solo lugar: la terminal. Eso mueve el problema, no lo cierra:

- **Se imprime una vez y ya.** Si la ventana se cierra, se limpia, o
  simplemente pasa el día, la credencial se perdió y el único camino de
  vuelta es correr `db-admin` otra vez.
- **Copiarla a mano invita a guardarla mal.** El destino natural de un
  "shown once" es un `.txt`, un Slack a uno mismo o un post-it. Todos
  peores que la bóveda que el usuario ya tiene y ya usa.
- **La URL correcta es la mitad del valor.** Un ítem sin URL no
  autocompleta; y la URL buena no está en la config de Echo sino en la
  instancia — `ir_config_parameter.web.base.url`, la misma que `connect`
  ya lee para abrir el navegador
  ([`connect_mint.py:36`](../../internal/cmd/scripts/connect_mint.py:36)).

## Decisiones (cerradas con el usuario)

1. **Opt-in con `--save`.** Escribir en una bóveda es un efecto
   persistente y fuera del proceso; que ocurra solo por tener `op`
   instalado sorprende, y sorprende más el día que corres `db-admin`
   contra una base de prueba.
2. **Actualiza el ítem existente en vez de crear otro.** 1Password guarda
   historial de contraseñas, así que la anterior no se pierde y no se
   acumula un ítem por reset. Sin esto, a los tres resets hay tres ítems
   con el mismo nombre y ninguno confiable.
3. **Título `Odoo <target> (<db>)`.** Agrupa por producto en la búsqueda
   y distingue dos bases del mismo proyecto.
4. **Local y remoto.** La maquinaria es idéntica en ambos caminos;
   limitarlo a remoto sería una regla más que recordar sin nada que la
   justifique. Sin `--save` no cambia nada.

## Diseño

### Superficie

```
db-admin --save                 crea/actualiza el ítem en la bóveda default de op
db-admin --save --vault <name>  fija la bóveda
```

`--vault` sin `--save` es `ErrUsage`: nombra el destino de algo que no va
a pasar.

### El pre-flight es lo que hace la unidad usable

Las verificaciones de `op` (binario en el PATH, sesión abierta) corren
**antes** del `UPDATE`, no después. El orden importa: si se comprobara al
final, un `op` sin firmar dejaría la base ya reseteada y el comando
fallando — la credencial nueva instalada y sin guardar en ningún lado,
que es el peor estado posible. Es el mismo criterio del lint pre-flight
de `deploy` (Unit 110): lo que puede abortar, aborta antes de tocar nada.

Después del reset ya no se aborta: si el guardado falla (red, bóveda,
permisos), la contraseña **se imprime igual** y el error se reporta como
WARNING. Perder el ítem es recuperable copiando de pantalla; perder la
contraseña no.

### La URL

Una query escalar más, por el mismo transporte que el `UPDATE`:

```sql
SELECT value FROM ir_config_parameter WHERE key = 'web.base.url'
```

Odoo reescribe ese parámetro con el host de la primera petición que
recibe salvo que exista `web.base.url.freeze`, así que puede venir vacía
o apuntando a `localhost:8069`. En esos dos casos el ítem se guarda **sin
URL** y se avisa en una línea: un ítem sin URL es incómodo, uno con la
URL equivocada autocompleta en el sitio equivocado.

### El ítem

Categoría `LOGIN`, campos `username` = `admin` y `password` = la
generada, más la base URL como sitio. El título sale de:

- remoto: `targetLabel(rsc)` — el nombre del target
- local: `statusProjectName(cfg, …)` — `compose_project` o el basename
  del proyecto

### Nada sensible en `argv`

`op` lo advierte en su propia ayuda: *"Command arguments get logged in
your command history, and can be visible to other processes"*. Así que la
contraseña **nunca** viaja como argumento:

- crear: `op item create -` con el JSON del template por **stdin**.
- actualizar: `op item get <title> --format json` → se parcha el campo
  `password` en memoria → `op item edit <id>` con el JSON parcheado por
  **stdin**.

Parchar el ítem completo, en vez de mandar un template mínimo, es lo que
preserva las secciones, notas y campos custom que el usuario le haya
agregado a mano.

La URL **no** va en el JSON sino en el flag `--url`, que existe tanto en
`create` como en `edit`: no es un secreto, y así las dos rutas tienen la
misma forma. En el update, cuando la instancia no da una URL usable se
re-declara la que el ítem ya traía, para no dejarlo sin sitio.

Detalle del CLI verificado con `--dry-run`: `op` solo reconoce la
plantilla `-` cuando stdin es un **pipe**; con un archivo redirigido pide
`--category`. `os/exec` siempre le entrega un pipe al hijo, así que del
lado de Echo se cumple por construcción.

### Ítem existente: cómo se busca

`op item get <title> [--vault <v>] --format json`. Un ítem inexistente
sale con exit 1 y el mensaje `isn't an item`; cualquier otro exit≠0 es un
fallo real y se propaga en vez de degradar a "crear" — si no, un problema
de red se convertiría en un ítem duplicado.

## Implementación

### `internal/cmd/onepassword.go` (nuevo)

```go
// opAvailable reports whether the 1Password CLI is installed and holds an
// unlocked session.
func opAvailable(ctx context.Context) error

// opSaveLogin creates the item, or updates the one already titled title,
// and reports which of the two happened.
func opSaveLogin(ctx context.Context, vault, title, username, password, url string) (created bool, err error)
```

`opItemGet` / `opRun` privados para el envoltorio de `exec.CommandContext`
con stdin y captura de stderr. La detección de "no existe" vive en un
`errors.Is`-able `errOPItemMissing`.

### `internal/cmd/db.go`

- `dbFlags` gana `save bool` y `vault string`; `parseDBArgs` aprende
  `--save`, `--vault <v>` y `--vault=<v>`.
- `RunDBAdmin` / `runDBAdminRemote`: pre-flight `opAvailable()` cuando
  `flags.save`, antes de resolver la credencial.
- Tras el reset, `saveAdminCredential(...)` lee la base URL, arma el
  título y llama a `opSaveLogin`; los fallos salen por `opts.log` como
  WARNING, no como `error`.
- La base URL se lee con `docker.ConfigParameter` (local) y una
  `remotePsqlScalar` (remoto).

### `internal/docker/postgres.go`

```go
// ConfigParameter returns an ir_config_parameter value, empty when the
// key is absent.
func ConfigParameter(ctx context.Context, composeCmd, dir, dbContainer, user, db, key string) (string, error)
```

### Wiring

`commandFlags["db-admin"]` += `--save`, `--vault`; help y README con las
dos líneas nuevas.

### Tests

- `onepassword_test.go`: el parcheo de un ítem existente **preserva**
  secciones, notas y campos custom; escribe la contraseña nueva; la
  agrega si el ítem no tenía campo `password`; reporta la URL primaria
  para poder re-declararla; y deja el cuerpo sin `urls`.
- `db_test.go`: `--save`, `--vault` en sus dos formas, y `--vault` sin
  `--save` como `ErrUsage`.
- La detección de `isn't an item` contra el texto real del CLI 2.34.

## Verify when done

- [ ] `db-admin --save` crea el ítem con título, usuario, contraseña y
      URL correctos, y 1Password lo ofrece al abrir el back office.
- [ ] Correrlo dos veces actualiza el mismo ítem, y la contraseña previa
      queda en el historial del ítem.
- [ ] Un ítem con una nota agregada a mano conserva la nota tras el
      update.
- [ ] Con `op` desinstalado o sin sesión, `--save` aborta **antes** de
      tocar la base (la contraseña anterior sigue sirviendo).
- [ ] Un fallo de guardado después del reset imprime la contraseña y un
      WARNING, y sale 0.
- [ ] `web.base.url` vacía o `localhost` ⇒ ítem sin URL, con aviso.
- [ ] `--vault` sin `--save` es `ErrUsage`.
- [ ] La contraseña no aparece en `ps` durante la corrida.
- [ ] `go build ./... && go vet ./... && go test ./...` verdes.
- [ ] `CHANGELOG.md` bajo `[Unreleased] → Added`.
