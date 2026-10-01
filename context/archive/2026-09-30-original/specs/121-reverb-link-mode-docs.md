# Unit 121: link mode como el camino, `-E` en deprecación

Cuarta y última unidad del plan **echo-link-mode**. La E4 del plan pide
**borrar** `-E`; esta unidad hace la mitad reversible —deprecarlo y
documentar el camino que lo reemplaza— y deja el borrado escrito para
cuando la unidad 30 de Reverb esté en el host.

## Goal

Que quien use `-E` se entere de que hay un camino mejor y cuál es, sin
quitarle el único que hoy funciona.

## Por qué no se borra todavía

Las unidades 118–120 dejaron el modo link completo del lado de Echo, pero
lo que **lee** es un archivo que Reverb todavía no escribe: el perfil
`~/.config/echo/projects/<sha256(compose_dir)>.toml` de su unidad 30. Hasta
que eso esté desplegado, un entorno de Reverb no tiene perfil que leer y
`-E` es el único camino vivo. Borrarlo ahora es dejar el repo sin forma de
apuntar a un entorno hasta que aterrice la otra mitad.

## Diseño

### La línea de deprecación

`resolveReverbShell` es el único punto de entrada del camino `-E`: cada
comando llega ahí. Emite, una vez por invocación, una WARNING nombrando el
reemplazo concreto:

```
WARNING reverb: -E is deprecated — register the environment as a connect
        target and `link` it; the server profile carries the rest
        env=iza/staging
```

No cambia nada del comportamiento: el flag sigue resolviendo por HTTP.

### La ayuda

El bloque `reverbHelpEntries` del REPL deja de describir `-E` como *la*
forma de apuntar a un entorno y describe primero el modo link (registrar el
target, `link`, `[reverb] token` opcional para snapshots y ciclo de vida),
con `-E` marcado como deprecado abajo. La fila `not yet` de `deploy`/`watch`
se aclara: es una limitación **de `-E`**, no del modo link, donde ambos son
el loop rsync-al-overlay.

### El README

La sección `## Reverb mode` abre con el modo link —el snippet de
`[connect_targets.<env>]`, `link`, y qué sale gratis del perfil— y `-E`
queda debajo como camino heredado.

## Lo que la E4 borrará (cuando Reverb unit 30 esté en el host)

- El parseo de `-E`/`--env` en `remoteFlagsIn`, el prefijo `env:`
  (`reverbRefPrefix`, `reverbRefIn`, `ReverbRef`), `resolveReverbShell` y el
  mapeo payload→contexto. El cliente HTTP de `internal/reverb` se queda: la
  unidad 120 lo usa.
- `reverbDeferred` y `requireNoReverb` completos, con sus llamadas en
  `deploy`, `watch` e `i18n-pull`: sus negativas se aplican por el prefijo
  `env:`, o sea solo a `-E`.
- `-E` en el parser de flags de `update_remote.go` y en `commandFlags`.
- `[reverb] compose_cmd` y `ssh_host`: el compose sale del `global.toml` del
  host y el SSH del connect target. Quedan un release como no-ops con línea
  de deprecación, y se van.
- Los casos de `reverb_test.go` que ejercitan el flag.

## Acceptance

- `go build ./... && go vet ./... && go test ./...` verdes.
- `echo_cli logs -E iza/staging` sigue funcionando y avisa una vez.
