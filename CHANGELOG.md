# Changelog

All notable changes to Echo are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **`watch` sin rama abre un selector de ramas.** El branch pasa a ser
  opcional: `watch` (sin argumento) lista las ramas locales del repo —
  ordenadas por commit más reciente— en el picker de una sola selección y
  observa la elegida; `watch <branch>` sigue igual. El picker corre **antes**
  de tocar SSH, así que cancelarlo no cuesta nada; sin TTY se exige la rama
  como argumento (ErrNonInteractive). Nuevos helpers `pickWatchBranch`/
  `gitLocalBranches`; `parseWatchArgs` ya no exige el positional.
- **`logview`: selección de logs por bloques y copia de la selección.** La
  vista de detalle gana un cursor de línea (`❯`) y con **Espacio** marca/
  desmarca el **bloque** bajo el cursor — un bloque es una línea con nivel más
  sus líneas sin nivel siguientes (una entrada de log y su continuación, p. ej.
  un `ERROR` con su traceback, que ya heredan su color) hasta el siguiente
  nivel. Las líneas seleccionadas muestran `✓` en un gutter y `Ctrl+O` copia
  **solo los bloques marcados** (o, si no hay ninguno, todo lo visible, como
  antes). `esc` limpia primero el filtro de texto, luego la selección, y
  después vuelve a la lista; editar el filtro o cambiar de nivel re-ancla la
  selección (va por posición). Helpers puros `blockStartOf`/`blockEndOf`/
  `selectedLines` con tests.
- **`logview --from <t>` / `--remote`: navega el historial de logs de un
  destino remoto por SSH.** Antes `logview` solo abría el `cmd-logs/` local y
  cualquier flag remoto caía en "unknown flag". Ahora acepta `--from <target>`
  (destino de connect nombrado) y `--remote` (el `link` de este directorio) y
  lee el historial del servidor de solo lectura: resuelve el target, deriva su
  directorio `cmd-logs/` del **`ProjectKey` determinista** del `remote_path`
  (la misma clave con la que Echo guardó los registros allá) y streamea todos
  los records en una sola pasada SSH. El navegador es idéntico al local; el
  título muestra el destino y la vista de detalle carga cada corrida desde el
  mapa precargado (loader inyectado en el modelo). `--clear` sigue siendo
  local. Nuevo `internal/cmd/logview_remote.go`
  (`FetchRemoteCmdLogs`/`parseRemoteCmdLogs`, con tests) y flags remotos en el
  autocompletado. `parseLogviewArgs` gana `--from`/`--remote`.

### Fixed
- **`logview`: la vista de detalle rebasaba la altura del terminal y empujaba
  el header (con el filtro) fuera de vista.** El cálculo de la ventana de
  scroll (a) contaba solo 4 líneas de chrome cuando el detalle dibuja 5
  (head, filtro, blank, blank, footer) y no reservaba las filas de los
  indicadores `↑/↓ N more`, y (b) medía cada log-line como **una** fila
  aunque las líneas largas se **envuelven** a varias filas visuales — así el
  cuerpo desbordaba y el terminal scrolleaba el header hacia arriba. Ahora el
  modelo captura también el `Width` del `WindowSizeMsg` y el llenado es
  *wrap-aware*: `visualRows` mide las filas envueltas de cada línea (ignorando
  ANSI vía `lipgloss.Width`), `bodyWindow` llena la ventana por filas visuales,
  `maxTopOffset` mantiene la última línea alcanzable y `pageBack` pagina
  correctamente; `logviewChrome` pasa a 5 y `maxRows` reserva 2 filas para los
  indicadores. `cmdBudget` de la lista ahora escala con el ancho real. Helpers
  puros con tests en `logview_test.go`.

### Changed
- **README: documentados los comandos del 0.21.0 y añadidos sus GIFs de demo.**
  El `README.md` no cubría los comandos entrados en el último release; ahora
  documenta `push`/`watch`/`compare` (nueva sección **Sync & compare**),
  `db-pull` (sección Database) y `logview` (Output & reporting), con filas en la
  tabla de Status y cuatro GIFs nuevos grabados con VHS en modo simulación
  (`demo/gifs/{push,db-pull,compare,logview}.gif`). Se añadieron sus tapes
  (`demo/tapes/*.tape`), las funciones de simulación fieles al render real en
  `demo/sim/echo-sim.sh` (árbol de cambios de `push`, secuencia dump→restore de
  `db-pull`, tabla de estado + drill-down de `compare`, TUI alt-screen de
  `logview`), los colores de logger (FNV-1a % 8) de los nuevos loggers y los
  glyphs seti/md de tipo de archivo en `demo/sim/glyphs.sh`.

## [0.21.0] — 2026-07-08

### Added
- **`compare --all` — estado de sincronización del módulo completo** (Unit 86).
  Extiende el `compare` de la Unit 80 con un modo a nivel de módulo:
  `compare <mod> --all [--from <t>|--remote] [--copy]` compara **todo el
  módulo** contra su copia en Docker y muestra una tabla de estado por archivo
  — `changed / added / missing / equal` — cerrando con un veredicto
  `echo.compare: module compared module=… from=… changed=N added=N missing=N
  equal=N` (o un único `in sync` si todo coincide). En TTY (y sin `--copy`),
  los archivos que difieren alimentan un loop interactivo: se elige un archivo,
  se ve su diff (el render de la Unit 80), se vuelve, hasta `esc`. Comparación
  por **checksums, no N lecturas**: cada lado se hashea en un solo comando
  (local con `crypto/md5` in-process; contenedor local/remoto con un
  `find -exec md5sum {} +` vía `docker.Exec` o el transporte SSH host/conf de
  la Unit 79) y se comparan como mapas; `skipViewPath` filtra ambos lados.
  `--copy` copia la tabla + veredicto en texto plano en vez de entrar al loop;
  no-TTY imprime solo la tabla. Un módulo ausente en el contenedor lista todos
  sus archivos como `added`. `compare` sin `--all` es idéntico a la Unit 80.
  Nuevo `internal/cmd/compare_all.go` (`RunCompareAll`/`CompareModuleFile`/
  `diffModuleSets`/`localModuleHashes`/`containerModuleHashes`/
  `remoteModuleHashes`/`parseMD5Sums`, tipo exportado `FileStatus`);
  `parseCompareArgs` gana `--all` y se extrajo `compareFetchContainer` de
  `RunCompare`. Tests en `compare_all_test.go`.
- **`db-pull` — clona una base de datos remota al stack local** (Unit 85).
  Nuevo comando `db-pull [--from <t>|--remote] [--as <name>]
  [--neutralize|--no-neutralize] [--filestore] [--force]`: hace `pg_dump -Fc`
  del target remoto sobre SSH **streameando el stdout binario directo a
  `./backups/<db>_<target>_<ts>.dump`** (sin buffer en memoria ni temp remoto,
  con línea de progreso `pulled N MB…`), lo restaura en el Postgres local bajo
  un nombre distinto reusando la maquinaria de `db-restore`, y —por defecto,
  solo cuando el origen es prod— lo **neutraliza** (`db-neutralize`). El lado
  remoto es **solo lectura** (un `pg_dump`, más un tar opcional del filestore):
  no hay gate de prod remoto; todas las mutaciones ocurren en local con sus
  propios guards. El `.dump` queda como un backup normal que el picker de
  `db-restore` puede volver a restaurar. `--as` fija el nombre local (default
  `<remoteDB>_<target>` saneado a identificador Postgres); un DB local con ese
  nombre se reemplaza con `--force` (termina conexiones). `--filestore` también
  trae los adjuntos (tar del filestore del contenedor Odoo remoto → extracción
  local → copia al contenedor local bajo el nuevo nombre; si no hay filestore,
  WARNING y sigue). La DB activa **no** cambia: el cierre sugiere
  `db-use <name>`. Nuevo transporte `runSSHToFile` (stdout binario → archivo
  con progreso, borra el parcial si falla) y `restoreBackupFile` extraído de
  `RunDBRestore` para restaurar un dump concreto sin picker. Nuevos
  `internal/cmd/db_pull.go` (`RunDBPull`/`parseDBPullArgs`/`sanitizeDBName`/
  `pullFilestore`/`humanBytes`); registrado en la familia `db-*`. Tests
  `db_pull_test.go` (tri-state de neutralize, `sanitizeDBName`, `runSSHToFile`
  happy-path + fallo-sin-parcial vía seam).
- **`logview` — navegador interactivo del historial de logs por comando**
  (Unit 82). Nuevo comando alt-screen (bubbletea) sobre lo que persiste la
  Unit 81, con el mismo lenguaje visual que el `help` pager y el picker (barra
  `│` izquierda teñida por el stage). Dos niveles: una **lista de corridas**
  (más recientes primero, columnas tiempo · comando · estado · nº de líneas ·
  db, con filtro por escritura sobre el comando) y, al `enter`, la **vista de
  log** de esa corrida donde escribir filtra las líneas en vivo y `tab` cicla
  el filtro de nivel como **umbral mínimo** (all → DEBUG+ → INFO+ → WARNING+ →
  ERROR+ → CRITICAL, `shift+tab` hacia atrás; las líneas sin nivel solo se ven
  en "all"). Ambos filtros componen (AND). `ctrl+o` copia exactamente las
  líneas visibles al portapapeles; `esc` limpia el filtro activo y si no hay,
  navega atrás/afuera; `q` cierra; `ctrl+x` cierra Echo entero. Cada línea se
  colorea como se vio en vivo (`renderLogLine`/`kindFromLevel`). Escotillas
  headless: `--list` (tabla plana sin TTY), `--last` (abre la corrida más
  reciente directo), `--clear [--force]` (borra el historial del proyecto tras
  confirmar). Es meta-comando (no resetea `lastOutput`, así `copy-last` sigue
  copiando el comando anterior) y no se registra a sí mismo. Cierra con una
  línea `echo.logview`. Helpers puros testeables `filterRuns`/`filterLogLines`/
  `cycleLevel`/`runStatusLabel`/`logviewTimeLabel` en `internal/repl/logview.go`;
  `CmdLogMeta` gana `LineCount` para listar sin abrir cuerpos.
- **Historial de logs por comando persistido a disco** (Unit 81). Cada comando
  despachado (REPL, one-shot y pasos de recipe por igual) guarda su salida
  capturada como un registro JSON por ejecución en
  `~/.config/echo/cmd-logs/<clave-de-proyecto>/`, con nombre
  `<unix-millis>-<comando>.json` (orden lexicográfico = cronológico). Cada
  registro lleva la metadata que el navegador (Unit 82) necesita sin abrir el
  cuerpo (comando completo, verbo, db, stage, `from` remoto, exit, duración,
  errores/warnings, truncado) más las líneas etiquetadas por nivel reutilizando
  `config.ReportLine`. El guardado es best-effort (un fallo de escritura nunca
  rompe ni retrasa el comando). No se registran los meta-comandos
  (`help`/`clear`/`copy-last`), `report`, `logview`, ni las capturas vacías.
  Retención podada oportunistamente (al arrancar la sesión y tras cada
  guardado) con dos perillas en `[cmd_logs]` de `global.toml`: `retention_days`
  (7 por defecto, 0 = para siempre), `max_runs` (500 por defecto, 0 = sin
  límite) y `disabled` (apaga escritura y poda). Helpers en
  `internal/config/cmd_logs.go` (`SaveCmdLog`/`ListCmdLog`/`LoadCmdLog`/
  `PruneCmdLogs`/`ClearCmdLogs`) y el sink en `internal/repl/cmdlog.go`.
- **Iconos nerd-font por tipo de archivo en el árbol de `push`, con toggle y
  fallback** (Unit 83). Cuando están habilitados, cada archivo del árbol de
  cambios lleva su glyph nerd-font (set seti: `.py` , `.xml` , `.po` 󰗊,
  `.csv`, `.js`, `.json`, `.yml`, `.md`, imágenes…, con glyph de carpeta en
  los nodos de directorio); si no, el árbol se dibuja sin glyphs. Se controla
  con el nuevo setting `icons` en `global.toml` (`auto` por defecto / `on` /
  `off`) y el override por env `ECHO_ICONS`; en `auto` se activan solo en una
  terminal interactiva que no sea "plana" (`$TERM` distinto de `linux`/`dumb`)
  y se apagan cuando la salida va a un pipe/archivo (`--log`, CI), para no
  ensuciar logs. El mismo toggle gobierna el glyph de la lista de `modules`
  (antes incondicional). La forma en texto plano (copy-last/`--log`) nunca
  lleva iconos. Helpers `resolveIcons`/`fileIcon`/`parseIconToggle`.

### Fixed
- **`push`/`watch` re-sincronizaban (y mostraban) el módulo completo en cada
  commit aunque solo cambiara un archivo** (Unit 83). `watch` empaqueta cada
  commit con `git archive`, que le pone a **todos** los archivos la fecha
  (mtime) del commit; como rsync decidía por tamaño+fecha, veía todos los
  archivos como distintos y los re-sincronizaba, imprimiendo el árbol
  completo. Ahora rsync usa `--checksum` (compara por **contenido**, no por
  fecha), así solo se transfieren los archivos que de verdad cambiaron; y
  `parseItemize` descarta las líneas de itemize de solo-atributos (update type
  `.`, típicamente un ajuste de mtime), de modo que un commit de un solo
  archivo muestra exactamente ese archivo en el árbol.

