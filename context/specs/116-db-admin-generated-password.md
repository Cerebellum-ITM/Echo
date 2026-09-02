# Unit 116: `db-admin` deja de instalar una credencial conocida

## Goal

`db-admin` deja de escribir `admin`/`admin` en texto plano y pasa a generar
una contraseña aleatoria por corrida, guardada como un hash
`pbkdf2_sha512` estilo passlib que Odoo verifica tal cual. El login sigue
siendo `admin`. El resultado es una base recuperada **y protegida**: la
credencial existe solo en la terminal de quien corrió el comando, no en
`res_users`, ni en un backup, ni en un `db-pull`.

## El hueco que cierra

La Unit 66 optimizó por recuperar acceso, y en eso acertó. Pero el estado
que deja es una base abierta:

- **La credencial es pública.** `admin`/`admin` está documentada en el
  README, en el help y en este repo. Cualquiera que alcance el puerto de
  Odoo entra como administrador. En un dev aislado da igual; en el
  staging que alguien expuso "un rato" no.
- **El secreto queda legible en la DB.**
  [`ResetUserCredentials`](../../internal/docker/postgres.go:205) escribe
  `password = 'admin'` en claro a propósito, apoyándose en el esquema
  `plaintext` deprecado del crypt context de Odoo. Aunque la contraseña
  fuera fuerte, quedaría en `res_users.password` hasta el siguiente login
  exitoso — y de ahí viaja a cada `db-backup`, `db-pull` y `pg_dump` que
  se tome en esa ventana.
- **El guard mide la variable equivocada.** El confirm rojo depende de
  `stage == "prod"`
  ([`db.go:670`](../../internal/cmd/db.go:670)), no de qué credencial se
  va a instalar. Instalar `admin`/`admin` en un staging accesible es tan
  malo como en prod, y ahí no pregunta nada.

## Decisiones (cerradas con el usuario)

1. **Solo se genera la contraseña; el login se queda en `admin`.** El
   login no es el secreto: quien tiene el back office lo lee. Generarlo
   agrega fricción (dos cosas que copiar) y rompe datos, demos y notas que
   referencian `admin`. No se genera correo ni se toca `res_partner`.
2. **Se escribe hash `pbkdf2_sha512`, no texto plano.** Sin el hash, una
   contraseña fuerte sigue siendo legible por `psql` hasta el primer
   login; la unidad no cumpliría su propio objetivo. De paso deja de
   depender de que Odoo siga aceptando `plaintext` (deprecado desde hace
   varias versiones).
3. **El guard pasa a ser por riesgo, no por stage.** Confirma siempre en
   `prod` (resetear al admin le quita el acceso a quien tenía la
   contraseña real: eso merece una pregunta aunque la credencial nueva sea
   fuerte) **y** siempre que la credencial resultante sea conocida o débil
   (`--insecure`), en cualquier stage. `--force` sigue saltándose ambos.
4. **`--insecure` conserva el comportamiento viejo** (`admin`/`admin`)
   para bases desechables. Es un cambio de default, no una eliminación.

## Diseño

### Superficie

```
db-admin [name]                 login admin + contraseña generada (hash), impresa una vez
db-admin --password <pw>        contraseña explícita (también hasheada)
db-admin --insecure             admin/admin como hasta hoy; confirm en cualquier stage
db-admin --force                salta el confirm
```

`--password` y `--insecure` son mutuamente excluyentes: juntas son
`ErrUsage` ("`--password` and `--insecure` set the same field").

### La contraseña generada

20 caracteres de un alfabeto sin ambigüedades visuales
(`abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789`, sin `il1O0`),
tomados de `crypto/rand` con rechazo de módulo. Se copia a mano de la
terminal: la legibilidad es un requisito, no un adorno. Sin símbolos, que
solo complican pegarla en un shell.

### La salida

La contraseña se imprime **una vez**, y el mensaje dice que es la única
vez:

```
→ muutrade  admin / xR4kmPq7hTvb2ZnwCyFd (uid 2)
  shown once — Echo stores only the hash
```

Con `--insecure` la segunda línea cambia a un aviso en `palette.Warn`:
`known credentials — dev databases only`.

### El hash (`internal/odoo/passlib.go`, nuevo)

Odoo usa `passlib.CryptContext(schemes=['pbkdf2_sha512', 'plaintext'])`,
así que verifica el formato modular de passlib:

