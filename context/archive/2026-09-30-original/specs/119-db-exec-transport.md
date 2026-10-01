# Unit 119: el `db_container` que no es un servicio de compose

Segunda unidad del plan **echo-link-mode** (E2 en
`~/Documents/Projects/dev_tools/reverb/docs/echo-link-mode-echo-units.md`),
apilada sobre la 118.

## Goal

Que `checkpoint`, `db-pull`, `db-list` y la mitad DB de `i18n-pull` funcionen
contra un target cuyo Postgres no vive en el compose del proyecto — el caso
de todo entorno de Reverb, donde la base es del **proyecto** y el compose es
del **entorno**.

## El hueco que cierra

Todo lo que Echo corre en la base pasa por `remoteDBCmd`
([`i18n_pull.go:553`](../../internal/cmd/i18n_pull.go:553)) y por los
constructores de dump/restore de
[`checkpoint_remote.go`](../../internal/cmd/checkpoint_remote.go:203), que
emiten `cd <remote_path> && <compose> exec -T <db_container> …`. Eso asume
que `db_container` es un **servicio** del compose del directorio. En Reverb
no lo es: el `compose.yml` del entorno solo trae Odoo, y Postgres corre en el
compose del proyecto. El comando falla con `no such service` aunque el
contenedor esté arriba y aunque el mismo nombre sea correcto como
`--db_host` (la red sí lo resuelve).

Es además el bug que hoy hace inservible `-E` para la base: `reverb.go` mapea
`containers.db` —un **nombre de contenedor**— sobre el campo que todos los
constructores consumen como servicio.

## Decisiones

1. **El perfil del servidor dice qué transporte usar.** La unidad 30 de
   Reverb escribe una tabla marcador `[reverb]` en
   `projects/<key>.toml`; Echo la lee y elige `docker exec -i` en vez de
   `compose exec -T`. Sin marcador nada cambia.
2. **Fallback genérico además del marcador.** Si un `compose exec` falla y
   el error trae `no such service`, se reintenta una vez con la forma
   `docker`. Cubre los targets hechos a mano cuya base se salió del compose,
   que hoy fallan igual y no tienen marcador que poner. Va **sin** la línea
   INFO que pedía el plan: `internal/cmd` no tiene logger ambiente —cada
   comando hila su propio callback— y los constructores de DB están seis
   niveles abajo de quien lo tiene. Cablearlo por una línea informativa
   costaba más de lo que aclara.
3. **`docker exec -i`, no `-it`.** El stdin importa —`pg_restore < file`
   viaja por ahí— y no hay TTY en una corrida por SSH.
4. **Un solo constructor de `connectTarget` desde el perfil.** Los cinco
   sitios que armaban el struct a mano pasan por `remoteConnectTarget`, que
   es donde se deriva el transporte. Sin eso, la próxima ruta remota nace
   otra vez con el default equivocado.

## Diseño

### El marcador en el perfil

`projectFile` gana `Reverb *reverbMarkerFile` (`toml:"reverb"`) con
`env_id`, `project`, `env` y `api_url`; `RemoteProfile` gana
`Reverb *ReverbMarker` con los mismos campos exportados.
`ParseRemoteProfile` lo copia; tabla ausente ⇒ `nil`. Solo se lee del perfil
de **proyecto**: es una propiedad del entorno, no del host.

### El transporte

`connectTarget` gana `dbExec string` — `"compose"` (default, cero valor) o
`"docker"`. `remoteConnectTarget(prof)` lo pone en `"docker"` cuando
`prof.Reverb != nil`; el camino `-E` de `reverb.go` lo fija directo, porque
ahí los `containers.*` del payload son nombres por definición.

Los constructores de comando se parametrizan por modo:

```go
dbExecInner(t, mode, argv)          // compose: <compose> exec -T <db> …
                                    // docker:  docker exec -i <db> …
dbExecCmd(remotePath, t, mode, argv) // compose: cd <path> && <inner>
                                     // docker:  <inner>
```

El `cd` sobra en la forma docker (no hay compose que resolver), pero los
constructores que redirigen a un archivo del servidor
(`remoteDumpToFile`, `remoteRestoreDump`) lo siguen poniendo ellos mismos
porque la ruta del dump es relativa al proyecto.

### El fallback

```go
withDBExecFallback(t, func(mode string) error { … })
```

corre el modo declarado por el target y, solo si el modo era `compose` y el
error trae `no such service`, repite una vez con `docker`. Los tres sitios
que solo buscan salida usan el envoltorio listo
`runRemoteDBCmd(ctx, run, sshHost, remotePath, t, argv)`, parametrizado por
el runner SSH (`runSSH` o el seam `ckptRunSSH`) para no romper los tests.

El reintento no se memoiza: `remoteShellContext` viaja por valor, y pagar un
round trip extra por comando en un target sin marcador es más barato que
hilar un puntero por toda la superficie remota. Con marcador no se paga
nada.

## Tests (`internal/cmd`, `internal/config`)

1. Table test de `dbExecCmd`/`dbExecInner`: formas `compose` y `docker`,
   con el quoting de siempre.
2. `ParseRemoteProfile` con y sin `[reverb]` en el perfil de proyecto.
3. `remoteConnectTarget`: marcador ⇒ `dbExec == "docker"`, sin marcador ⇒
   `"compose"`.
4. `withDBExecFallback` con un runner falso que devuelve `no such service`
   la primera vez: corre dos veces y la segunda en modo docker. Y un error
   distinto: corre una sola vez.

## Alcance

Entra todo lo que corre **dentro** del contenedor de Postgres por SSH:
`remotePsqlScalar`/`remotePsqlExec` (y con ellos `checkpoint` entero), el
`df` del pre-flight, el dump y el restore de checkpoints, el `pg_dump` de
`db-pull` y las consultas de módulos de `deploy` e `i18n-pull`. `db-list`
queda fuera porque hoy es local-only (`docker.ListDatabasesDetailed` contra
el stack del directorio): no tiene rama remota que arreglar.

Los usos de `db_container` como `--db_host` no se tocan: ahí el nombre del
contenedor siempre fue lo correcto.

## Acceptance

- `go build ./... && go vet ./... && go test ./...` verdes.
- Contra un entorno de Reverb con perfil (unidad 30 desplegada):
  `checkpoint create --remote`, `db-list --remote`, `db-pull --remote`.