### Changed
- **La salida de archivos de `push` ahora es un árbol de cambios legible con
  color** (Unit 83). Antes se volcaban tal cual los códigos crípticos de
  `rsync --itemize-changes` (`<f+++++++++`, `cd+++++++`). Ahora se **parsean**
  a cambios tipados (nuevo/cambiado/borrado) y se renderizan como un árbol
  agrupado por carpeta entre un frame grepeable `echo.push.module`
  (`syncing module=… dest=…` … `synced module=… new=N changed=M`): conectores
  de árbol atenuados (`├─`/`└─`/`│`), archivos de la raíz primero y luego cada
  subcarpeta, con un glyph por operación coloreado (`+` nuevo en verde, `~`
  cambiado en ámbar, `−` borrado en rojo). El árbol también aparece en
  `watch` (cada ciclo) y en `deploy --push` — `OnSync` se propaga por
  `WatchOpts`/`DeployOpts` —, salvo en `deploy --json`, donde se suprime para
  no ensuciar el stdout parseable. La forma en texto plano (sin ANSI) sigue
  alimentando `copy-last`/`--log` vía el nuevo `printStyled`. Helpers
  `parseItemize`/`BuildSyncTree`/`FileChange`/`SyncRow`.

### Fixed
- **`push` mandaba el módulo a la raíz del proyecto docker remoto en vez de a
  `addons/`** (Unit 83). El destino se calculaba **espejando la carpeta local**
  desde donde corrías `push`: si lo corrías desde dentro de `addons/`, el
  subpath local era `.` y el módulo terminaba en `<remote_path>/<módulo>` (la
  raíz, junto al `docker-compose.yml`) en lugar de `<remote_path>/addons/
  <módulo>`; el destino incluso cambiaba entre `--dry-run` y la ejecución real
  según el cwd. Ahora el destino lo decide **el layout del remoto, nunca la
  carpeta local**: un módulo ya presente en un addons dir real se actualiza en
  su sitio (un hallazgo en la raíz, `base "."`, se ignora y se re-aloja), y uno
  nuevo aterriza en el primer addons dir que exista bajo `remote_path` (las
  rutas relativas del perfil remoto, si no `addons`/`custom`). Un módulo nunca
  se escribe en la raíz del proyecto docker, y `push` da el mismo resultado sin
  importar desde qué directorio local lo corras. Nuevo helper
  `remoteAddonsCandidates`.

### Added
- **Nuevo comando `watch` — auto push+deploy al detectar commits en una rama**
  (Unit 84). `watch <branch> [--from <t>|--remote] [--interval <sec>]
  [--force]` es un loop en foreground que hace poll del ref de la rama
  (`git rev-parse refs/heads/<branch>`) y, cada vez que **avanza**, sube el
  contenido **commiteado** de los módulos afectados (Unit 83) y corre un
  `deploy --commits` headless (Unit 78). Pensado para el flujo multi-worktree:
  los refs se comparten entre worktrees, así que un commit en esa rama desde
  *cualquier* worktree dispara el ciclo — el commit es la unidad de deploy, no
  hay file-watching. Un ciclo = check de fast-forward (`merge-base
  --is-ancestor`; un rebase/amend → WARNING `branch rewritten` y re-baseline
  sin deployar) → commits del rango `old..new` resueltos a módulos
  (`resolveCommitModule`, los no resolubles se saltan con WARNING) → `git
  archive <sha>` a un dir temporal (se sube el código commiteado, no el working
  tree del worktree del watcher) → `pushModuleSet` → deploy headless (el
  historial de deploy marca los SHAs, así un watcher reiniciado no
  re-despliega). Un push/deploy fallido loguea ERROR, avanza el baseline y
  sigue (los commits perdidos se recuperan en el siguiente `deploy --auto`);
  solo errores de setup irrecuperables (target no resoluble, rama borrada)
  detienen el watcher. Un target `prod` se niega a arrancar sin `--force`.
  `Ctrl+C` cierra limpio con un frame `watch stopped cycles=N`. Interval
  default 10s, mínimo 2s. No se ofrece en `sequence` (un paso que no termina
  colgaría la secuencia).
- **Nuevo comando `push` — sube los módulos locales al addons dir remoto**
  (Unit 83). `push [<mod>...] [--from <t>|--remote] [--dirty] [--dry-run]
  [--delete] [--force]` hace `rsync` de los módulos seleccionados al host
  remoto sobre SSH, reusando la resolución de target de `deploy`/`link` — así
  Echo deja de depender del CLI externo para copiar el código al servidor y
  cierra el ciclo local → servidor → deploy. El destino se resuelve probando
  primero dónde ya vive el módulo en el filesystem del host remoto (el probe
  de la Unit 79) y, si es nuevo, espejando el subpath local del checkout (o la
  primera ruta relativa de addons del perfil). Excluye `__pycache__`/`*.pyc`/
  `.git`; `--delete` (opt-in) borra en remoto lo que ya no existe local;
  `--dry-run` usa `rsync -n` y muestra el itemize sin transferir (y salta el
  gate de prod). Selección: positionals (validados contra el repo local →
  error de uso antes de tocar SSH), multi-select picker (coloreado por el
  stage del perfil remoto), o `--dirty` (los módulos con cambios sin
  commitear). Un remoto conf-mode (addons solo dentro de la imagen) falla
  cerrado con hint. Gate de prod vía `confirmRemoteProd` (`--force` lo salta,
  no-TTY falla cerrado). El core `pushModuleSet` (con parámetro `srcRoot`) es
  reusable. Se integra en `sequence` remoto y en **`deploy --push`**, que
  sincroniza los módulos resueltos justo antes del `stop → up → -u` (un fallo
  de push aborta el deploy antes de reiniciar nada; `--dry-run` compone).
- **Nuevo comando `compare` — diff de un archivo local vs su copia en Docker**
  (Unit 80). `compare [<mod>] [--from <t>|--remote] [--copy]` elige un archivo
  de módulo del checkout local (la misma cadena de pickers que `view`) y lo
  compara contra la copia que corre **dentro del contenedor**: el contenedor
  Odoo local por defecto, o el de un target remoto con `--from`/`--remote`. El
  diff unificado se calcula **en proceso** con `go-difflib` (sin depender de
  un `diff` local ni remoto): la copia del contenedor es el lado viejo (`---`)
  y el archivo local el nuevo (`+++`), así los `+` se leen como "cambios
  locales aún no desplegados" (labels `<target>/<mod>/<file>` vs
  `local/<mod>/<file>`). Se renderiza con `bat --language=diff` (paginado) y
  cae a impresión interna plana si no hay bat; `--copy` copia el diff crudo.
  Contenidos idénticos (p. ej. en modo mount, donde ambos lados son el mismo
  archivo bind-mounted) imprimen una sola línea `identical`, sin pager. Un
  archivo presente en local pero ausente en el contenedor sale como diff
  todo-`+` tras un WARNING. Lectura pura en ambos lados: sin confirmación de
  prod. Un módulo sin copia local es error (no compara contenedor-contra-
  contenedor). El lado del contenedor local se resuelve leyendo el
  `addons_path` real del `odoo.conf` del contenedor cuando el modo host no
  expone rutas útiles.
- **`view` puede abrir el archivo desplegado en un host remoto** (Unit 79).
  `view [<mod>] --from <target>` / `--remote` navega y muestra un archivo de
  módulo desde el contenedor Odoo del servidor sobre SSH, reusando el
  transporte de `deploy`/`test`/`logs` (`resolveRemoteShell` +
  `runSSH`/`remoteContainerCmd`). `remoteModuleBase` prueba primero el
  filesystem del host remoto (`<remotePath>/<addons>/<mod>`, el layout que
  asume `deploy`) y cae al contenedor (`compose exec test -f`) para el modo
  conf; `find`/`cat` siguen el transporte que ganó. Los pickers (módulo y
  archivo) siguen siendo locales, sourced del checkout local vía
  `resolveModules`; `--copy` y `--last` funcionan contra el origen remoto (el
  log añade `from=<target>`). No hay confirmación de prod: `view` es
  estrictamente de lectura. Los flags `--from`/`--remote` se strippean para
  que el token de valor nunca se lea como nombre de módulo. Sin flag remoto,
  `view` se comporta exactamente igual que antes.

## [0.20.0] — 2026-07-05

### Added
- **`deploy` headless para scripts / CI** (Unit 78). Hasta ahora `deploy`
  resolvía los módulos con un multi-select picker de commits, así que sin TTY
  fallaba cerrado y no era invocable desde un script. Ahora dos vías
  explícitas saltan el picker reusando el motor de la Unit 61 sin tocarlo:
  **`deploy --modules m1,m2`** (lista explícita, validada contra el repo
  local — un módulo sin `__manifest__.py` es error de uso, exit `2`, antes de
  tocar el remoto) y **`deploy --auto`** (auto-selecciona los commits
  pendientes —ahead of upstream, menos los ya desplegados— más todos los
  módulos dirty; si no hay nada pendiente imprime `nothing to deploy` y sale
  `0`). Sin TTY y sin `--auto`/`--modules`, `deploy` falla cerrado con un hint
  específico (`ErrNonInteractive` → exit `2`). Con TTY y sin flags, el picker
  sigue igual (cero regresión). **`deploy --json`** emite un resumen
  parseable (`target`, `db`, `modules[]{name,action,ok}`, `skipped`,
  `errors`, `warnings`, `planned`) a `stdout` con los logs a `stderr` (mismo
  patrón que `modstate --json`); `--dry-run --json` añade `planned:true` sin
  ejecutar nada remoto. Nuevo sentinel `ErrUsage` para mapear errores de uso a
  exit `2`. `--auto` y `--modules` son mutuamente excluyentes.
- **El builder de `i18n-pull` en `sequence` / `--build` elige varios módulos
  a la vez** (Unit 77). `runI18nPullBuild` pasó de un picker de un módulo a
  **multi-selección** (`runFuzzyPickerCore`, coloreado por el stage del
  perfil remoto) y compone `i18n-pull <mod...> --lang=<lang> [--from=<name>]`
  — el idioma va como flag explícito `--lang=` (Unit 76), así todos los
  positionales son módulos sin ambigüedad y la línea vuelve a parsearse igual.
  Con esto, una secuencia remota `deploy → i18n-pull` jala el `.po` de **todos**
  los módulos desplegados en un solo paso, elegidos por adelantado en la
  revisión (replicable con `sequence --last`). `--all`/`--installed` siguen sin
  ofrecerse en el builder (para el flujo masivo usa el comando directo).
- **`i18n-pull` puede traer varios módulos en una sola corrida** (Unit 76).
  Antes solo aceptaba un módulo o el todo-o-nada `--all`; ahora
  `i18n-pull sale account es_MX` trae el `es_MX.po` de ambos de un tiro. La
  ambigüedad módulo-vs-idioma se resuelve así: si el **último** positional
  tiene forma de locale (`es`, `es_MX`, `pt_BR`, `sr@latin`; regex
  `isLocale`) y hay dos o más positionales, ese es el idioma y el resto son
  módulos; si no, todos son módulos y el idioma cae al default `es_MX`. Un
  solo positional sigue siendo un módulo (compatibilidad con Unit 50). El
  nuevo flag **`--lang <code>`** fija el idioma de forma explícita y hace que
  **todos** los positionales sean módulos (escape hatch para módulos con
  nombre parecido a un locale). El picker de `i18n-pull` sin argumentos pasa
  a ser **multi-selección** (mismo `runFuzzyPickerCore` de
  `install`/`update`/`test`, coloreado por el stage remoto). Una corrida de
  varios módulos es un **batch**: un módulo que falla al exportar se salta con
  `WARNING` y la corrida continúa, cerrando con el resumen `pull complete
  pulled=N skipped=M` — igual que ya hacía `--all`. Un solo módulo sigue
  fallando de inmediato. `--all` e `--installed` sin cambios; el build mode
  de `i18n-pull` sigue siendo de un módulo.
- **`test <mod...> --from <target>` / `--remote` corre la suite de tests de
  Odoo en una instancia remota** (Unit 75). Hasta ahora `test` solo tocaba
  el contenedor local; ahora acepta los mismos switches remotos que
  `shell-run` / `deploy` / `logs` / `restart`: `--from <t>` nombra un target
  global de `connect`, `--remote` usa el binding `[connect]` del directorio.
  Reusa exactamente el transporte de `deploy`/`shell-run`
  (`resolveRemoteShell` → `remoteContainerCmd` → `runSSHStream`): la conexión
  (DB, host, credenciales) sale del **perfil remoto**, no de la config local,
  y el argv de `odoo.Test` es idéntico al local (mismo `--test-tags`,
  `--stop-after-init`, `--no-http --http-port=8189`, `--log-level=test`), así
  que los logs se colorean igual que un test local. Los módulos se resuelven
  **antes** de ramificar, así que el picker se comparte; `--tags` y
  `--update` componen con el modo remoto. Como una corrida remota comparte el
  Postgres del target, el modo remoto **gatea en prod** vía
  `confirmRemoteProd` (confirmación roja; `--force` la salta; sin TTY falla
  cerrado), a diferencia del `test` local que nunca gatea. Sin flag remoto,
  `test` se comporta **exactamente igual que antes** (contenedor local). Los
  tokens `--from <val>` / `--remote` se despojan antes de leer positionales,
  así que el valor tras `--from` nunca se confunde con un módulo.

