# Unit 120: modo Reverb derivado del perfil, sin flag

Tercera unidad del plan **echo-link-mode** (E3), apilada sobre la 118 y la
119.

## Goal

Que un target linkeado que resulta ser un entorno de Reverb obtenga **solo**
por estar linkeado los comportamientos que hoy exige `-E`: checkpoints como
snapshots, `up`/`stop`/`restart` por la API (para que la UI no lea drift),
push al overlay con el aviso de sombra, y `push --clean` vaciando el overlay.

## El hueco que cierra

Los comportamientos Reverb existen y funcionan, pero cuelgan del flag. `-E`
resuelve el entorno por HTTP en cada invocación, no persiste nada
—`link --show`, el pre-flight del skill `odoo-probe`, no tiene qué mostrar—
y exige el token en cada laptop. La unidad 118 y la 119 ya dejaron el camino
clásico llegando al mismo sitio (overlay por `[push] path` del servidor,
base por `docker exec`); falta que las tres cosas que **sí** necesitan la API
se enciendan solas.

## Decisiones

1. **El interruptor es "marcador + credenciales".** `rsc.reverb` se puebla
   en el camino clásico cuando el perfil del servidor trae `[reverb]` **y**
   el cliente tiene con qué hablarle a la API. Sin credenciales locales todo
   sigue clásico y se emite una línea INFO por invocación diciendo qué falta.
   Es el invariante que hace que no haya que tocar ninguno de los siete
   sitios que ya ramifican en `rsc.reverb != nil`: donde ese campo está
   puesto, hay API.
2. **`paths` se deriva del perfil, no de un resolve.** `Overlay` es el
   `[push] path` que el servidor declara —Reverb escribe ahí exactamente el
   overlay—, `ComposeDir` es el `remote_path` del target, y `Addons` queda
   vacío: `isUnderAddons` ya trata el vacío como "no se puede comprobar, no
   se rechaza nada". Así se conserva la promesa del plan: cero round trips
   HTTP para lo que no los necesita.
3. **El `api_url` del marcador gana.** El host declara dónde vive su daemon;
   moverlo no obliga a editar cada laptop. `reverbClientFor` lo prefiere
   sobre el `[reverb] url` local, y cae a éste cuando el marcador no lo trae.
4. **El token sigue siendo local.** El marcador no lleva secretos — es un
   archivo en un host compartido. Sin `[reverb] token` no hay API, y el
   punto 1 se encarga.
5. **`reverbDeferred` no se toca.** Las negativas de `deploy`, `watch` e
   `i18n-pull` se aplican por el prefijo `env:` del `from`, o sea solo al
   camino `-E`. En modo link nunca dispararon: `deploy --remote` y `watch`
   ya son el loop rsync-al-overlay que el plan quiere. El mapa entero se va
   con `-E` en la unidad siguiente.

## Diseño

### El poblado

```go
// reverb.go
func reverbEnvFromProfile(cfg, prof, remotePath) (*reverbEnv, string)
```

devuelve el entorno, o `nil` más el motivo para la línea INFO. `reverbEnv`
gana `apiURL`, tomado del marcador.

`resolveRemoteShell` lo llama después de leer el perfil, y emite

```
INFO reverb: Reverb environment; checkpoints and lifecycle go through
     compose — set [reverb] token to use the Reverb API   env=iza/staging
```

cuando hay marcador sin credenciales.

### Lo que se enciende solo

| Comando | Con marcador + credenciales |
| --- | --- |
| `checkpoint create/list/rm` | snapshots por API (`runCheckpointReverb`) |
| `up` / `stop` / `restart` | `runReverbEnvAction`, sin drift en la UI |
| `push`, `push --dirty` | overlay + aviso de sombra (`GET /environments/{id}/overlay`) |
| `push --clean` | vacía el overlay en vez de exigir un target git-deploy |

### `link --show`

Una línea más cuando el perfil trae marcador:

```
INFO  reverb env    env=iza/staging  id=116  api=on
```

`api=on|off` dice si las credenciales locales están puestas, que es
exactamente la pregunta que el pre-flight del skill necesita responder.

## Tests

1. `reverbEnvFromProfile`: sin marcador ⇒ nil sin motivo; con marcador y sin
   token ⇒ nil con motivo; con ambos ⇒ entorno con `id`/`project`/`env`,
   `paths.Overlay` = `[push] path` del perfil y `apiURL` del marcador.
2. El `apiURL` local se usa cuando el marcador no trae `api_url`.
3. `reverbClientFor` prefiere el del entorno.

## Acceptance

- `go build ./... && go vet ./... && go test ./...` verdes.
- Contra un entorno con perfil: `checkpoint create --remote` deja un
  snapshot en la UI, `stop --remote`/`up --remote` no dejan drift.
