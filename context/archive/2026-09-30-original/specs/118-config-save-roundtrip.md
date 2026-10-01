# Unit 118: `SaveGlobal` / `SaveProject` conservan todas las secciones

Primera de las cuatro unidades del plan **echo-link-mode** (E1 en
`~/Documents/Projects/dev_tools/reverb/docs/echo-link-mode-echo-units.md`):
que un entorno de Reverb se use como un target linkeado clásico, sin `-E`.

## Goal

Que escribir la config nunca borre lo que no se estaba escribiendo. Hoy
registrar un connect target reescribe `global.toml` desde cero y se lleva por
delante `[reverb]`, `[checkpoint]`, `[push]`, `[deploy]`, `[promote]` e
`icons`; y `SaveProject` tira `[promote]` del perfil y los campos
`git_deploy`/`git_branch`/`git_path` del `[connect]`.

## El hueco que cierra

`SaveGlobal` (`internal/config/config.go:818`) construye un `globalFile`
literal con nueve campos y lo encodea encima del archivo. Todo lo que el
`Config` no modela — o modela pero el literal no menciona — desaparece:

- **Bloquea el gesto que la unidad 30 de Reverb reparte.** El plan es que
  Reverb entregue un snippet `[connect_targets.<env>]` listo para pegar; la
  registración es justo la llamada (`SaveConnectTarget` → `SaveGlobal`) que
  borra el `[reverb] token`. Configurar el target rompe el modo Reverb.
- **Ya muerde hoy, fuera de Reverb.** `deploy --set-git-branch` sobre un
  binding de directorio (`deploy_setbranch.go:104`) escribe
  `cfg.ConnectGitBranch` y llama a `SaveProject`, que no emite ese campo:
  la rama se "guarda" y no queda nada en disco.
- **Es una clase de bug, no un caso.** Cada tabla nueva de config nace con
  la misma trampa: si el escritor no la nombra, el siguiente `save` la
  borra. El arreglo correcto es que el escritor deje de ser un rebuild.

## Decisiones

1. **Load-modify-write, no rebuild.** Ambos writers decodean el archivo
   actual en su struct de archivo (`globalFile` / `projectFile`) y solo
   sobreescriben los campos que el `Config` posee. Lo desconocido para el
   `Config` sobrevive porque nunca se toca.
2. **"Poseer" un campo significa asignarlo siempre**, también cuando queda
   vacío. Si `Prompt` o `CmdLogs` vuelven a default, se asigna `nil`
   explícito: la semántica de hoy (una sección que vuelve a default se
   limpia) se conserva, y no aparece la ambigüedad "no lo escribí porque no
   cambió" vs "no lo escribí porque lo quité".
3. **`icons` y `[promote]` del proyecto quedan como NO poseídos.** Ningún
   camino los escribe hoy; preservarlos desde disco es la conducta correcta
   y evita que un `Config` armado a mano los vacíe.
4. **Las tablas se auditan contra el loader.** Todo lo que `Load` lee tiene
   que estar declarado en el struct de archivo, o el round-trip lo pierde
   igual. Hoy `globalFile` y `projectFile` ya declaran todas; la unidad lo
   fija con un test.

## Diseño

### `SaveGlobal`

```go
g := loadGlobalFile(path)   // lo de disco, o cero si no existe / no parsea
g.Theme = cfg.Theme
...                         // los nueve campos que el Config posee
g.Prompt = nil              // y su rama de asignación
g.CmdLogs = nil
```

Campos poseídos: `Theme`, `Logo`, `Banner`, `ComposeCmd`, `LogDBMax`,
`ConnectTargets`, `ProjectAliases`, `Prompt`, `CmdLogs`.
Preservados: `Icons`, `Checkpoint`, `Push`, `Deploy`, `Promote`, `Reverb`.

### `SaveProject`

Mismo patrón sobre `projects/<key>.toml`. El `[connect]` que emite gana los
tres campos de topología git, y el gate de la sección los incluye — un
target git-deploy sin `ssh_host` local ya no se pierde:

```go
if cfg.ConnectSSHHost != "" || cfg.ConnectRemotePath != "" ||
    cfg.ConnectChromePath != "" || cfg.ConnectGitDeploy ||
    cfg.ConnectGitBranch != "" || cfg.ConnectGitPath != "" {
```

Campos poseídos: los escalares del perfil, `Connect`, `Push`, `Checkpoint`
(solo si `CheckpointSource == "project"`, regla de la unidad 104: una
política que vive en global no se copia hacia abajo) y `Deploy`.
Preservado: `Promote`.

### Lo que NO cambia

- `writeAtomic` y los permisos.
- `savePromote`, que ya hacía load-modify-write; queda como estaba.
- La regla de `CheckpointSource`: con load-modify-write el resultado es el
  mismo, porque un perfil con `[checkpoint]` en disco se carga con
  `source == "project"`.

## Tests (`internal/config`)

1. `global.toml` fixture con las seis tablas no poseídas más valores
   distintivos → `SaveConnectTarget` con un target nuevo → recargar y
   afirmar que las seis siguen ahí con su valor y que el target se agregó.
2. Mismo fixture → cambiar el theme y `SaveGlobal` → `[reverb] token`
   intacto.
3. Perfil de proyecto con `[promote] branch` y `[connect] git_deploy = true`
   → `SaveProject` de un `Config` cargado con `Load` → ambos sobreviven.
4. Round-trip de vaciado: un `Config` con prompt default limpia `[prompt]`.

## Acceptance

- `go build ./... && go vet ./... && go test ./...` verdes.
- Con un `global.toml` que tenga `[reverb]`, correr
  `echo_cli connect --add-target …` deja el token en su lugar.