### Fixed
- **El builder de `deploy` (dentro de `sequence --remote` / `--from`) ahora
  atenúa los commits ya desplegados.** Al armar una secuencia, el picker de
  commits de `deploy` mostraba **todos** los commits como pendientes —
  ignorando el historial de "desplegado" que el `deploy` directo sí respeta.
  La causa: `runDeployBuild` pasaba `deployedSet = nil` al picker,
  confundiendo dos cosas distintas — poder **persistir** marcas manuales
  (correcto deshabilitarlo en build: el commit-point de la secuencia es su
  review, no este picker) con solo **mostrar atenuados** los ya enviados
  (que únicamente lee el historial). En una secuencia el target se conoce de
  antemano (`--from` o el binding `[connect]`), así que ahora se resuelve en
  modo solo-lectura y se carga `deployedSet` para el muting; si no hay target
  resoluble, degrada a sin-muting como antes. `allowMark` sigue en `false`.

## [0.19.0] — 2026-06-29

### Added
- **Marcado manual de commits como "desplegado" en el picker de `deploy`**
  (Unit 74). El historial de deploy se indexa por **SHA exacto**, así que
  una rama nueva que nunca corrió en Echo, un commit rebaseado/enmendado, o
  el primer deploy desde otra máquina aparecían todos como "por desplegar"
  aunque el código ya estuviera en el server. Ahora, dentro del selector,
  **`ctrl+d`** togglea la marca de "desplegado" del commit bajo el cursor
  (lo mutea/desmutea en vivo) y **`ctrl+a`** marca **todas** las filas
  visibles de golpe (o las desmarca si ya estaban todas marcadas),
  respetando el filtro activo — ideal para vaciar de un tiro la lista de
  pendientes de una rama que ya está arriba. Solo aplica a commits (los
  módulos dirty no tienen SHA). La edición se persiste al historial del
  target **al confirmar con `enter`** (antes del prod-gate, así sobrevive
  aunque luego canceles el deploy); `esc` la descarta. El modo build queda
  excluido (no resuelve target). Nuevos `config.UpdateDeployedMarks` /
  `config.UnmarkDeployed`.

## [0.18.0] — 2026-06-26

### Changed
- **La lista de comandos de `sequence` se podó** (Unit 73). Se quitaron de la
  selección los inspectores de sesión que no hacen trabajo en un batch
  (`copy-last`, `report`, `db-list`) y los de meta/config (`alias`, `link`);
  quedan solo comandos que ejecutan acciones (más `ps` como chequeo rápido
  de estado).
- **`sequence` integra `deploy` e `i18n-pull` correctamente** (Unit 73).
  Antes ambos abrían su picker interactivo a mitad de la ejecución
  (rompiendo el "revisar antes de aplicar" y el replay con `--last`). Ahora
  son **must-build**: su selección (commits/dirty para `deploy`; target +
  módulo para `i18n-pull`) se captura **en la fase de build**, se muestra en
  la revisión y se guarda para `--last`; la ejecución es no-interactiva. El
  builder de `i18n-pull` además reusa el target de la secuencia (`--from`),
  sin volver a preguntar. `bakeRemote` es consciente del comando:
  `deploy`/`i18n-pull` no aceptan `--remote` (usan `--from` o el `[connect]`
  del directorio), así que en modo `--remote` no se les añade un flag
  inválido.

### Added
- **`up` y `stop` pueden actuar sobre un host remoto** (Unit 73). Igual que
  `restart`/`logs`, aceptan `--from <target>` (un connect target nombrado) o
  `--remote` (el binding de `link` del directorio) y corren el verbo de
  compose sobre SSH (`up -d` / `stop`) reutilizando el transporte existente,
  con la salida streameada estilo Odoo. `stop --remote` pide confirmación
  roja cuando el stage remoto es `prod` (`--force` la salta); `up` no
  confirma (arrancar no es destructivo). Ambos son **projectless** en modo
  remoto (corren desde un repo sin `docker-compose.yml`) y entran a la lista
  de comandos remote-capables de `sequence`, así una secuencia remota puede
  reiniciar un stack (`stop` → `up` → …).
- **`deploy` acepta selección no-interactiva `--commits`/`--modules`** (Unit
  73). `deploy --commits=<sha,sha> --modules=<addon,addon>` salta el picker
  de commits/dirty y despliega justo esos objetivos; los SHAs se resuelven
  por hash corto o largo (recuperando subject para el mapeo a módulo) y los
  módulos nombrados que ya no estén dirty (p. ej. un replay tras commitear)
  se incluyen igual por nombre. Sin estas flags, `deploy` abre su picker
  como siempre. Esto habilita un **builder de `deploy`** (`deploy --build`):
  el picker de commits/dirty corre en build-time y hornea esas flags, así la
  selección se puede revisar, copiar y reejecutar.
- **Nuevo comando `sequence`: arma y corre varios comandos en orden** (Unit
  73). Un builder de recetas interactivo: un picker tri-estado lista los
  comandos y un solo `Tab` cicla cada uno `off → run → build`, donde el
  número `⟦n⟧` es el orden de ejecución (= orden de selección) y el glyph
  `` (Nerd Font `cod-tools`, misma familia que el `` de `modules`) marca
  los que pasan por el builder de flags (reusa `--build` en modo
  return-only). Tras una pantalla de revisión **log-framed** (barra `│`
  coloreada por entorno + línea de contexto `N steps · stage/versión ·
  local`/`→ target`, y los pasos numerados en orden de ejecución con el
  comando en accent, los flags resaltados y los valores atenuados como en el
  REPL) con acciones Run / Save `.echo` / Copy / Cancel, los pasos corren
  con el motor de recetas y el estilo de logs
  Odoo de Echo (`echo.sequence` / `echo.sequence.step`), fail-fast por
  defecto (`--continue-on-error` lo desactiva). Un paso `logs` en follow se
  fuerza al final y la línea de cierre `sequence complete` se emite **antes**
  de entrar al follow, para que el `^c` no parezca un fallo. Funciona local y
  remoto: `sequence --remote` / `sequence --from <target>` corre toda la
  secuencia contra un target (la lista se filtra a comandos remote-capables
  —`restart`, `logs`, `i18n-pull`, `deploy`— y el flag se hornea en cada
  paso). En modo remoto `sequence` es **projectless** (como
  `deploy`/`restart`/`logs`): corre desde un directorio sin
  `docker-compose.yml`; y si un paso marcado para el builder no puede listar
  sus candidatos locales en ese contexto, se degrada a correr el comando
  tal cual en vez de abortar la secuencia. `sequence --last` repite la
  última secuencia ejecutada del proyecto (headless, sin picker). `Save as
  recipe` deja un `.echo` que `echo run` vuelve a correr. Spec
  `73-sequence-builder.md`.

## [0.17.0] — 2026-06-24

### Added
- **`restart` y `logs` ahora pueden actuar sobre un host remoto** (Unit 72).
  Igual que `shell`/`deploy`, ambos aceptan `--from <target>` (un connect
  target nombrado) o `--remote` (el binding de `link` del directorio) y
  corren el verbo de compose sobre SSH reutilizando el transporte ya
  existente (`resolveRemoteTarget` + `remoteComposeCmd` + `runSSHStream`),
  con la salida streameada y coloreada estilo Odoo como en local. `restart
  --from prod` reinicia el contenedor Odoo del perfil remoto (pasar
  servicios acota) y, cuando el stage remoto es `prod`, pide confirmación
  roja (`--force` la salta) — más estricto que el `restart` local, que no
  confirma. `logs --from prod` sigue los logs en vivo sobre SSH (follow por
  defecto); `-t/--tail`, `--no-follow` y `--copy` (que copia al portapapeles
  local) se respetan. Sin flag remoto, ambos comandos se comportan
  exactamente como hoy (local). Ambos pasan a ser `projectlessOneShot` solo
  cuando hay un flag remoto, así corren desde un repo de addons sin
  `docker-compose.yml`. Spec `72-remote-restart-logs.md`.

## [0.16.0] — 2026-06-23

### Added
- **El nombre de la DB se acorta en los logs cuando supera un límite, para
  no provocar wrap** (Unit 71). En cada línea de log estilo Odoo el nombre
  de la base va en la columna `db`; uno largo (típico de un dump de odoo.sh
  como `mycompany-main-prod_2026-06-18_23-42-53`) empujaba el resto de la
  línea y la envolvía. Ahora se trunca **solo en pantalla** con elipsis en
  medio (`mycompany-…_23-42-53`), conservando inicio y fin; los nombres
  normales (`habitta_prod`, `my_shop`) quedan intactos. El límite es la
  config global `log_db_max` (default 20). Cubre las dos rutas de render
  (`renderOdooLog` para las líneas `echo.<cmd>` y `formatOdooLine` para las
  líneas de Odoo streameadas) y el connect projectless; el portapapeles
  (`copy-last`) y el transcript de `echo run --log` conservan el nombre
  completo. Helper puro `theme.MiddleTruncate(s, max)` (rune-aware). Spec
  `71-log-db-name-truncate.md`.
- **`update --installed` ofrece todos los módulos instalados en el picker,
  no solo los del repo** (Unit 70). Sin el flag, el picker de `update` solo
  lista los addons del proyecto, así que no había forma de *descubrir* y
  actualizar módulos core como `base`/`web`/`account` desde el picker (sí
  escribiendo `update base` a mano). Con `--installed` el picker se llena
  desde `ir_module_module` (los marcados como instalados en la DB activa,
  misma consulta que `modstate`), así cualquier módulo instalado —core o de
  terceros— es seleccionable. Solo cambia la fuente del picker: el resto
  (`-u`, `--last`, start line, `--i18n`/`--level`) queda igual; el flag es
  inerte si se combina con módulos explícitos/`--all`/`--last` (que saltan
  el picker). Nuevos `installedModules`/`installedModuleNames` en
  `internal/cmd/modules.go`; `pickModulesForUpdate` gana el parámetro
  `installed`. Spec `70-update-installed-picker.md`.
- **`deploy` ahora ofrece los módulos con cambios sin commitear (dirty) en
  el mismo picker** (Unit 69). Antes solo ofrecía commits; ahora detecta
  los addons con cambios en el working tree (`git status --porcelain`,
  modificados + sin trackear) y los lista como entradas seleccionables
  arriba de los commits (`~ <module>  ·  uncommitted (N files)`). El set
  final de módulos es la **unión** de los módulos resueltos de los commits
  elegidos más los módulos dirty seleccionados (deduplicado); cada dirty
  resuelve directo (`via=dirty`) y sus paths alimentan la detección de
  i18n (`i18n/` → `--i18n-overwrite`). Como el código dirty no está
  commiteado ni en el servidor, al seleccionar dirty se emite un
  `WARNING`: deploy los actualiza en el server pero no sube el código (eso
  lo hace tu otra herramienta). Detección best-effort: árbol limpio o
  `git status` que falla → picker solo con commits, como antes. Nuevos
  `dirtyModule`, `gitDirtyModules`, `parsePorcelainPaths`,
  `dirtyModulesFromPaths` y `pickDeployItems` (reemplaza
  `pickDeployCommits`) en `internal/cmd/deploy.go`. Spec
  `69-deploy-dirty-modules.md`.
- **Comando `db-use [name]` para cambiar la base de datos activa** (Unit
  66). Cambia la `cfg.DBName` del proyecto — la que `db-list` marca con
  `●` y el destino implícito de `update`/`install`/`shell`/`psql`/
  `modstate`/`db-admin`/etc. Sin argumento abre un picker sobre la lista
  de bases (como `db-drop`); con nombre cambia directo tras verificar que
  existe (`no database named "<x>"` si no). Persiste `db_name` vía
  `config.SaveProject`, así que sobrevive reinicios; como la sesión
  comparte el mismo `*config.Config`, el prompt toma la nueva DB en el
  siguiente render. Cambiar a la DB ya activa es un no-op reportado
  (`→ <db> (already active)`). `RunDBUse` en `internal/cmd/db.go`; wiring
  en `commands.go`/`repl.go`.
- **Comando `db-admin [name]` para recuperar acceso al administrador**
  (Unit 66). Resetea el login **y** la contraseña del usuario `id = 2`
  (el admin de Odoo) a `admin` / `admin` para entrar al back office sin
  conocer las credenciales actuales. La DB destino sale de `cfg.DBName`,
  la sobreescribe un argumento posicional y, si no hay ninguna, abre el
  mismo picker que `db-drop`/`db-neutralize`. Es una operación puramente
  PostgreSQL (`UPDATE res_users SET login='admin', password='admin' WHERE
  id=2 RETURNING id` vía la maquinaria `psql` existente, nuevo
  `docker.ResetUserCredentials`): la contraseña se guarda en **texto
  plano** a propósito — el crypt context por defecto de Odoo mantiene el
  esquema `plaintext` deprecado, así que la verifica en el siguiente login
  y la re-hashea a `pbkdf2_sha512` (funciona en Odoo 16/17/18/19). Guarda
  un confirm rojo cuando `stage=prod` (un admin/admin en producción es un
  agujero de seguridad), salteable con `--force`; la DB activa no se
  protege porque es el destino normal. Si no existe la fila `id=2` falla
  con `no user with id 2 in "<db>"` en vez de un no-op silencioso.
  Archivos: `internal/docker/postgres.go` (`ResetUserCredentials`),
  `internal/cmd/db.go` (`RunDBAdmin` + `confirmAdminReset`), wiring en
  `internal/repl/commands.go` y `internal/repl/repl.go`. Spec
  `66-db-admin-reset.md`. Verificación EN VIVO contra un contenedor
  pendiente del usuario.

