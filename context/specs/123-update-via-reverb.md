# Unit 123: `update --remote` delega en `env_update` de Reverb

Quinta unidad del plan **echo-link-mode** (E7 en
`reverb/docs/echo-link-mode-echo-units.md`). Depende de la unidad 120 (el
contexto Reverb derivado del marcador) y de la unidad 29 de Reverb
(`POST /environments/{id}/update`).

## Goal

Que `update <mods> --remote` sobre un entorno de Reverb linkeado corra la
actualización del lado del daemon —Odoo parado, secuencias de señalización
reiniciadas, checkpoint `pre_update`, rollback si falla— en vez de un
`compose exec … odoo -u` al lado del Odoo vivo, que no da nada de eso.

## Diseño

- El interruptor es el mismo de la 120: `rsc.reverb != nil` (marcador en el
  perfil **y** credenciales locales). Sin él, el camino clásico sigue igual.
- `--all` se rechaza con el motivo de Reverb (lista los módulos): sobre una
  copia restaurada de producción `-u all` es la corrida que nunca termina.
- `--i18n` se queda en el camino clásico: el job no tiene interruptor de
  i18n, y cambiarle el significado en silencio sería peor que no delegar.
- `--no-checkpoint` mapea a `"snapshot": false`. El picker sigue siendo el
  de siempre (lista los addons del remoto); los nombres elegidos van en la
  petición.
- Los eventos del job se transmiten por `StreamOut` como líneas de log, así
  el usuario ve el stop, la salida del one-shot, el start y el rollback.

## Implementación

- `internal/reverb/ops.go`: `UpdateModules(ctx, envID, modules, snapshot)`.
- `internal/cmd/update_remote.go`: `runUpdateReverb`, el rechazo de `--all`,
  `--no-checkpoint` en `parseRemoteUpdateFlags`.
- Tests: `ops_test.go` (ruta y cuerpo), `update_remote_test.go` (el flag).

## Acceptance

- `go build ./... && go vet ./... && go test ./...` verdes.
- Contra `iza/staging` linkeado: `echo_cli update crm_iza --remote` imprime
  `update delegated to env:iza/staging job=…`, los eventos del job, y el
  entorno sigue sirviendo con `pre_update` en sus snapshots.