```
$pbkdf2-sha512$<rounds>$<salt>$<checksum>
```

- `rounds` = 25000 (el default de passlib, el mismo que Odoo escribe).
- `salt` = 16 bytes de `crypto/rand`.
- `checksum` = PBKDF2-HMAC-SHA512, 64 bytes de salida.
- `salt` y `checksum` van en la **adapted base64** de passlib: base64
  estándar, sin padding, con `+` sustituido por `.`.

`crypto/pbkdf2` es stdlib desde Go 1.24 y el módulo ya pide 1.25: **cero
dependencias nuevas**.

```go
// Hash returns an Odoo-compatible passlib pbkdf2_sha512 hash for the
// given password. Odoo's crypt context verifies this scheme natively, so
// the plaintext never reaches the database.
func Hash(password string) (string, error)
```

### `docker.ResetUserCredentials`

La firma no cambia — el `password` que recibe pasa a ser el hash ya
formado, y el comentario de la función deja de prometer texto plano. La
sustitución de `+` por `.` de la adapted base64 evita que el hash traiga
caracteres problemáticos, y `escapeIdent` sigue cubriendo la comilla.

## Implementación

### `internal/odoo/passlib.go` (nuevo)

`Hash` según lo anterior; `adaptedB64` privado. Un solo archivo, sin
estado.

### `internal/cmd/db.go`

- Las constantes `adminPassword` se van; queda `adminUserID` y
  `adminLogin`. Nueva `insecureAdminPassword = "admin"`.
- `dbFlags` gana `insecure bool` y `password string`; `parseDBArgs`
  aprende `--insecure`, `--password <v>` y `--password=<v>` (mismo patrón
  que `--as`).
- `RunDBAdmin`: resuelve la contraseña (explícita → `admin` si
  `--insecure` → generada), decide si hace falta confirmar
  (`stage == prod || flags.insecure`), hashea y llama a
  `ResetUserCredentials`.
- `confirmAdminReset` recibe el motivo (prod / credencial conocida) para
  que el `Description` diga cuál de los dos riesgos está avisando, en vez
  del texto único de hoy.
- `generateAdminPassword() (string, error)` junto a `RunDBAdmin`.

### Wiring

- `internal/repl/commands.go`: `"db-admin": {"--force", "--password", "--insecure"}`.
- `internal/repl/repl.go` (`helpSections`): la entrada pasa a
  `"Reset admin (uid 2) to a generated password"`, con las sublíneas de
  `--password` e `--insecure`.
- `README.md`: fila de `db-admin` en la tabla Database y el párrafo de
  prosa que hoy promete `admin`/`admin`.

### Tests

- `internal/odoo/passlib_test.go`: formato del hash (5 campos, prefijo,
  rounds), salt distinto entre corridas, y un **vector fijo** —
  salt+password conocidos → checksum esperado — para que un cambio de
  encoding rompa el test y no el login.
- `internal/cmd/db_test.go` (o donde viva `parseDBArgs`): `--password`
  en sus dos formas, `--insecure`, y el conflicto entre ambas.
- `registry_test.go` no cambia (el nombre del comando es el mismo).

## Verify when done

- [ ] `db-admin` sobre la DB activa imprime una contraseña de 20 chars y
      **entra al back office con ella** (verificación en vivo contra el
      contenedor, como pidió la Unit 66).
- [ ] `SELECT password FROM res_users WHERE id=2` devuelve
      `$pbkdf2-sha512$25000$…` — nada legible.
- [ ] Odoo **no** re-hashea al login (el scheme ya es el vigente): el
      valor de `password` es idéntico antes y después de entrar.
- [ ] `db-admin --password 'foo bar'` funciona con espacios y entra.
- [ ] `db-admin --insecure` pide confirm en un stage no-prod y deja
      `admin`/`admin`.
- [ ] `db-admin --insecure --force` no pregunta.
- [ ] `db-admin --password x --insecure` falla con `ErrUsage` sin tocar la
      DB.
- [ ] Tab completa `--password` e `--insecure` tras `db-admin`.
- [ ] `go build ./... && go vet ./... && go test ./...` verdes.
- [ ] `CHANGELOG.md` con la entrada bajo `[Unreleased] → Changed`
      (cambio de default) y `Added` (flags nuevas).