### Changed
- **`db-restore` ahora deja renombrar la base antes de restaurarla** (Unit
  68). Tras elegir el backup en el picker aparece un input "Restore as"
  pre-llenado con el nombre derivado del archivo: lo editas para acortarlo
  (típico de un dump de odoo.sh con nombre kilométrico que si no ensucia
  todos los logs) o presionas Enter para aceptarlo. `--as <name>` salta el
  prompt (intención no-interactiva); en modo no-TTY cae al nombre derivado
  como antes. El nombre se valida inline (no vacío, sin espacios) y
  Esc/Ctrl+C cancela limpio sin crear la DB. Nuevos `promptRestoreName` y
  `validateDBName` en `internal/cmd/db.go`. Spec `68-db-restore-rename.md`.
- **`db-restore` ahora narra su progreso en vivo en vez de trabajar en
  silencio** (Unit 67). Antes, tras el picker de backup no se mostraba
  nada hasta el `→ <target>` final, aunque el `CREATE DATABASE` + el
  `pg_restore` de una base real tardan segundos o minutos. Ahora emite una
  línea INFO estilo Odoo por cada fase (`echo.db-restore.restore`):
  dropping existing database, creating database, restoring data
  (`file=`/`format=`), extracting archive, copying filestore,
  neutralizing; y además **streamea en vivo la salida del paso largo**:
  `docker.Restore`/`RestoreSQL` reciben un callback `onLine` y `pg_restore`
  corre con `--verbose`, así cada línea de progreso (creación de tablas,
  carga de datos) fluye como una línea `DEBUG` atenuada bajo el mismo
  logger mientras los hitos INFO marcan los límites de fase. La detección
  de fallo conserva solo las líneas de error (`error`/`fatal`) para el
  mensaje, no el volcado verbose completo. Nuevo `DBOpts.Log` (tipo
  `DBLogger`), cableado en el `runDB` del REPL igual que el logger de
  `connect`. `db-backup` queda igual (fuera de alcance). Spec
  `67-db-restore-progress.md`.

## [0.15.0] — 2026-06-19

### Added
- **Banner del header coloreado por environment, con dos estilos figlet del
  wordmark `echo` elegidos al azar** (Unit 15). Reemplaza el box `ECHO`
  hardcodeado por: **B (soundwave)** — Calvin S de doble trazo (`╔═╗`) con una
  onda `▁▂▃▅▇▅▃▂▁` debajo — y **D (shadow)** — ANSI Shadow (`█`) con degradado
  vertical y un ripple `)))` opcional. El **color principal sale del stage
  activo** (`PromptColor`: dev→verde, staging→ámbar, prod→rojo del tema en uso)
  y el degradado/onda se **derivan en código** aclarando/oscureciendo ese color
  con los nuevos helpers `theme.Lighten`/`theme.Darken` — sin hex hardcodeado,
  así funciona en los 4 temas. Al arrancar el REPL el estilo se elige al azar
  entre B y D; nueva config `banner = auto|soundwave|shadow` (default `auto`)
  para fijarlo. Un **guard de ancho** respeta la columna izquierda del header:
  D (gradiente) aparece desde ~85 cols de terminal, el ripple desde ~95, y por
  debajo cae a B — nunca desborda el borde. Para previews/demos, la env var
  `ECHO_BANNER=soundwave|shadow` fuerza el estilo y **salta el guard de ancho**
  (puede desbordar en terminal angosta; ese es el precio del opt-in explícito).
  Archivos: `internal/banner/echo.go`
  (arte + `resolveBannerStyle` + `renderEchoBanner`), `internal/banner/header.go`
  (cableado del banner + `Opts.Banner`), `internal/theme/theme.go`
  (`Lighten`/`Darken`), `internal/config` (`Banner`, default `auto`),
  `internal/repl/repl.go` (opts). Tests `echo_test.go` (selección + invariante
  de ancho) y `shade_test.go` (límites de mezcla). Verificación visual en TTY
  ancho pendiente del usuario.

### Changed
- **El bloque "What's new" del header dejó de promocionar contenido obsoleto.**
  Mostraba "First release — header + prompt" y sugería `ls` (comando que ya no
  existe en el `Registry`). Ahora destaca capacidades reales y actuales: el
  banner coloreado por stage, `deploy` (mandar commits a un servidor) y
  `connect` (abrir Odoo como cualquier usuario).

## [0.14.0] — 2026-06-19

### Added
- **GIFs de demo del README, generados con [VHS](https://github.com/charmbracelet/vhs).**
  Nueva carpeta `demo/` con un `.tape` por GIF (`hero`, `update`, `deploy`,
  `db-list`, `modinfo`) y un dispatcher de simulación en `sim/echo-sim.sh`:
  como Echo envuelve Docker/Odoo/SSH, grabar el binario real exigiría un stack
  vivo y filtraría hosts, DBs y rutas privadas, así que los GIFs reproducen el
  estilizado exacto de Echo (paleta tokyo, formato de log Odoo por segmento,
  glifos Nerd Font extraídos del código) con datos inventados. El mockup ASCII
  estático del README se reemplaza por el GIF *hero* real, y cada sección
  (módulos, base de datos, deploy) lleva su GIF embebido.
- **`deploy` recuerda qué commits ya desplegó a cada target y los atenúa en
  el picker** (Unit 65). Tras un deploy exitoso, los commits seleccionados
  que resolvieron a módulo se guardan localmente en
  `~/.config/echo/deploy-history/<projectKey>.toml`, keyeados por target
  (hash de host+path), así un commit mandado a *staging* no cuenta como
  desplegado a *prod*. En el siguiente `deploy` esos commits salen en color
  tenue (`Faint`) con la leyenda `muted = already deployed`, dejando ver de
  un vistazo lo nuevo desde el último despliegue. Best-effort como el resto
  del recall: un archivo ausente/corrupto degrada a "nada desplegado" sin
  error; `--dry-run`, un gate de prod rechazado o un paso fallido no
  registran nada. El historial se cap­ea a los 1000 SHAs más recientes por
  target.

### Fixed
- **El placeholder `type to filter…` del picker ya se muestra completo.**
  bubbles dimensiona el buffer del placeholder a `Width+1`, y con `Width=0`
  lo truncaba a una sola runa — el filtro mostraba un `t` fijo que parecía
  texto tecleado. El input del picker ahora fija `Width` para que el
  placeholder se renderice entero.

### Changed
- **El prompt `filter ›` del picker toma el color del stage** (verde dev /
  amarillo staging / rojo prod, leído del `.env`/perfil), igual que la
  barra izquierda, así el entorno se lee de un vistazo también en la línea
  de filtro.
- **Cada `[TAG]` de los commits en el picker de `deploy` se colorea por
  tipo** (ADD verde, FIX rojo, IMP cian, REF/MERGE acento, DOC ámbar,
  REL acento, …); un tag no reconocido recibe un pastel estable elegido por
  hash, de modo que la taxonomía propia del proyecto también se distingue
  por color sin estar cableada.
- **La línea de inicio de `update`/`install`/`uninstall`/`test` ahora
  reporta las banderas usadas.** `echo.<cmd>.module.<mod>.start` gana un
  campo `flags=` con los flags que el usuario pasó (p. ej. `--i18n`,
  `--level=debug`), junto al `modules=` ya existente, así un
  `update <mod> --i18n` queda registrado como tal en el log. Sin flags el
  campo se omite. En `deploy`, la línea del run remoto de Odoo
  (`echo.deploy.odoo: running module install/update`) ahora nombra los
  módulos `update=`/`install=` y, cuando aplica, `flags=--i18n-overwrite`,
  reflejando en el punto de ejecución lo que ya muestra la línea de plan.
- **La línea de plan de `deploy` ahora cuelga del logger
  `echo.deploy.plan`** (antes `echo.deploy`), de modo que la rotación de
  color por logger la pinta en un tono distinto al resto de las líneas del
  deploy — es la que el operador revisa antes del gate de prod.

### Added
- **`deploy` detecta cambios de traducción y añade `--i18n-overwrite`
  automáticamente** (Unit 64): al resolver cada commit seleccionado, `deploy`
  escanea sus archivos tocados (`git diff-tree`, ahora también para los
  commits resueltos por título) y marca el módulo cuando alguno cae bajo
  `<módulo>/i18n/`. Si un módulo marcado queda en el set de actualización
  (`-u`), el único run de Odoo remoto lleva `--i18n-overwrite`, de modo que
  los `.po` desplegados sobrescriben las traducciones en la BD. Los módulos
  del set de instalación (`-i`) no disparan el flag (una instalación fresca
  ya carga sus traducciones) y se reportan con una línea informativa. Dos
  overrides per-invocación, mutuamente excluyentes: `--i18n` fuerza el flag
  aunque no se detecte nada y `--no-i18n` lo suprime aun detectándose; la
  línea de plan muestra `i18n=on|off|forced|suppressed`, visible también en
  `--dry-run`.

## [0.13.0] — 2026-06-12

### Added
- Nuevo comando **`link [<target>] [--show] [--rm]`** (Unit 60): enlaza el
  directorio actual (típicamente un repo de addons sin `docker-compose.yml`,
  modo *projectless*) a un connect target global, escribiendo su
  `ssh_host`/`remote_path` en la sección `[connect]` per-project que
  `connect`/`i18n-pull` ya consumen. Sin argumento abre un picker de targets
  (uno solo se usa automático); el enlace se guarda **antes** de probar el
  remoto, así que un host inalcanzable es `WARNING`, no fallo. `--show`
  muestra el enlace, lee el perfil Echo remoto (línea de system-status) y
  renderiza los contenedores remotos con la **misma tabla estilizada del
  `ps` local** (lectura `--format json` vía SSH + `docker.ParsePS`, cierre
  `echo.link.ps: containers listed`; fallback al stream crudo si el JSON
  falla); `--rm` quita el enlace (idempotente).
- **Ejecución remota con streaming** (`runSSHStream`): variante de `runSSH`
  que reenvía stdout/stderr remotos línea a línea en tiempo real hacia el
  mismo pipeline de render que los subprocesos locales (`emitStreamLine`),
  cumpliendo la invariante de streaming también sobre SSH. Base del comando
  `deploy` (Unit 61).
- Nuevo comando **`deploy [--from <target>] [--limit N] [--dry-run]
  [--force]`** (Unit 61): despliega commits locales seleccionados a una
  instancia Odoo remota vía SSH. Abre un picker multiselect sobre los últimos
  N commits (default 20) del repo actual; cada commit se mapea a su módulo
  por el esquema de título `[Tag] module: title` (validando que exista
  `__manifest__.py`) con fallback por los archivos tocados en el commit
  (`git diff-tree`) cuando toca exactamente un módulo — los commits
  irresolubles se excluyen con `WARNING` y se reportan en el resumen. El
  split install/update sale de consultar `ir_module_module` en la BD remota
  (instalado / `to upgrade` → `-u`; lo demás → `-i`). Con el plan visible (y
  confirmación si el stage remoto es `prod`), ejecuta en el remoto
  `compose stop` → `compose up -d` → un solo run de Odoo combinando
  `-i`/`-u` (`--stop-after-init`, credenciales `--db_*` del `.env` remoto),
  todo streameado en vivo con el estilo de logs Odoo. `--dry-run` hace las
  lecturas y muestra el plan sin ejecutar nada. Asume que el código ya está
  pulleado en el servidor. Nuevo builder `odoo.InstallUpdate`.
- **Odoo shell remoto** (Unit 62): `shell --from <target>` (o `--remote`
  para usar el enlace del directorio) abre el shell de Odoo de la instancia
  remota vía `ssh -tt`, pasando por la misma maquinaria PTY del shell local
  (captura + colorizado de logs de arranque; `docker.RunInteractive`
  extraído de `ExecInteractive`). `shell-run <file> --from <target>` /
  `--remote` corre un `.py` **local** a través del shell de Odoo remoto
  (script por stdin de ssh vía `runSSHStream`), conservando el auto-copy de
  solo los `print` del script. Resolución de target compartida con
  `deploy`/`i18n-pull` (`resolveRemoteTarget`); la confirmación de prod usa
  el stage del perfil remoto. Ambos comandos son projectless one-shot solo
  en modo remoto.
- **`shell` acepta stdin por pipe** (Unit 63): `cat fix.py | echo shell`
  (y `… | echo shell --from prod --force`) detecta el stdin no-TTY y corre
  el contenido por el shell de Odoo en modo headless — local o remoto —
  con la salida streameada estilo Odoo, sin auto-copy (el consumidor del
  pipe es dueño de la salida; `copy-last` sigue disponible). En el REPL
  interactivo nada cambia. Además `shell-run -` lee el script de stdin
  explícitamente (como `echo run -`), conservando su auto-copy; `-` con
  stdin TTY falla rápido en vez de bloquearse. Nuevo
  `docker.ExecWithStdinReader` (la variante con archivo delega en él) y
  helper `cmd.StdinPiped`. El guard de prod se mantiene: un pipe contra
  prod exige `--force` (sin TTY no hay confirmación).

## [0.12.0] — 2026-06-12

### Added
- Nuevo comando **`shell-run [<archivo>]`** (Unit 59): corre un script `.py`
  local a través del Odoo shell pasándolo por stdin —equivalente a
  `odoo-bin shell -d <db> --no-http < investigar.py`— y **auto-copia** la
  salida al portapapeles al terminar (`copied N lines`; `--no-copy` lo evita).
  Sin argumento abre un picker de `.py`; con argumento corre directo. La
  salida se stremea coloreada estilo Odoo (igual que `update`). El auto-copiado
  toma **solo la salida del script** (las líneas de `print`), descartando el
  boot/inicialización del shell de Odoo —se filtran las líneas con formato de
  log Odoo—; el transcript completo (boot incluido) sigue disponible con
  `copy-last`. Corre sin TTY (`exec -T`) para que el pipe de stdin funcione.
  **De dónde salen los `.py`:** una carpeta `scripts/` en la raíz del proyecto
  se detecta sola (sin config); la config de proyecto `scripts_dir` permite una
  ruta distinta (relativa al proyecto o absoluta); si no hay ninguna, la raíz
  del proyecto (top-level, sin recursión, para no escanear los addons). En DB
  de stage `prod` pide confirmación (`--force` la salta). Builder `odoo.Shell`
  compartido con el `shell` interactivo; piping a stdin vía
  `docker.ExecWithStdin`.

### Fixed
- **`i18n-export`/`i18n-update` en Odoo 19** dejaban el `odoo.conf` efímero
  (con las credenciales, requerido por `odoo i18n … -c`) ilegible para Odoo:
  se copiaba con `docker cp`, que lo deja `root:root 0600`, y el proceso Odoo
  (usuario no-root) no podía leerlo → `error: the config file '…' … is not
  readable` y el export fallaba (exit 2); como además `/tmp` es sticky, el
  `rm -f` de limpieza daba `exit status 1`, y al no generarse archivo nuevo
  quedaba el `.po` viejo del repo (parecía que "copiaba" el existente). Ahora
  el conf se escribe **dentro** del contenedor por stdin (`sh -c 'cat > …'` vía
  `docker.ExecWithStdin`), quedando propiedad del usuario Odoo —legible y
  removible—, igual que ya hacía el `i18n-pull` remoto. El `.po` de
  `i18n-update` no estaba afectado (viene del repo, 0644). Solo afecta Odoo 19+
  (17/18 usan flags `--db_*`, sin conf).
- **`i18n-export`/`i18n-pull` en Odoo 19 exportaban un `.po` incompleto**
  (módulos del proyecto `not installable, skipped` / `Some modules are not
  loaded`). Causa: el `odoo i18n export -c <conf>` **reemplaza** al conf real
  del contenedor en vez de fusionarlo, y el conf que Echo generaba solo traía
  la conexión de BD, **sin `addons_path`** → Odoo no encontraba los módulos del
  proyecto y el export omitía sus términos. Ahora el conf generado incluye el
  `addons_path` real (se lee crudo del `odoo.conf` del contenedor con
  `extractAddonsPath`, sin filtrar enterprise porque un módulo puede depender de
  él) vía `odoo.RenderConf(conn, addonsPath)`; el pull remoto pasa el
  `addons_path` del perfil remoto. En 17/18 no aplica (el legacy usa el conf
  real del contenedor). Nota: con los módulos ahora cargados, desaparece el
  ERROR de carga que marcaba el comando como `failed`.
- **`i18n-pull` en Odoo 19 seguía exportando un `.po` distinto al de
  `i18n-export`** (parecía traer una versión vieja/incompleta). Causa: el
  `addons_path` del conf efímero salía de `prof.AddonsPaths`, el **snapshot
  persistido** en el perfil Echo del servidor (`projects/<hash>.toml`), que
  (1) no se refresca en el pull —si el `odoo.conf` remoto cambió, se usaban
  paths viejos—, (2) está **filtrado** por `parseAddonsPath` (descarta dirs
  `enterprise*`), y (3) en `addons_mode = "host"` guarda subpaths relativos
  al host, inválidos dentro del contenedor. Como `-c` reemplaza al conf real,
  cualquiera de esos huecos hacía que Odoo cargara de menos y el export
  omitiera términos. Ahora el pull lee el `addons_path` **en vivo y crudo**
  del `odoo.conf` real del contenedor remoto (`remoteAddonsPath`, vía SSH +
  `extractAddonsPath`) —la misma fuente que usa el `i18n-export` local—, con
  el snapshot del perfil solo como fallback si el `cat` falla. Una sola
  lectura por run (no por módulo). En 17/18 no aplica (sin `-c`).
- **`logs`** ahora se pinta **idéntico a `update`** (Unit 58). Dos causas que
  Unit 57 no resolvió:
  1. `docker compose logs -f` antepone un gutter `servicio  | ` a cada línea
     que rompía el parser de Odoo → se añade `--no-log-prefix` a
     `Logs`/`LogsFollow`.
  2. A diferencia de `update`/`install` (`exec -T`, logs planos), `docker
     compose logs` reproduce el ANSI que Odoo guardó cuando corrió con TTY;
     esos códigos SGR rompían `formatOdooLine` y la línea caía a impresión
     cruda con los colores nativos de Odoo (logger sin pastel, etc.). Ahora
     `emitStreamLine` limpia el ANSI con `stripANSISeq` antes de parsear —el
     mismo tratamiento que ya hacía `shell`— así `logs` y `update` pasan por
     el mismo formateador por segmentos (ts dim, chip de nivel, db en acento,
     logger en pastel, mensaje normal). Para `update` es no-op (no trae ANSI).

### Changed
- **`help` ahora es un visor paginado** en el REPL interactivo: cada sección
  (Project, Modules, i18n, Database, Shell, Docker, Session, Scripting, Build)
  es una página; **←/→** (también `h`/`l` y tab) se mueven entre secciones con
  wrap, **↑/↓** hacen scroll dentro de una sección alta, `esc` cierra y
  `Ctrl+X` sale de Echo (igual que en los pickers). Corre en pantalla alterna
  (no contamina el scrollback) con el mismo estilo "log-framed" del picker:
  barra `│` tintada por stage, header con tabs y contador `(n/N)`, footer de
  atajos en faint. La segunda sección "Shell" (copy-last / report / clear /
  help) se renombró a **"Session"** para que los tabs no se repitan.
- **`echo help` desde la terminal también abre el visor paginado**: cuando se
  corre como one-shot (`echo help`) y tanto stdin como stdout son una terminal
  real, usa el mismo pager que el REPL interactivo. Dentro de una receta, o si
  la salida está redirigida/entubada (pipes, `>`, CI), `help` sigue imprimiendo
  el listado plano de siempre.
- **`modules`** ahora prefija cada módulo con el glyph nerd-font ``
  (`cod-package`) en color de acento y colorea el nombre, conservando el wrap
  al ancho de terminal y la línea de cierre `echo.modules: modules listed
  count=N` (Unit 58).

## [0.11.0] — 2026-06-11

### Changed
- Estilo consistente para los últimos comandos que salían "crudos" (Unit 57):
  - **`db-list`** ahora es una tabla estilizada `name · size · created` (mismo
    patrón que `modstate`/`ps`): header en acento, la DB activa con `●` verde
    y nombre en verde, size/fecha atenuados, cierre `echo.db-list: databases
    listed count=N`.
  - **`modules`** lista los nombres envueltos al ancho de la terminal (layout
    del picker) y cierra con `echo.modules: modules listed count=N` en vez del
    `(N modules)` plano; `modules --config` no cambia.
  - **`logs`** en modo follow ahora colorea el stream con el mismo parser
    Odoo que `up`/`down`/`update` (antes pasaba el output crudo de docker);
    Ctrl+C lo corta limpio. El costo del parse por línea es insignificante
    aun en vivo. `--no-follow`/`--copy` ya coloreaban.
- `ps` ahora renderiza una **tabla estilizada** (Unit 56) en vez del
  passthrough crudo de `docker compose ps`: lee los contenedores estructurado
  vía `--format json` y los muestra como `service · image · status · ports`
  con header en acento y columnas alineadas (mismo patrón que `modstate`). El
  `status` se colorea por salud/estado (healthy=verde, unhealthy=rojo,
  starting=amarillo; running=verde, exited/dead=rojo, paused/created=dim) y
  los puertos publicados se compactan a `pub→target`. Cierra con una línea
  `echo.ps: containers listed count=N`. Si `--format json` falla por
  cualquier motivo, cae al streaming crudo anterior (sin regresión).
- Los pickers interactivos (target de `connect`/`i18n-pull`, módulos de
  `install`/`update`/`uninstall`/`test`/`build`, usuario y sesiones recientes
  de `connect`) se reestilizaron a un formato **log-framed** (Unit 55) para
  que se integren al stream de logs Odoo en vez de verse como un widget
  aparte: se quitó el título en negrita-acento y la línea divisoria `────`;
  el bloque cuelga de una **barra vertical `│` izquierda coloreada por el
  stage** del target (`dev`=verde, `staging`=amarillo, `prod`=rojo) —el env
  se ve de un vistazo, y en `prod` es una barra roja prominente—; el filtro
  va en su propia línea (`filter ›`) con el placeholder `type to filter…`
  ahora legible; las filas quedan indentadas con el nombre resaltado y la
  columna secundaria (host:path / nombre) atenuada; el cursor `❯` y la
  selección también llevan el color del stage. El color de stage se aplica en
  todos los pickers cuyo stage se conoce (los locales vía `cfg.Stage`, los de
  `i18n-pull`/usuario vía el perfil remoto); el picker de **target** mantiene
  el acento por defecto porque el stage de cada candidato vive en su perfil
  remoto y no se conoce hasta conectarse.

### Added
- Línea de **system-status** al iniciar `connect`, `run` e `i18n-pull`
  (Unit 54): una sola línea Odoo-style `echo.system.status: system cli=…
  odoo=… env=… project=… db=…` emitida una vez al arranque (no por
  sub-comando), donde `env` es el stage configurado del target
  (`dev`/`staging`/`prod`),
  pensada sobre todo para corridas one-shot sin el banner del REPL. `cli`
  es la versión de Echo con metadata de build (`+<sha>`, `.dirty` si el
  árbol está sucio); `odoo` es la versión del target (local `cfg.OdooVersion`
  o remota `RemoteProfile.OdooVersion`), que muestra `unknown` cuando falta
  —diagnóstico inmediato de un target mal configurado—; `project` es el
  alias `--from`/`compose_project` o el basename del path; `db` el nombre de
  la base. Nunca incluye credenciales. Para exponer la versión del CLI a la
  capa `internal/cmd` (que no puede importar `internal/repl`) se agregó
  `cmd.EchoVersion`, seteada una vez desde `main.go`. La línea se emite lo
  más arriba posible: primera en `run`, tras resolver el target en `connect`,
  y en `i18n-pull` apenas se lee el perfil remoto (reemplaza a la antigua
  línea `connected`, ya que la versión de Odoo es remota y no se conoce antes
  de conectarse). `i18n-pull` además dejó de emitir la línea `start` genérica
  (sin información) y ahora abre con `selecting remote target` / `target
  resolved`.
- `Ctrl+X` ahora cierra el REPL de Echo, además de `exit`/`quit`/`Ctrl+D`.
  A diferencia de `Ctrl+D` (que solo hace EOF con la línea vacía), `Ctrl+X`
  sale de forma explícita aunque haya texto en la línea (estilo nano). La
  ayuda y el banner de inicio documentan el nuevo atajo. También funciona
  **dentro de los pickers** (selección de target en `connect`/`i18n-pull`,
  de módulo, de usuario, etc.): `Ctrl+X` cierra Echo entero —vía el nuevo
  `cmd.ErrQuit`— en vez de solo cancelar el picker (eso sigue siendo
  `Esc`/`Ctrl+C`); el texto de ayuda del picker lo refleja.

### Fixed
- `i18n-export`, `i18n-update` e `i18n-pull` ahora funcionan contra Odoo 19
  (Unit 53). Odoo 19 eliminó la forma por flags de servidor
  (`--modules=`, `--i18n-export=`, `--i18n-import=`) y la reemplazó por el
  subcomando `odoo i18n export|import`, cuyo único input de conexión es
  `-c`/`-d` (las flags `--db_*` ya no se aceptan en ese parser). Echo emite
  ahora la forma nueva en instancias 19+ y conserva la forma legacy en
  17/18, eligiendo según la versión de Odoo configurada del target
  (`cfg.odoo_version` en local; `RemoteProfile.OdooVersion` propagado al
  `connectTarget` en remoto). El error `no such option: --modules` queda
  resuelto.

### Added
- Builders `odoo.ExportI18n`/`odoo.UpdateI18n` ahora son version-aware y
  reciben la versión + un `confPath`; helpers nuevos `odoo.Major` (parsea el
  major de la versión) y `odoo.RenderConf` (genera un `odoo.conf` mínimo con
  la conexión de DB). En 19+ las credenciales viajan en un `odoo.conf`
  efímero escrito dentro del contenedor (`/tmp/echo-i18n-*.conf`,
  regenerado por invocación y borrado junto al `.po`), porque el subcomando
  `i18n` no acepta `--db_*`. `RemoteProfile` ahora lee `odoo_version` del
  perfil remoto (Unit 53).

## [0.10.0] — 2026-06-10

### Added
- New `modstate [--all] [--json]` command (Unit 47): dump every module's
  `name`/`state`/`version` from `ir_module_module` for the active project's
  database. Installed-only by default; `--all` widens to every state
  (`to upgrade`, `uninstalled`, …). Human mode prints an aligned
  `name | state | version` table (state colored by status, NULL version as
  `-`); `--json` emits a clean JSON array to **stdout only** — no ANSI, no
  log lines — one object per module (`{"name":…,"state":…,"version":…}`,
  a NULL `latest_version` serialized as `null`), so the output pipes
  straight into `jq`. In `--json` mode any diagnostic goes to stderr and
  stdout stays empty on error. Headless (no TTY, no picker), one-shot
  eligible and `-C`-aware like `update`/`install`. Exit codes: `0` ok,
  `1` DB/execution error, `2` usage / project-not-configured.
- `echo run --last` (Unit 52): ejecuta directamente el recipe `.echo` más
  reciente del directorio actual sin abrir el picker. No requiere TTY
  (apto para scripts), compone con `--continue-on-error` y `--log`, y el
  transcript registra qué archivo se resolvió
  (`echo.run: latest recipe → <nombre>`). Mutuamente excluyente con
  `--pick`, un path posicional y stdin.

### Changed
- El picker de `echo run --pick` ahora lista los recipes `.echo`
  ordenados por fecha de creación (más reciente primero) en lugar de
  alfabéticamente — birthtime real en macOS, fecha de modificación como
  fallback en otras plataformas; empates se rompen alfabéticamente
  (Unit 52).

## [0.9.0] — 2026-06-10

### Added
- Universal `--build` / `-b` flag (Unit 51): `<cmd> --build` walks you
  through composing the command interactively, then asks what to do with
  the result. Step 1 runs the command's positional picker(s) — modules
  (`install`/`update`/`uninstall`/`test`/`modinfo`/`view`/`i18n-export`/
  `i18n-update`), database (`db-backup`/`db-drop`/`db-neutralize`), backup
  file (`db-restore`), or compose service (`logs`/`restart`); i18n-export/
  i18n-update also prompt for the lang (prefilled `es_MX`). Step 2 is a
  multi-select over the command's known flags (Tab to toggle, Enter to
  confirm, Enter with none selected = no flags). Step 3 prompts for a value
  on each flag that takes one — a picker when the options are known
  (`--level`, report `--level`/`--min-level`) or a text
  field otherwise (`--tags`, logs `-t`, `--out`, report `--step`,
  db-restore `--as`); cancelling a value drops just that flag. Step 4 shows
  the composed line and offers **Run it now** (dispatches it through the
  normal command frame), **Copy to clipboard** (the recipe-style line,
  without the `echo ` prefix, ready to paste into a `.echo` file), or
  **Cancel**. `--build`/`-b` highlight as known flags and Tab-complete on
  every command. Build mode is interactive: a non-TTY invocation (recipe,
  CI) fails closed with exit 2. `--build` must be the only argument
  (extra args → exit 2), and a command with no picker and no flags reports
  "nothing to build" (exit 2). The composer does not encode mutual flag
  exclusions — the commands still validate those at run time.
  `i18n-pull --build` gets a dedicated remote-aware flow: its module
  candidates live on the remote, so it first resolves a connect target
  (one → auto, several → picker), **bakes `--from=<target>`** into the
  composed line for reproducibility, lists that remote's own modules for
  the picker, and prompts for the lang — composing
  `i18n-pull <module> <lang> --from=<target>`. The SSH round-trips
  (`reading remote profile`, `N module(s) found`) surface as INFO
  `echo.build` lines so the waits aren't silent. `--all` / `--installed`
  are not offered there — they would ignore the picked module.
- New `i18n-pull [<mod>] [<lang>] [--from <target>] [--all]` command
  (Unit 50): export a module's translations **from a remote Odoo instance**
  (reached over SSH like `connect`) and write the resulting `.po` into the
  **local repo** at `<addons>/<mod>/i18n/<lang>.po` — for bringing
  translations edited in a remote prod/staging UI back into the working
  tree. The remote is the project's own `[connect]` config by default, or a
  named `connect_target` via `--from`; with neither set it falls back to
  the global connect targets — using the only one automatically, or opening
  a picker when there are several. Per module it runs
  `odoo --i18n-export` inside the remote container, `cat`s the file back
  over SSH, and cleans up the temp file — the remote DB is never modified.
  A single module by default (fuzzy picker when omitted), `--all` pulls
  every candidate (skipping failed ones with a warning). The module list
  comes from the **remote** instance — by default the remote project's own
  modules (the directories under its `addons_path`, read from its
  `odoo.conf` or the addons paths stored in its Echo profile), so you get
  the modules you maintain, not every stock Odoo module; `--installed`
  switches to every installed module (`ir_module_module`) as an escape
  hatch. Resolving over the remote means it works even when the local
  project you run from is unrelated or has no addons. The `.po` lands in the
  module's real addons dir when it's on the host, falling back to a
  cwd-relative `<mod>/i18n/<lang>.po` when it isn't (conf-mode / staging
  whose addons live only in the container). Progress is reported as
  Odoo-style `echo.i18n-pull` log lines (matching `connect`) — `target
  resolved`, `reading remote profile`, `connected`, `listing modules`, and
  an `exporting`/`pulled` line per module — so the SSH waits aren't silent. Default language `es_MX`; one-shot eligible
  (`echo i18n-pull sale es_MX`). Like `connect`, it does **not** require a
  local compose project: run from outside a `docker-compose.yml` directory
  (it writes into the current repo using cwd, or `-C <dir>`) — only a
  remote target is needed (the project's `[connect]` or `--from`).
- `update --i18n` (Unit 49): overwrite the updated modules' translations
  from their `.po` files. The flag adds Odoo's `--i18n-overwrite` to the
  `-u` run, so terms already translated in the database are replaced by the
  modules' shipped translations instead of being kept. It applies to every
  active language (Odoo's `-l` only scopes `i18n-export`/`i18n-import`, not
  a module update — for a single language use `i18n-update <mod> <lang>`).
  Composes with `--all`, `--last`, and `--level`; flag spelling is the same
  across Odoo 17/18/19.
- Project aliases (Unit 48): `-C` now accepts a short alias in place of a
  directory, so `echo -C habitta modstate` works from anywhere. Aliases are
  a user-level `name → local-path` registry in `global.toml` under
  `[project_aliases]` (the same shape as `[connect_targets]`). A real
  directory always wins, so `-C <dir>` is unchanged. Resolution order:
  existing directory → `project_aliases` → a `connect_target` of the same
  name whose `remote_path` is a local directory (free reuse of connect
  names when you run Echo on the server) → otherwise a usage error (exit 2).
- New `alias` command to manage the registry: `alias <name>` registers the
  current project, `alias` / `alias --list` lists all, `alias --rm <name>`
  removes one, and `alias --migrate` backfills aliases from connect targets
  whose `remote_path` resolves locally (explicit and idempotent; reports
  added/skipped). Output is `echo.alias` log lines; headless and one-shot
  eligible (`echo alias --list`).
- `init` now offers an optional alias step at the end (prefilled with the
  project directory's basename); registering it makes `-C <alias>` work,
  leaving it blank skips with no error.

## [0.8.0] — 2026-06-10

### Added
- Migration detection on `install`/`update`/`uninstall`: Echo now watches the
  streamed Odoo log for `odoo.modules.migration` lines (`module <mod>: Running
  migration [<version>] <phase>-migration`) and, after the success/failure
  recap, closes the run with one `echo.<cmd>.migration` INFO line per migrated
  module — `migration detected module=<mod> version=<ver> phases=pre,post`.
  The per-phase lines (pre/post/end) collapse into a single record keyed by
  module + version, and the trailing range marker (`18.0.0.6>`) is trimmed.
  `report` mirrors this: it scans the whole last run (every step, regardless
  of the step/level filter) and appends the same `echo.report.migration`
  summary lines so a migration that happened inside `echo run` is surfaced.
- New `modinfo [<mod>]` command (Unit 42): compare the version Odoo
  recorded as installed in the database (`ir_module_module.latest_version`
  + `state`) against the version declared in the module's
  `__manifest__.py`, printing a one-line verdict as an `echo.modinfo` log
  line — `in sync`, `update pending` (code newer than the DB), `db ahead`,
  or `not installed`. The manifest version is normalized the way Odoo's
  `adapt_version` does (prepends the `17.0` series to a short version)
  before comparing, so `1.3.0` matches the DB's stored `17.0.1.3.0`. With
  no module a single-select picker chooses one; `--copy` copies the report;
  reads the manifest from the host (host mode) or the container (conf
  mode). One-shot eligible (`echo modinfo sale_goals_management`).
  `--last` re-shows the session's last `modinfo` target without the picker
  (in-memory only, per session) — so a result first reached via the picker
  can be copied with `modinfo --last --copy`.
- New `view [<mod>]` command (Unit 43): open a fuzzy picker of a module's
  files and display the chosen one through `bat`/`batcat` (syntax
  highlight + paging) when it's on `PATH`, falling back to a themed
  internal print otherwise. `--copy` copies the file's contents to the
  clipboard instead. Reads files from the host (host mode) or inside the
  Odoo container (conf mode). With no module a module picker runs first.
  `--last` re-displays the session's last viewed file without the pickers
  (in-memory only, per session) — handy to copy a file first reached
  interactively with `view --last --copy`.
- `shell` now restyles the Odoo Python shell's startup block too: the
  injected namespace globals (`env:`, `odoo:`, `openerp:`, `self:`) render as
  Echo structured fields — accent key + dim value — and the stock
  Python/IPython banner lines (`Python …`, `Type '…`, `IPython …`, `Tip: …`)
  are faded so the noise recedes and the prompt stands out. New
  `styleShellBanner` plugged into the shell `LineTransform` after the
  log-line match.

### Changed
- `shell` now colorizes Odoo's startup logs to match the rest of Echo: the
  Odoo log lines the interactive Python shell prints raw through the PTY
  (`… INFO ? odoo: …`, `odoo.modules.loading: …`, `odoo.modules.registry:
  …`) are restyled per-segment with the same renderer used for streamed
  `update`/`install` output (level chip, pastel logger, accent db). The
  interactive parts (IPython banner, prompt, eval output) pass through
  verbatim, and the auto-copy capture keeps the raw ANSI-free text.
  Implemented as an opt-in `LineTransform` on `docker.ExecInteractive`
  (`bash`/`psql` keep the plain passthrough); a 30 ms partial-flush keyed on
  a leading digit means keystroke echo never lags.

### Fixed
- `shell` log colorization also catches Odoo's *own* colored logs. Under
  `shell` (`docker compose exec -t`) Odoo's stdout is a TTY, so its
  `ColoredFormatter` wraps the level/logger in ANSI SGR codes — which broke
  the plain log-line regex, so each line slipped through wearing Odoo's
  coloring instead of Echo's. The `shell` transform now strips ANSI
  (`stripANSISeq`) before matching, so the lines re-render in Echo's style.
  (`update`/`install` use `exec -T`, no TTY, so their logs were already
  plain and unaffected.)
- `shell` now applies the loose-severity fallback (Unit 36) that the
  `update`/`install` stream already had: standalone stderr lines like
  `Warn: Can't find .pfb for face 'Courier'` (wkhtmltopdf/Qt) are reformatted
  into Echo's Odoo style under the synthetic `report.wkhtmltopdf` logger
  instead of leaking raw. The shell `LineTransform` now chains
  `renderLogLine` → `styleShellBanner` → loose-severity → verbatim, mirroring
  `emitStreamLine`. Extracted `renderOdooLog` (the string-returning core of
  `emitOdooLog`) so the transform can reformat without printing directly.

## [0.7.0] — 2026-06-09

### Added
- Per-step `--silent` in `echo run` (Unit 41): append `--silent` to a
  recipe step to suppress its output on screen **and** in the `--log`
  transcript, or `--silent=<lvl>` to drop that level and below while still
  showing more severe lines (`stop --silent=info` hides DEBUG/INFO, keeps
  WARNING/ERROR). The runner's `step N/M →` line and the recap stay visible
  (the recap shows `silent=<all|lvl>`), and silenced lines are still
  captured for `report`, so `report --step=N` can pull them up. `--silent`
  is recipe-only — intercepted by the runner, never passed to the command —
  so it works on any non-interactive step.
- New `report` command (Unit 40) inspects or copies the **last run's** logs
  by step and level, across process boundaries: every `echo run` now
  persists a structured `~/.config/echo/run-logs/last-run.json` (per step:
  command, status, and its captured lines tagged with a log level), and
  `report` queries it. `report --step=<N>` selects a step (default: all);
  `--level=<lvl>` matches that level exactly, `--min-level=<lvl>` matches
  it and more severe (`ERROR` and `CRITICAL` stay distinct); `--copy` puts
  the matched lines on the clipboard (OSC 52-aware), otherwise they print
  to stdout colored by level. Works one-shot (`echo report …`) and in the
  REPL (`report …`). Example: `echo report --step=1 --level=warn --copy`.
- `echo run --pick` (Unit 39) opens a single-select picker of the `*.echo`
  recipe files in the current directory and runs the chosen one — so you
  can launch a recipe without typing its path. Top-level only (no
  recursion); composes with `--continue-on-error` and `--log`. With no
  matches it prints `no .echo recipes found in <dir>`; `--pick` plus a path
  is a usage error; a non-TTY invocation fails closed (exit 2).
- `echo run <file>` now ends with a per-step run summary (Unit 37): one
  `echo.run` line per step with its status (`ok` / `failed` / `cancelled`
  / `skipped`), warning count, and duration (`took`), plus a final
  `run summary` totals line (`steps` / `ok` / `failed` / `skipped` /
  `errors` / `warnings` / `took`; `errors` and `warnings` are always
  reported, even at zero). Under fail-fast the steps after the failure are
  reported as `skipped`. The recap is captured by `--log` like the rest of
  the run. Process exit codes are unchanged.
- Loose-severity stderr lines now reformat into Echo's Odoo log style
  (Unit 36). A line whose first token is a bare severity keyword + `:` —
  e.g. wkhtmltopdf's `Warn: Can't find .pfb for face 'Courier'` or
  `Error: Failed loading page` — is rendered with a timestamp, level chip
  and the synthetic `report.wkhtmltopdf` logger instead of leaking through
  as raw, unstyled text. A loose `Warn:` counts toward the run's warning
  total; a loose `Error:`/`Critical:` is colored but **not** counted as a
  failure, so a noisy tool's stderr can't flip a finished update to ✗.
  Lines inside an active traceback (err/warn inheritance) are left grouped,
  not hijacked. Applies to module (`update`/…) and `logs` output.
- `update --last` repeats the last `update` for the current project and
  database (Unit 35) — the resolved module list, or `--all` if that was
  last — bypassing the picker and running directly. The target is
  persisted on disk (`~/.config/echo/last-updates/<key>.toml`, one record
  per database), so it survives a REPL restart, and is recorded even when
  the update fails, so re-running after a fix just works. The previous
  `--level` is inherited unless overridden on the repeat.
- In the interactive REPL, the `update` fuzzy picker now highlights the
  previous run's modules (Unit 35), and confirming the picker with nothing
  selected offers a brief confirmation to repeat that last update —
  listing the modules — so the empty picker and `--last` are two routes to
  the same "repeat last". Explicit `update <mods>` and `update --all` run
  directly with no confirmation, and script mode (`echo run <file>`,
  `echo update …`) never prompts.
- `echo run <file> --log[=<path>]` writes the whole recipe run to a
  plain-text transcript (Unit 34) — every step's streamed output plus the
  `echo.run` step/summary lines, ANSI-stripped — so an update routine
  leaves an auditable record. Opt-in: bare `--log` writes a timestamped
  file under `~/.config/echo/run-logs/`; `--log=<path>` writes to an
  explicit path; and `--log=<dir>` (e.g. `--log=.` for the current
  directory) writes a `<recipe>.log` named after the recipe into that
  directory. Without the flag, nothing is written. A log-file error warns
  but never aborts the run, and the final line reports the path.
- `--level <lvl>` flag on `update` / `install` / `uninstall` (Unit 33),
  mapping to Odoo's native `--log-level` so a developer can raise or lower
  the verbosity of a module operation (e.g. `update sale --level debug_sql`
  to see the SQL, `--level warn` to quiet it). Both `--level <lvl>` and
  `--level=<lvl>` forms work. The value is validated against Odoo's level
  set (`debug_rpc_answer` … `critical`, `test`, `notset`) — an invalid
  level is rejected with the list of valid ones before Odoo is invoked.
  Without the flag, behavior is unchanged (Odoo's `info` default).
- `echo run <file>` **recipe runner** (Unit 32). Runs a whole update
  routine from a single file — one Echo command per line — instead of N
  separate invocations. Blank lines and `#` comments are ignored; the
  recipe can also be read from stdin (`echo run -` or piped input).
  Comments are stripped both as full lines (`# …`) and inline after a
  command (`update sale  # fix`), so an annotated table pastes in as-is.
  Each
  step streams through the same one-shot path script mode added, and the
  run **stops at the first step that exits non-zero** (fail-fast),
  exiting with that step's code; `--continue-on-error` runs every step
  and exits non-zero if any failed. Progress and the final summary are
  emitted as `echo.run` log lines in Echo's Odoo style. Because steps go
  through the one-shot dispatch, any step that would prompt fails closed
  without a TTY — a recipe must be explicit (module names, `--force`).
- Non-interactive **script mode** (Unit 31). `echo <command> [args]` now
  runs a single command and exits, so Echo can be driven from shell
  scripts and CI (e.g. an update routine that chains `echo stop`,
  `echo up`, `echo update ventas`, `echo restart`). Bare `echo` still
  opens the interactive REPL. One-shot output streams through the exact
  same Odoo-style render and start/finalize frame the REPL uses. The
  process exits with a meaningful code: `0` success, `1` execution error
  (or ERROR/CRITICAL lines counted), `2` usage error (unknown command,
  bad args, or a command that would need a prompt), `3` cancelled. Any
  command that would otherwise block on a confirmation or a fuzzy picker
  **fails closed** when stdin is not a TTY — it returns a clear error and
  a non-zero exit instead of hanging a script, naming the escape hatch
  (pass the missing argument, or `--force`). A human running the same
  command at a real terminal still gets the prompt. New `-C` /
  `--project-dir <dir>` flag runs a one-shot command from outside the
  project directory (like `git -C`).

### Changed
- The `echo run` per-step recap is now fully structured and color-cued:
  `step` and `status` are key=value fields (`step=1/4 status=ok`), the
  status value is colored by outcome (ok green, failed red,
  cancelled/skipped amber), and the `cmd` value is tinted by its action
  (`up`/`stop`/`update`… each a stable color). `report --copy` collapses to
  a single Odoo-style line (`echo.report: copied N lines to clipboard …`)
  instead of a log line plus a separate plain confirmation. Structured log
  lines with an empty message no longer render a stray double space.
- The `update` / `install` / `uninstall` / `test` **start line** now names
  the resolved module(s) — including picker selections and `update --last`,
  which previously logged a generic `echo.update.start`. The line is
  emitted once the module set is known (after the picker / `--last` disk
  read), with the modules in both the logger (`echo.update.module.<mod>` /
  `.modules` / `.all`) and a `modules=` field, so you can tell what's
  running from the start, not only from the end-of-run line.

## [0.6.0] — 2026-06-09

### Added
- `db-neutralize [name]` command and a `--neutralize` flag on `db-restore`
  (Unit 30). Both run Odoo's native `odoo neutralize` CLI inside the Odoo
  container, applying each installed module's `data/neutralize.sql` to
  deactivate production-only parameters (outgoing mail / fetchmail servers,
  cron jobs, payment providers, the environment ribbon, …). `db-neutralize`
  targets the configured DB by default, a positional name, or a picker when
  neither is set, and shows a red confirmation when the target is the active
  DB or `stage=prod` (skippable with `--force`). `db-restore --neutralize`
  neutralizes the freshly restored copy in one step — the prod→test flow.
- `connect` no longer spawns a fresh Chrome window (and a throwaway temp
  profile) on every run (Unit 29). It now reuses a persistent,
  Echo-dedicated Chrome instance (`~/.local/share/echo/connect-chrome`,
  override `$ECHO_CONNECT_CHROME_PROFILE`) and opens the session in a new
  **tab** by default — driving Chrome at the browser level over CDP so it
  never hijacks a tab you already had open. New `--new-window` flag opens
  the session in an isolated **incognito** window instead (its own cookie
  jar), so multiple users can be impersonated at the same time. The
  projectless `echo connect <name>` also honors `--new-window` and
  `--fresh`. The `opening chrome` log line shows `window=tab|incognito`.
- `connect` now caches the minted session locally and reuses it instead of
  re-querying users and re-minting on every run (Unit 28). On a repeated
  `connect <login>`, Echo loads the cached cookie, validates it with a
  single HTTP probe against `<base>/odoo` (a logged-out session redirects to
  the login page), and — if still valid — lands it straight into Chrome,
  skipping both the `res.users` query and the session mint. A stale or
  invalid cookie (past the 5-day TTL or rejected by the probe) is
  transparently re-minted. The interactive `connect` (no login) now offers
  the recently used logins first, with a "↻ Fetch all users…" row to fall
  back to the full list. New `--fresh` flag forces a re-mint, ignoring the
  cache. Sessions are stored per target+db at
  `~/.config/echo/connect-sessions/<key>.toml`.
- `connect` now narrates each step in Echo's Odoo-style log format
  (Unit 28), instead of running silently and printing a couple of plain
  lines at the end. Target resolution, the user query (with count), cache
  hit / validation / reuse / re-mint, the mint, and opening Chrome each
  emit a structured `echo.connect[.cache|.mint]` line — matching the rest
  of the CLI's log stream — closed by the usual `connect completed`.
- Module discovery now falls back to the instance's `odoo.conf` (Unit 26).
  When the host-side addons scan finds no modules — e.g. an instance whose
  addons live only inside the container, declared via `addons_path` in
  `/etc/odoo/odoo.conf` — `modules` / `install` / `update` / `uninstall` /
  `test` no longer fail with `no modules found`. Echo reads the conf inside
  the Odoo container (`conf_path`, default `/etc/odoo/odoo.conf`), parses
  `addons_path`, lists the modules in those container directories, and
  persists `addons_mode = conf` plus the discovered paths to the project
  config. In conf mode the conf is re-read live on each run, so edits to
  `addons_path` are picked up automatically. `modules --config` (the host
  folder picker) is unchanged and always pins `addons_mode = host`, so it
  remains the explicit escape hatch back to host scanning.
- The fuzzy picker now scrolls: long lists (e.g. a full module catalog)
  render in a viewport sized to the terminal height instead of spilling
  past the screen and hiding rows. The window follows the cursor, `pgup` /
  `pgdn` page through it, and `↑ N more` / `↓ N more` hints show how much
  is off-screen. Applies to every picker (modules, db-restore, connect,
  i18n).
- Flag highlighting and flag autocomplete in the REPL (Unit 24), building
  on the command highlighting. Flag tokens are now colored too: a known
  flag of the typed command shows in the accent color (bold), an unknown
  or forwarded flag shows faint — never red, so passthrough commands like
  `down`/`logs`/`connect` don't get falsely flagged. Tab now also completes
  flags: when the token under the cursor starts with `-` and a command
  precedes it, Tab fills the command's flags (single match completes,
  several share a common prefix then list on a second Tab), exactly like
  command completion. Backed by a new per-command flag registry
  (`commandFlags`) kept consistent with `Registry` by an init guard.

### Fixed
- The filestore is now read from and written to the **Odoo container**,
  not the host (Unit 25). Echo previously used the native install path
  `~/.local/share/Odoo/filestore/<db>`, so a restored filestore landed on
  the host where the containerized Odoo couldn't see it — every attachment
  then raised `FileNotFoundError`. `db-restore` now `docker cp`s the
  filestore into `<filestore_path>/<target>/` inside the Odoo container
  (best-effort `chown` so Odoo can also write), and `db-backup
  --with-filestore` pulls the filestore back out of the container. New
  per-project `filestore_path` config (default `/var/lib/odoo/filestore`).

### Changed
- `--force` on `db-drop` (and on `db-restore --force`'s replace step) now
  terminates the target DB's active connections (`pg_terminate_backend`)
  before dropping, instead of aborting (Unit 23). This makes an orphaned
  or busy database — e.g. one left behind by a failed restore — removable
  without manually running `down odoo` first. Without `--force`, `db-drop`
  still guards against active connections (now pointing at `--force` in
  the error) so a live DB isn't dropped by accident.
- `addons_path` entries whose base name starts with `enterprise` (e.g.
  `enterprise`, `enterprise-addons`) are now skipped by default when
  discovering modules from `odoo.conf`, keeping the Enterprise addons out
  of the update/install picker.
- Live command highlighting in the REPL (Unit 21). As you type, the first
  token (the command) is colored fish-style: green/bold when it's an exact
  command, red when it can no longer become one, and the default color
  while it's still a valid prefix (e.g. `ins` toward `install`). Only the
  command word is recolored — arguments keep the default style. Validity
  is driven by the existing command `Registry` (plus `exit`/`quit`), so it
  stays in sync automatically; `lineModel.View()` now renders the line
  itself while the embedded `textinput` keeps owning the (still-blinking)
  cursor. Colors come from `palette.Success` / `palette.Error`, so all four
  themes are covered.
- `db-restore` now also accepts a **standard Odoo backup `.zip`** (the kind
  downloaded from Odoo's database manager / odoo.sh), not just Echo's own
  archives (Unit 22). The restore auto-detects the archive flavor: a
  `dump.sql` (plain SQL) is loaded with `psql` while a `dump.backup`
  (pg_dump custom) keeps using `pg_restore`, and the filestore is copied
  correctly whether it's sharded directly under `filestore/<XX>/…` (Odoo)
  or nested under `filestore/<db>/…` (Echo). The Odoo download timestamp
  `_YYYY-MM-DD_HH-MM-SS` is now stripped when deriving the target db name,
  so `habitta_prod_2026-06-08_23-42-53.zip` restores into `habitta_prod`
  instead of the full timestamped name.

## [0.5.0] — 2026-06-08

### Added
- Docker container log alignment (Unit 20). The per-resource progress
  lines `docker compose` prints during `up` / `down` / `restart` /
  `stop` (e.g. `Container dvz_ny_odoo_19-db-1  Restarting`) are now
  reformatted into Echo's Odoo-style log line — `… INFO <db>
  docker.container: started name=dvz_ny_odoo_19-db-1` — instead of
  passing through raw and standing out as the only unaligned output.
  The logger is `docker.<resource>` (`container` / `network` /
  `volume` / `image`), the compose state becomes the message verb, and
  the resource name rides along as a `name=` field. Transitional states
  (`restarting`, `creating`, …) render faint (DEBUG) so the eye lands
  on the terminal state; compose `Error` / `Warning` states map to
  ERROR / WARNING and feed the run-stats counters so a failed container
  surfaces in the finalize summary. Closes the compose-output gap that
  Unit 08 explicitly deferred. Implements Unit 20.
- Loguru log format support (Unit 19). Lines emitted by `loguru`
  (`YYYY-MM-DD HH:MM:SS.mmm | LEVEL | module:func:line - msg`) are now
  classified, colored, and rendered with the same per-segment styling as
  standard Odoo `logging` lines. `| WARNING |` and `| ERROR |` lines
  increment the run stats counters and trigger auto-copy on failure
  exactly like their `logging` counterparts — closes the gap where a
  loguru ERROR during a test run was invisible to the failure detector.
  Traceback lines following a loguru error inherit the `err` kind for
  copy-on-failure grouping. Implements Unit 19.
- `test <mod...> [--update] [--tags <spec>]` command — runs the Odoo
  test suite for one or more modules. Default mode targets the
  already-installed modules and filters execution to just their tests
  via auto-built `--test-tags /<mod1>,/<mod2>` (no `-u`, fastest loop
  for iterating on Python test code since `--stop-after-init` spawns
  a fresh process that imports the latest disk state). `--update`
  opts into the `-u <mods>` reload for when views / model schema
  changed. `--tags <spec>` overrides the auto filter with a
  user-supplied spec (e.g. `:TestClass.test_method`). Always emits
  `--no-http` and `--http-port=8189` so the test process does not
  clash with the live Odoo bound to 8069 inside the same container
  (the explicit port is a safety net for Odoo 19 Enterprise where
  `--no-http` alone was observed to be ignored). Always emits
  `--log-level=test` (legacy but accepted in 17 / 18 / 19) for
  focused output. Fourth sibling of `install` / `update` / `uninstall`:
  same picker fallback when no module is given, same streaming +
  finalize frame, same auto-copy on failure (logger
  `echo.test.module.<mod>.error`). CLI flag set is identical across
  Odoo 17, 18 and 19. Implements Unit 11.
- `connect [<login>] [--all] [--force]` command — opens Chrome already
  logged in as any user of the configured DB, without their password,
  without opening any port, and without installing anything into Odoo.
  Mints a valid web session by running two embedded Python scripts inside
  the Odoo container (list users + mint via `root.session_store.new()` and
  `_compute_session_token`), then lands the `session_id` cookie into a
  throwaway-profile Chrome through the DevTools Protocol (`Network.setCookie`
  + `Page.navigate` to `<web.base.url>/odoo`) — CDP can set the HttpOnly
  cookie that JavaScript cannot. Minting runs locally via
  `docker compose exec` or, when `[connect].ssh_host` is configured, over
  SSH against the remote host, so the same command works for local and
  public-domain deployments. In remote mode the container/db mapping is
  **read from the server's own Echo profile** over SSH (located by hashing
  `remote_path` with the same key function Echo uses locally) — nothing is
  re-declared on the laptop; only `ssh_host` + `remote_path` are needed.
  When `web.base.url` is `http://` but the same host also serves HTTPS,
  connect probes and upgrades to `https://` (secure cookie + navigation),
  falling back to the original scheme for hosts without HTTPS (e.g. a
  local `http://localhost:8069`). Reuses `runSingleFuzzyPicker` and the
  standard `startLog` / `finalize` / `connectFailureLog` frame. New
  per-project `[connect]` config section (`ssh_host`, `remote_path`,
  `chrome_path`). Implements Unit 18.
- `echo connect [<name>] [<login>] [--add] [--all] [--force]` — projectless
  direct mode that runs from anywhere (no local `docker-compose.yml`),
  using **named remote targets** stored in global config. Registering a
  target picks an SSH host from the user's `~/.ssh/config` (Echo only
  references the alias, never edits the file), then lists that server's
  own Echo projects over SSH and lets you choose one and name it; next
  time `echo connect <name>` (or a picker of registered targets) connects
  straight away. Project profiles now persist `project_path`, and existing
  profiles self-migrate on next launch (`BackfillProjectPath`) so they
  become discoverable as targets — no manual re-init needed.

### Changed
- The Echo binary version shown in the header now carries a build
  metadata suffix: always the build's commit (`+<shortsha>`), plus a
  `.dirty` marker when the working tree had uncommitted or untracked
  changes at build time (e.g. `0.5.0+abc1234` or `0.5.0+abc1234.dirty`).
  Showing the commit even on a clean build pins exactly which revision
  a moved binary came from. The version constant in
  `internal/repl/repl.go` remains the single source of truth, bumped
  together with the `[Unreleased]` → `[X.Y.Z]` promotion in the same
  release commit; the Makefile decorates it via `-ldflags` from
  `git rev-parse --short HEAD` + `git status --porcelain`.
- `make build` now installs the binary straight to `~/.local/bin/echo_cli`
  (commonly on `PATH`) instead of leaving it under `./bin`. `make
  build_release` still emits the multi-platform binaries under `./bin`.

### Fixed
- `test` now passes both `--no-http` and `--http-port=8189` so the
  test process does not clash with the live Odoo server already
  bound to 8069 inside the same container. `--no-http` alone is the
  documented fix but was observed to be silently ignored on Odoo 19
  Enterprise; the explicit `--http-port` redirect guarantees the
  bind goes to an uncommon high port even on that distribution.
  Without these flags the run aborted with `Address already in use`
  before any test could execute. HttpCase suites are unaffected —
  they spin up their own ephemeral server regardless.

## [0.4.0] — 2026-05-19

### Added
- `stop [service]` command — wraps `docker compose stop` to halt the
  Odoo stack without removing the containers, complementing the
  destructive `down`. Hooks into the prompt health cache invalidation
  alongside `up` / `down` / `restart`.

### Changed
- Every command now closes with an Odoo-style end-log line. `finalize`
  was rewritten to emit `INFO echo.<cmd>: <name> completed` on success,
  `WARNING echo.<cmd>.cancelled` when the user aborts a confirmation /
  picker, and `ERROR echo.<cmd>.error` on residual errors — replacing
  the legacy `✓ / ✗ <summary>` print. `up` / `down` / `stop` / `restart`,
  `i18n-export` / `i18n-update`, and `db-backup` / `db-restore` /
  `db-drop` now share the exact start/end frame already used by
  `install` / `update` / `uninstall` and the shell sessions.
- `down` now asks for a red `huh.Confirm` when `stage=prod` before
  tearing down the stack, mirroring the prod-confirm guard already
  applied to `bash` / `psql` / `shell` and `db-drop`. The `--force` flag
  bypasses the prompt and is stripped from the arguments forwarded to
  `docker compose down`. Behavior in `dev` / `staging` is unchanged.
- Read-only commands (`ps`, `logs`, `modules`, `db-list`) now close with
  an Odoo-style end-log line — `INFO echo.<cmd>: <name> completed` on
  success, `ERROR echo.<cmd>.error: <name> failed` on failure — matching
  the start/end pair already emitted by `shell`, `bash`, and `psql`.
  Failures of these commands do not auto-copy to the clipboard since
  they do not change state.

### Added
- Odoo-aware REPL prompt: shows compose project name, Odoo version,
  database, a colored stage chip, and live container health (Odoo +
  Postgres) using Nerd Font glyphs. Segments are configurable via the
  new `[prompt]` block in `~/.config/echo/global.toml`
  (`segments`, `name_max`, `health_ttl`). Container health reads
  through a TTL-cached `docker inspect` and refreshes immediately
  after `up` / `down` / `restart`.
- Per-project `compose_project` override in the project TOML for
  cases where the docker-compose project name does not match the
  folder name (e.g. when set via `COMPOSE_PROJECT_NAME`).

### Changed
- The REPL prompt no longer renders the per-session id. Project
  identity now comes from the docker-compose project name resolved
  from `COMPOSE_PROJECT_NAME`, the per-project `compose_project`
  field, or the normalized project directory basename. The version
  bracket no longer inherits the stage color — the stage is shown as
  an independent colored chip after the bracket.

## [0.3.1] — 2026-05-18

### Fixed
- Ctrl+C during interactive shells (`bash` / `psql` / `shell`) is now
  detected by scanning the stdin byte stream for `0x03` (ETX), since
  raw mode disables the kernel's SIGINT translation and `signal.Notify`
  never fires while the host terminal is raw. The shell session now
  correctly reports `echo.<cmd>.cancelled` (WARN) instead of falling
  through to the ERROR auto-copy path.
- The stdin-reader goroutine spawned by `ExecInteractive` no longer
  leaks after the subprocess exits. It now reads from a `syscall.Dup`
  of stdin that is closed on the way out, unblocking the otherwise
  permanent `Read` with `EBADF` and freeing keystrokes for the REPL
  prompt — fixes the visible REPL "hang" after multiple `shell`
  sessions.

## [0.3.0] — 2026-05-18

### Added
- `db-backup`, `db-restore`, `db-drop`, `db-list` — full database lifecycle
  against the configured Postgres container, with `huh.Confirm` on destructive
  operations and the fzf-style fuzzy picker over `*.dump` / `*.zip` backups.
- `bash`, `psql`, `shell` — interactive sessions inside the running
  containers. The Odoo Python shell bypasses the entrypoint via explicit
  `--db_host` / `--db_port` / `--db_user` / `--db_password` / `--no-http`.
- `i18n-export` / `i18n-update` — translation lifecycle on top of Odoo's
  CLI, with a `/tmp/echo-i18n-*.po` shuffle inside the container plus
  `docker cp` to/from the host. Default language `es_MX`; prod-confirm on
  update.
- Tab autocomplete on the command registry (bash-style: LCP on first Tab,
  match listing on second consecutive Tab).
- `copy-last` and `copy-last --errors` — copy the previous command's
  output to the clipboard, optionally filtered to `err` / `warn` lines.
- Auto-copy on failure for every subprocess-backed command
  (`install` / `update` / `uninstall`, `bash` / `psql` / `shell`,
  `i18n-export` / `i18n-update`, `db-backup` / `db-restore` / `db-drop`,
  `up` / `down` / `restart`). The clipboard payload starts with an Odoo
  log-style header.
- 8-pastel rotation for Odoo logger names (FNV-1a hash so each logger
  keeps the same colour across runs).
- Hierarchical loggers for echo's own events: `echo.<cmd>.start`,
  `echo.<cmd>` (completed), `echo.<cmd>.error`, `echo.<cmd>.cancelled`.
  For module commands the path embeds the resolved target
  (`echo.update.module.<mod>`, `.modules`, `.all`).
- OSC 52 priority for the clipboard package when running under SSH or
  tmux (`$SSH_TTY` / `$SSH_CONNECTION` / `$TMUX`).
- Warning count exposed alongside error count on the post-command status
  line and on the structured ERROR field.

### Changed
- Post-command status lines (✓ / ✗) replaced by manually-rendered Odoo
  log lines so they sit next to the container's own log stream.
  `charmbracelet/log` is no longer used inside the REPL.
- Odoo log stream now renders per-segment: timestamp dim, PID faint,
  4-char level chip (`DEBU` / `INFO` / `WARN` / `ERRO` / `CRIT`) coloured
  per level, `db` in `palette.Accent`, logger via the pastel rotation,
  message in default foreground.
- Charm palette `Warning` switched from orange (`#f6ad55`) to pastel
  yellow (`#fde047`).
- Traceback continuation kind-inheritance no longer requires line
  indentation, so the full `Traceback (most recent call last):` block
  plus the `ExceptionType: message` tail is captured by auto-copy.
- `RunInstall` / `RunUpdate` / `RunUninstall` return the resolved
  modules so the REPL labels its report with real targets even after
  the fuzzy picker runs.
- Odoo log classifier anchors on the full prefix (`^ts pid LEVEL `) —
  stray `DEBUG` / `INFO` keywords inside traceback comments no longer
  break err-kind inheritance.
- Interactive shells go through a host-side PTY (`github.com/creack/pty`)
  so the combined container output can be tee'd into the auto-copy
  buffer without breaking interactivity.

### Fixed
- `RunOdooShell` no longer crashes Odoo with `ValueError: int('')` when
  `POSTGRES_PORT` is missing from `.env`; the missing flag is now
  skipped via `odoo.Conn.Flags()`, with a defensive default of `5432`.
- `ErrCancelled` text generalised from `"init cancelled"` to
  `"cancelled by user"` — the error is reused by every picker and
  prod-confirm, the old wording was misleading outside `init`.
- Ctrl+C during an interactive shell is now reported as a WARNING
  (`echo.<cmd>.cancelled`) instead of triggering an ERROR auto-copy of
  the `KeyboardInterrupt` traceback the user just produced.

## [0.2.0] — 2026-05-12

### Added
- `init` command (v2): interactive `huh` form with live docker
  introspection (`compose ps`, `psql -lqt`) and `.env` parsing.
- `install` / `update` / `uninstall` / `modules` — Odoo module
  lifecycle via `compose exec -T`.
- `up` / `down` / `restart` / `ps` / `logs` — Docker compose lifecycle
  with streamed output and a `--copy` flag on `logs`.
- Fuzzy picker (fzf-style, Bubble Tea) for module selection.
- Odoo log-level colouring with traceback inheritance.
- Action-result lines (`✓` / `✗`) after every long-running command.
- Persistent command history at `~/.config/echo/history`.

### Changed
- Theme and stage are now loaded from `~/.config/echo/` instead of
  being hardcoded.

## [0.1.0] — 2026-05-07

### Added
- Initial scaffold with theme system (4 palettes), two-column header,
  REPL prompt, and the `ls` command.

[Unreleased]: #unreleased
[0.9.0]: #090--2026-06-10
[0.6.0]: #060--2026-06-09
[0.3.1]: #031--2026-05-18
[0.3.0]: #030--2026-05-18
[0.2.0]: #020--2026-05-12
[0.1.0]: #010--2026-05-07
