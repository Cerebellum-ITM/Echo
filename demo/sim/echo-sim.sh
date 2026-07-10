#!/usr/bin/env bash
# echo-sim.sh — VHS simulation-mode dispatcher for Echo.
#
# Echo wraps Docker / Odoo / SSH, so recording the real binary would need a
# live stack and would leak private hostnames, DB names and paths. This file
# reproduces Echo's exact on-screen styling with fully invented data instead.
#
# Faithfulness notes (verified against the Go source):
#   * tokyo palette  → internal/theme/theme.go (var Tokyo)
#   * log-line shape → internal/repl/logrender.go / logemit.go
#     ts(dim) pid(faint) LEVEL-chip(bold,level) db(accent) logger(pastel) msg(fg)
#   * level chips    → DEBU/INFO/WARN/ERRO/CRIT (shortLevel)
#   * logger pastel  → FNV-1a(name) % 8 over loggerPalette (precomputed below)
#   * field colors   → keyColor / valueStyleFor in logemit.go
#   * prompt + glyphs → internal/repl/prompt.go + internal/banner/header.go
#
# The on-screen command (up / update / deploy / …) is a shell function defined
# here; VHS types it after a PS1 that mimics Echo's styled REPL prompt.

ESC=$'\033'
DB="my_shop"
EPID="28471"   # echo's own pid (echo.* and docker.* reformatted lines)
OPID="7"       # odoo process pid inside the container (streamed odoo.* lines)

# --- tokyo palette, as "R;G;B" truecolor triples (internal/theme/theme.go) ---
FG="192;202;245"; DIM="121;130;169"; FAINT="86;95;137"; ACCENT="122;162;247"
SUCCESS="158;206;106"; WARNING="224;175;104"; ERROR="247;118;142"; INFO="125;207;255"

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/glyphs.sh"

# --- colored-output primitives ------------------------------------------------
_c()  { printf '%s[38;2;%sm%s%s[0m' "$ESC" "$1" "$2" "$ESC"; }   # fg
_cb() { printf '%s[1;38;2;%sm%s%s[0m' "$ESC" "$1" "$2" "$ESC"; } # bold fg

# loggerColor: FNV-1a(name) % 8 over the pastel rotation (logrender.go).
# Precomputed so the on-screen tone matches the binary exactly per logger.
_logcolor() {
  case "$1" in
    docker.network)                     printf '255;214;165';;  # peach
    docker.container)                   printf '160;196;255';;  # sky
    echo.up.start)                      printf '189;178;255';;  # lavender
    echo.up)                            printf '155;246;255';;  # cyan
    echo.update.module.sale.start)      printf '202;255;191';;  # mint
    echo.update.module.sale)            printf '255;179;186';;  # coral
    echo.update.module.base.start)      printf '160;196;255';;  # sky
    echo.update.module.base)            printf '255;198;255';;  # pink
    odoo.modules.loading)               printf '255;214;165';;  # peach
    odoo.modules.registry)              printf '255;179;186';;  # coral
    odoo.addons.base.models.ir_module)  printf '202;255;191';;  # mint
    odoo.tools.translate)               printf '189;178;255';;  # lavender
    odoo.sql_db)                        printf '255;214;165';;  # peach
    odoo.service.server)                printf '255;198;255';;  # pink
    echo.deploy.remote)                 printf '240;166;202';;  # rose
    echo.deploy)                        printf '189;178;255';;  # lavender
    echo.deploy.compose)                printf '240;166;202';;  # rose
    echo.deploy.odoo)                   printf '255;179;186';;  # coral
    echo.db-list)                       printf '155;246;255';;  # cyan
    echo.db-restore)                    printf '155;246;255';;  # cyan
    echo.db-restore.restore)            printf '240;166;202';;  # rose
    echo.modinfo)                       printf '202;255;191';;  # mint
    echo.sequence)                      printf '189;178;255';;  # lavender
    echo.sequence.step)                 printf '155;246;255';;  # cyan
    echo.sequence.build)                printf '202;255;191';;  # mint
    echo.push)                          printf '255;198;255';;  # pink
    echo.push.remote)                   printf '255;198;255';;  # pink
    echo.push.module)                   printf '255;179;186';;  # coral
    echo.db-pull)                       printf '160;196;255';;  # sky
    echo.db-pull.remote)                printf '160;196;255';;  # sky
    echo.db-pull.dump)                  printf '160;196;255';;  # sky
    echo.db-pull.restore)               printf '160;196;255';;  # sky
    echo.db-pull.filestore)             printf '255;214;165';;  # peach
    echo.compare)                       printf '189;178;255';;  # lavender
    echo.logview)                       printf '189;178;255';;  # lavender
    echo.watch)                         printf '189;178;255';;  # lavender
    echo.watch.logs)                    printf '202;255;191';;  # mint
    echo.watch.cycle)                   printf '189;178;255';;  # lavender
    odoo.addons.base.models.ir_cron)    printf '202;255;191';;  # mint
    werkzeug)                           printf '160;196;255';;  # sky
    *)                                  printf '202;255;191';;
  esac
}

# _chip LEVEL -> "CHIP COLORTRIPLE" (shortLevel in logrender.go)
_chip() {
  case "$1" in
    DEBUG)    printf 'DEBU %s' "$FAINT";;
    INFO)     printf 'INFO %s' "$INFO";;
    WARNING)  printf 'WARN %s' "$WARNING";;
    ERROR)    printf 'ERRO %s' "$ERROR";;
    CRITICAL) printf 'CRIT %s' "$ERROR";;
    *)        printf '%s %s' "$1" "$FG";;
  esac
}

# _field key=val -> styled ` key=val` (keyColor / valueStyleFor in logemit.go)
_field() {
  local kv="$1" k="${1%%=*}" v="${1#*=}" kc vc="38;2;${FG}" qv
  case "$k" in
    module|modules) kc="1;38;2;${ACCENT}";;
    err|errors)     kc="1;38;2;${ERROR}";;
    warnings)       kc="1;38;2;${WARNING}";;
    copied)         kc="1;38;2;${INFO}";;
    *)              kc="38;2;${DIM}";;
  esac
  case "$k" in
    status) case "$v" in
      ok)                 vc="1;38;2;${SUCCESS}";;
      failed)             vc="1;38;2;${ERROR}";;
      cancelled|skipped)  vc="38;2;${WARNING}";;
    esac;;
  esac
  qv="$v"; case "$v" in *' '*) qv="\"$v\"";; esac
  printf ' %s[%sm%s%s[0m=%s[%sm%s%s[0m' "$ESC" "$kc" "$k" "$ESC" "$ESC" "$vc" "$qv" "$ESC"
}

# _log LEVEL PID LOGGER MSG [key=val ...] — one Odoo-format log line.
_log() {
  local level="$1" pid="$2" logger="$3" msg="$4"; shift 4
  local chip cc ts lc
  read -r chip cc <<<"$(_chip "$level")"
  lc="$(_logcolor "$logger")"
  ts="$(date '+%Y-%m-%d %H:%M:%S'),$(printf '%03d' $(( (RANDOM % 900) + 100 )))"
  printf '%s' "${LOGPREFIX:-}"
  printf '%s' "$(_c "$DIM" "$ts") $(_c "$FAINT" "$pid") $(_cb "$cc" "$chip") $(_c "$ACCENT" "$DB") $(_c "$lc" "${logger}:")"
  [ -n "$msg" ] && printf ' %s' "$(_c "$FG" "$msg")"
  local f; for f in "$@"; do _field "$f"; done
  printf '\n'
}

# --- prompt: mimic Echo's styled REPL prompt (segments name/version_db/stage/
#     health, prompt.go). dev stage -> success color; containers running ->
#     success glyphs. Non-printing escapes are wrapped in \[ \] for readline. -----
echo_ps1() {
  local b="\[${ESC}[1m\]" r="\[${ESC}[0m\]"
  local a="\[${ESC}[38;2;${ACCENT}m\]" d="\[${ESC}[38;2;${DIM}m\]"
  local f="\[${ESC}[38;2;${FAINT}m\]" g="\[${ESC}[38;2;${FG}m\]"
  local s="\[${ESC}[38;2;${SUCCESS}m\]" i="\[${ESC}[38;2;${INFO}m\]"
  PS1="${a}${G_LOGO}${r} ${a}${b}echo-my-shop${r} "
  PS1+="${d}[${r}${g}18.0${r}${f} · ${r}${d}my_shop]${r} "
  PS1+="${s}${b}dev${r} ${s}${G_DOCKER} ${G_PG}${r}"
  PS1+="${g}:${r}${i}~${r}${g}\$ ${r}"
  export PS1
}

# --- the startup header (banner.Render), rendered once for the hero GIF -------
# Box geometry mirrors banner.Render: leftW=40, rightW=39, so each row spans
# 1+1+40+1+1+1+39+1+1 = 86 cols. The left column shows the SHADOW banner style
# (ANSI Shadow "ECHO" with a per-row gradient of the dev-stage colour + ripple),
# matching internal/banner/echo.go. The gradient steps are Lighten/Darken of the
# tokyo SUCCESS token (dev stage), computed with the same factors as the Go code.
echo_header() {
  local title='─── Echo v0.14.0 '   # 17 visible cols
  # gradient of SUCCESS (158;206;106): lighten top rows, darken bottom rows.
  local G1='202;228;173' G2='179;217;139' G3='158;206;106'
  local G4='139;181;93'  G5='120;157;81'  G6='101;132;68'
  local RP='192;223;158'   # ripple = Lighten(success, 0.35)
  printf '%s%s%s%s\n' "$(_c "$FAINT" '╭')" "$(_c "$ACCENT" "$title")" \
    "$(_c "$FAINT" "$(_dash 67)")" "$(_c "$FAINT" '╮')"
  _hrow ""                                                          ""
  _hrow "$(_cb "$FG" '  Welcome back pascual!')"                    "$(_cb "$WARNING" 'Tips for getting started')"
  _hrow ""                                                          "$(_c "$FG" 'Run ')$(_c "$INFO" 'help')$(_c "$FG" ' to see all commands')"
  _hrow "$(_cb "$G1" '  ███████╗ ██████╗██╗  ██╗ ██████╗ ')$(_c "$RP" '·')"        "$(_c "$FG" 'Type ')$(_c "$INFO" 'exit')$(_c "$FG" ' or Ctrl+D to quit')"
  _hrow "$(_cb "$G2" '  ██╔════╝██╔════╝██║  ██║██╔═══██╗ ')$(_c "$RP" ')))')"      "$(_c "$FAINT" "$(_dash 37)")"
  _hrow "$(_cb "$G3" '  █████╗  ██║     ███████║██║   ██║ ')$(_c "$RP" ' ·')"       "$(_cb "$WARNING" "What's new")"
  _hrow "$(_cb "$G4" '  ██╔══╝  ██║     ██╔══██║██║   ██║')"                        "$(_c "$DIM" '· Stage-colored startup banner')"
  _hrow "$(_cb "$G5" '  ███████╗╚██████╗██║  ██║╚██████╔╝')"                        "$(_c "$DIM" '· ')$(_c "$INFO" 'deploy')$(_c "$DIM" ' — ship commits to a server')"
  _hrow "$(_cb "$G6" '  ╚══════╝ ╚═════╝╚═╝  ╚═╝ ╚═════╝')"                         "$(_c "$DIM" '· ')$(_c "$INFO" 'connect')$(_c "$DIM" ' — open Odoo as any user')"
  _hrow ""                                                          ""
  _hrow "$(_c "$DIM" '  tokyo · dev')"                              ""
  _hrow "$(_c "$DIM" '  ~/dev/my-shop')"                            ""
  _hrow ""                                                          ""
  printf '%s%s%s\n' "$(_c "$FAINT" '╰')" "$(_c "$FAINT" "$(_dash 84)")" "$(_c "$FAINT" '╯')"
}

# _dash N — N box-drawing dashes (─), built in a loop so it works on bash 3.2.
_dash() { local i s=''; for (( i = 0; i < $1; i++ )); do s+='─'; done; printf '%s' "$s"; }

# _hrow LEFT RIGHT — one header row, padded to the box columns (leftW=40,
# rightW=39). Padding is computed on the plain text, then colored, so ANSI
# escapes never throw off the alignment.
_hrow() {
  local lp rp bar
  bar="$(_c "$FAINT" '│')"
  lp="$(_pad "$1" 40)"; rp="$(_pad "$2" 39)"
  printf '%s %s %s %s %s\n' "$bar" "$lp" "$bar" "$rp" "$bar"
}

# _pad STYLED WIDTH — pad a styled string to WIDTH visible columns.
_pad() {
  local plain n
  plain="$(printf '%s' "$1" | sed "s/$ESC\[[0-9;]*m//g")"
  n=${#plain}
  if [ "$n" -ge "$2" ]; then printf '%s' "$1"; else
    printf '%s%*s' "$1" $(( $2 - n )) ''
  fi
}

# ============================ command sims ====================================

up() {
  _log INFO  "$EPID" echo.up.start    "up"
  sleep 0.15
  _log INFO  "$EPID" docker.network   "created"  "name=my-shop_default"; sleep 0.12
  _log DEBUG "$EPID" docker.container "creating" "name=my-shop-db-1";    sleep 0.10
  _log INFO  "$EPID" docker.container "started"  "name=my-shop-db-1";    sleep 0.28
  _log DEBUG "$EPID" docker.container "creating" "name=my-shop-odoo-1";  sleep 0.10
  _log INFO  "$EPID" docker.container "started"  "name=my-shop-odoo-1";  sleep 0.20
  printf '\n'
  _log INFO  "$EPID" echo.up "up completed"
}

update() {
  if [ "$1" = "--installed" ]; then _update_installed; return; fi
  if [ "$1" = "--build" ]; then _update_build; return; fi
  _log INFO "$EPID" echo.update.module.sale.start "update" "modules=sale" "flags=--i18n"
  sleep 0.25
  _log INFO "$OPID" odoo.modules.loading              "loading 1 modules...";                       sleep 0.22
  _log INFO "$OPID" odoo.modules.loading              "Loading module sale (1/1)";                  sleep 0.35
  _log INFO "$OPID" odoo.addons.base.models.ir_module "module sale: loading translation es_MX";     sleep 0.25
  _log INFO "$OPID" odoo.tools.translate              "module sale: overwriting es_MX translation"; sleep 0.25
  _log INFO "$OPID" odoo.modules.loading              "Module sale loaded in 0.42s, 318 queries (+11 other)"; sleep 0.20
  _log INFO "$OPID" odoo.modules.loading              "1 modules loaded in 0.44s, 318 queries";     sleep 0.15
  _log INFO "$OPID" odoo.modules.registry             "Registry loaded in 0.51s";                   sleep 0.20
  printf '\n'
  _log INFO "$EPID" echo.update.module.sale "update completed"
}

# update --installed: the picker is sourced from the DB's installed modules
# (ir_module_module), so core modules like `base` are pickable — not just the
# repo's addons. Here `base` is selected and updated.
_update_installed() {
  printf '\n  %s\n' "$(_cb "$WARNING" 'Installed modules to update')"
  printf '  %s %s  %s\n' "$(_c "$ACCENT" '▸')" "$(_c "$SUCCESS" '☑')" "$(_c "$FG"  'base')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'web')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'mail')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'account')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'sale')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'sale_extra')"
  printf '\n'; sleep 0.6
  _log INFO "$EPID" echo.update.module.base.start "update" "modules=base"; sleep 0.25
  _log INFO "$OPID" odoo.modules.loading  "loading 1 modules...";                         sleep 0.22
  _log INFO "$OPID" odoo.modules.loading  "Loading module base (1/1)";                     sleep 0.32
  _log INFO "$OPID" odoo.modules.loading  "Module base loaded in 0.88s, 612 queries";      sleep 0.22
  _log INFO "$OPID" odoo.modules.loading  "1 modules loaded in 0.91s, 612 queries";        sleep 0.15
  _log INFO "$OPID" odoo.modules.registry "Registry loaded in 1.02s";                      sleep 0.20
  printf '\n'
  _log INFO "$EPID" echo.update.module.base "update completed"
}

deploy() {
  _log INFO "$EPID" echo.deploy.remote "target resolved" "host=erp-prod" "path=/srv/odoo/my-shop"
  sleep 0.30
  printf '\n  %s\n' "$(_cb "$WARNING" 'Select commits / dirty modules to deploy')"
  printf '  %s %s  %s\n' "$(_c "$ACCENT" '▸')" "$(_c "$SUCCESS" '☑')" "$(_c "$WARNING" '~ stock_extra  ·  uncommitted (3 files)')"
  printf '    %s  %s\n'   "$(_c "$SUCCESS" '☑')"                      "$(_c "$FG"  'a1b2c3d  [FIX] sale_extra: correct tax rounding')"
  printf '    %s  %s\n'   "$(_c "$SUCCESS" '☑')"                      "$(_c "$FG"  'e4f5a6b  [ADD] website_promo: launch banner')"
  printf '    %s  %s\n'   "$(_c "$FAINT" '☐')"                       "$(_c "$DIM" '0c1d2e3  [IMP] docs: update install notes')"
  printf '\n'; sleep 0.55
  _log INFO    "$EPID" echo.deploy "items selected" "commits=2" "dirty=1"; sleep 0.18
  _log INFO    "$EPID" echo.deploy "resolved" "module=stock_extra"   "via=dirty";   sleep 0.15
  _log WARNING "$EPID" echo.deploy "selected modules have uncommitted changes — deploy updates them on the server but does not push the code" "modules=stock_extra"; sleep 0.30
  _log INFO    "$EPID" echo.deploy "resolved" "commit=a1b2c3d" "module=sale_extra"    "via=subject"; sleep 0.15
  _log INFO    "$EPID" echo.deploy "resolved" "commit=e4f5a6b" "module=website_promo" "via=diff";    sleep 0.22
  _log INFO    "$EPID" echo.deploy "plan"     "update=sale_extra,stock_extra" "install=website_promo" "skipped=1"; sleep 0.28
  _log INFO "$EPID" echo.deploy.compose "stop";  sleep 0.22
  _log INFO "$EPID" echo.deploy.compose "up -d"; sleep 0.28
  _log INFO "$EPID" echo.deploy.odoo    "running module install/update"; sleep 0.30
  _log INFO "$OPID" odoo.modules.loading "loading 3 modules...";                  sleep 0.25
  _log INFO "$OPID" odoo.modules.loading "Module website_promo loaded in 0.61s";  sleep 0.22
  _log INFO "$OPID" odoo.modules.loading "Modules loaded.";                       sleep 0.20
  printf '\n'
  _log INFO "$EPID" echo.deploy "deploy complete" "update=2" "install=1" "skipped=1"
}

db-list() {
  printf '\n'
  printf '  %s  %s  %s\n' \
    "$(_cb "$ACCENT" "$(printf '%-15s' name)")" \
    "$(_cb "$ACCENT" "$(printf '%-5s' size)")" \
    "$(_cb "$ACCENT" created)"
  _dbrow 1 my_shop          '82 MB' '2026-05-12 09:14'
  _dbrow 0 my_shop_staging  '41 MB' '2026-05-30 18:02'
  _dbrow 0 my_shop_test     '12 MB' '2026-06-11 11:47'
  _dbrow 0 template_demo    '3 MB'  '2026-04-02 08:20'
  printf '\n'
  _log INFO "$EPID" echo.db-list "databases listed" "count=4"
}

# _dbrow ACTIVE NAME SIZE CREATED
_dbrow() {
  local mark name
  if [ "$1" = 1 ]; then
    mark="$(_c "$SUCCESS" '●') "
    name="$(_c "$SUCCESS" "$(printf '%-15s' "$2")")"
  else
    mark='  '
    name="$(_c "$FG" "$(printf '%-15s' "$2")")"
  fi
  printf '%s%s  %s  %s\n' "$mark" "$name" \
    "$(_c "$DIM" "$(printf '%-5s' "$3")")" "$(_c "$DIM" "$4")"
}

db-restore() {
  # 1) backup picker — an odoo.sh dump with a long, unwieldy name
  printf '\n  %s\n' "$(_cb "$WARNING" 'Pick a backup to restore')"
  printf '  %s %s\n' "$(_c "$ACCENT" '▸')" "$(_c "$FG"  'mycompany-main-prod_2026-06-18_23-42-53.zip')"
  printf '    %s\n'  "$(_c "$DIM" 'my_shop_20260611-1147.dump')"
  printf '\n'; sleep 0.7

  # 2) rename prompt — derived name pre-filled, then edited to a short one
  printf '  %s  %s\n' "$(_cb "$ACCENT" 'Restore as')" "$(_c "$DIM" 'edit to rename, Enter to accept')"
  printf '  %s %s' "$(_c "$ACCENT" '›')" "$(_c "$FAINT" 'mycompany-main-prod')"; sleep 0.9
  printf '\r%s[K  %s %s\n' "$ESC" "$(_c "$ACCENT" '›')" "$(_c "$FG" 'staging')"; sleep 0.5

  # 3) live progress — milestones (INFO) bracket the pg_restore stream (DEBUG)
  local DB='staging'
  printf '\n'
  _log INFO  "$EPID" echo.db-restore.restore "creating database"; sleep 0.30
  _log INFO  "$EPID" echo.db-restore.restore "restoring data" "file=mycompany-main-prod_2026-06-18_23-42-53.zip"; sleep 0.30
  _log DEBUG "$EPID" echo.db-restore.restore 'creating TABLE "public"."res_partner"';            sleep 0.16
  _log DEBUG "$EPID" echo.db-restore.restore 'restoring data for table "public"."account_move"'; sleep 0.16
  _log DEBUG "$EPID" echo.db-restore.restore 'creating INDEX "public"."sale_order_pkey"';         sleep 0.16
  _log DEBUG "$EPID" echo.db-restore.restore 'creating CONSTRAINT FK "public"."account_move_line"'; sleep 0.16
  _log INFO  "$EPID" echo.db-restore.restore "copying filestore"; sleep 0.28
  printf '  %s %s\n' "$(_c "$SUCCESS" '→')" "$(_c "$FG" 'staging (with filestore)')"
  printf '\n'
  _log INFO  "$EPID" echo.db-restore "db-restore completed"
}

modinfo() {
  case "${1:-sale}" in
    account)
      _log WARNING "$EPID" echo.modinfo "module inspected" \
        "module=account" "db=18.0.1.2" "state=installed" "manifest=18.0.1.4" "status=update pending";;
    *)
      _log INFO "$EPID" echo.modinfo "module inspected" \
        "module=sale" "db=18.0.1.3" "state=installed" "manifest=18.0.1.3" "status=up to date";;
  esac
}

# sequence: build several commands in order and run them (Unit 73). Shows the
# tri-state picker in its final selected state (⟦n⟧ = run order, tools glyph =
# builder mode), then the log-framed review (layout A), then execution through
# the recipe engine — closing the summary BEFORE the terminal `logs` follow.
sequence() {
  # 1) tri-state picker (final state). Build rows carry the cod-tools glyph.
  printf '\n  %s\n' "$(_cb "$WARNING" 'sequence — pick commands · order = pick order')"
  _seqpick "$(_c "$ACCENT" '❯')" "$(_c "$SUCCESS" '⟦1⟧')" "$(_c "$ACCENT" "$G_TOOLS")" update   'build flags'
  _seqpick '  '                   "$(_c "$SUCCESS" '⟦2⟧')" "$(_c "$ACCENT" "$G_TOOLS")" test     'build flags'
  _seqpick '  '                   "$(_c "$SUCCESS" '⟦3⟧')" ' '                          restart  'run as-is'
  _seqpick '  '                   "$(_c "$SUCCESS" '⟦4⟧')" ' '                          logs     'follow · forced last'
  _seqpick '  '                   "$(_c "$FAINT" '⟦ ⟧')"   ' '                          db-backup ''
  printf '\n'; sleep 1.0

  # 2) builders for the marked steps (return-only) — one line each.
  _log INFO "$EPID" echo.sequence.build "building step" "step=1/2" "command=update"; sleep 0.25
  _log INFO "$EPID" echo.sequence.build "building step" "step=2/2" "command=test";   sleep 0.30
  printf '\n'

  # 3) log-framed review (layout A). Bar = stage color (dev → green).
  local bar; bar="$(_c "$SUCCESS" '│ ')"
  printf '%s%s%s%s%s%s%s\n' "$bar" "$(_c "$DIM" 'sequence · ')" "$(_c "$FG" '4 steps')" \
    "$(_c "$DIM" ' · ')" "$(_c "$SUCCESS" 'dev/18.0')" "$(_c "$DIM" ' · ')" "$(_c "$SUCCESS" 'local')"
  printf '%s\n' "$(_c "$SUCCESS" '│')"
  _seqstep "$bar" 1 update  "$(_c "$INFO" '--all') $(_c "$INFO" '--level')$(_c "$DIM" '=warn')" ''
  _seqstep "$bar" 2 test    "$(_c "$INFO" 'sale') $(_c "$INFO" '--tags')$(_c "$DIM" '=/sale')" ''
  _seqstep "$bar" 3 restart "" ''
  _seqstep "$bar" 4 logs    "" "$(_c "$WARNING" 'follow · ends the run')"
  printf '\n'; sleep 0.6

  # 4) action select — "Run it now" highlighted, then chosen.
  printf '  %s\n'   "$(_cb "$FG" 'Apply this sequence?')"
  printf '  %s %s\n' "$(_c "$ACCENT" '❯')" "$(_cb "$FG" 'Run it now')"
  printf '    %s\n'  "$(_c "$DIM" 'Save as recipe (.echo)')"
  printf '    %s\n'  "$(_c "$DIM" 'Copy to clipboard')"
  printf '    %s\n'  "$(_c "$DIM" 'Cancel')"
  printf '\n'; sleep 0.9

  # 5) execution through the recipe engine.
  _log INFO "$EPID" echo.sequence      "running" "steps=4" "mode=local"; sleep 0.25
  _log INFO "$EPID" echo.sequence.step "step 1/4 → update --all --level=warn"; sleep 0.20
  _log INFO "$OPID" odoo.modules.loading "Modules loaded.";                    sleep 0.25
  _log INFO "$EPID" echo.sequence.step "" "step=1/4" "status=ok" "warnings=2" "took=18.6s"; sleep 0.20
  _log INFO "$EPID" echo.sequence.step "step 2/4 → test sale --tags=/sale";    sleep 0.20
  _log INFO "$OPID" odoo.modules.loading "0 failed, 0 error(s) of 42 tests";   sleep 0.25
  _log INFO "$EPID" echo.sequence.step "" "step=2/4" "status=ok" "took=1m15s"; sleep 0.20
  _log INFO "$EPID" echo.sequence.step "step 3/4 → restart";                   sleep 0.22
  _log INFO "$EPID" echo.sequence.step "" "step=3/4" "status=ok" "took=3.1s";  sleep 0.20
  _log INFO "$EPID" echo.sequence "sequence complete" "steps=4" "ok=3" "errors=0" "warnings=2" "took=1m37s"; sleep 0.30
  _log WARNING "$EPID" echo.sequence.step "step 4/4 → logs (follow, ^c to stop)"; sleep 0.30
  _log INFO "$OPID" werkzeug "127.0.0.1 - - GET /web HTTP/1.1 200 -"
}

# _seqpick CURSOR BADGE GLYPH NAME HINT — one tri-state picker row, name padded.
_seqpick() {
  printf '  %s %s %s %s' "$1" "$2" "$3" "$(_c "$FG" "$(printf '%-10s' "$4")")"
  [ -n "$5" ] && printf '  %s' "$(_c "$DIM" "$5")"
  printf '\n'
}

# _seqstep BAR N CMD ARGS NOTE — one review step (command accent, args styled).
_seqstep() {
  printf '%s%s%s' "$1" "$(_c "$DIM" "$2  ")" "$(_cb "$ACCENT" "$3")"
  [ -n "$4" ] && printf ' %s' "$4"
  [ -n "$5" ] && printf '   %s' "$5"
  printf '\n'
}

# push: rsync selected local modules to a remote target's addons dir over SSH
# (Unit 83). Each module is bracketed by a greppable syncing/synced frame; its
# file changes render as a colored change tree — dim connectors, an op glyph
# (+ new = success, ~ changed = warning, − deleted = error), a dim nerd-font
# file-type icon, and the file name in fg. Directory nodes are all-dim with a
# folder glyph. Mirrors internal/repl/push.go renderSyncTree.
push() {
  _log INFO "$EPID" echo.push.remote "target resolved" "host=erp-staging" "path=/srv/odoo/my-shop"
  sleep 0.30
  _log INFO "$EPID" echo.push.module "syncing" "module=sale_extra" "dest=/srv/odoo/my-shop/addons/sale_extra"
  sleep 0.28
  _treef '├─ '   changed "$GI_PY"  '__manifest__.py'; sleep 0.06
  _treed '├─ '   'i18n/'
  _treef '│    ' changed "$GI_PO"  'es_MX.po';         sleep 0.06
  _treed '├─ '   'models/'
  _treef '│    ' changed "$GI_PY"  'sale_order.py';    sleep 0.06
  _treef '│    ' new     "$GI_PY"  'sale_report.py';   sleep 0.06
  _treed '└─ '   'views/'
  _treef '     ' changed "$GI_XML" 'sale_order_views.xml'
  sleep 0.32
  _log INFO "$EPID" echo.push.module "synced" "module=sale_extra" "new=1" "changed=4"
  sleep 0.28
  _log INFO "$EPID" echo.push.module "syncing" "module=website_promo" "dest=/srv/odoo/my-shop/addons/website_promo"
  sleep 0.22
  _treef '├─ '   new "$GI_PY"  '__manifest__.py'; sleep 0.06
  _treed '└─ '   'views/'
  _treef '     ' new "$GI_XML" 'promo_banner.xml'
  sleep 0.28
  _log INFO "$EPID" echo.push.module "synced" "module=website_promo" "new=2" "changed=0"
  printf '\n'
  _log INFO "$EPID" echo.push "push complete" "modules=2" "files=7"
}

# _treef PREFIX OP ICON NAME — one file row of the change tree:
#   dim connector · op glyph (kind-colored) · dim file icon · fg name.
_treef() {
  local prefix="$1" op="$2" icon="$3" name="$4" glyph gc
  case "$op" in
    new)     glyph='+'; gc="$SUCCESS";;
    changed) glyph='~'; gc="$WARNING";;
    deleted) glyph='−'; gc="$ERROR";;
  esac
  printf '%s%s %s %s\n' "$(_c "$DIM" "$prefix")" "$(_c "$gc" "$glyph")" \
    "$(_c "$DIM" "$icon")" "$(_c "$FG" "$name")"
}

# _treed PREFIX NAME — a directory node, all dim with a folder glyph.
_treed() { printf '%s\n' "$(_c "$DIM" "$1$GI_FOLDER $2")"; }

# db-pull: clone a remote database into the local stack (Unit 85). The remote
# side is read-only (one pg_dump streamed over SSH into ./backups/); the dump
# is then restored locally under a distinct name and — a prod source, by
# default — neutralized. The db column shows the local target name throughout.
db-pull() {
  local DB='muutrade_prod_prod'
  _log INFO  "$EPID" echo.db-pull.remote  "target resolved" "host=erp-prod" "path=/srv/odoo/muutrade"; sleep 0.28
  _log INFO  "$EPID" echo.db-pull         "pulling database" "target=prod" "source=muutrade_prod" "stage=prod"; sleep 0.28
  _log INFO  "$EPID" echo.db-pull.dump    "streaming remote dump" "file=muutrade_prod_prod_20260708-173204.dump"; sleep 0.30
  _log DEBUG "$EPID" echo.db-pull.dump    "pulled 12.4 MB"; sleep 0.20
  _log DEBUG "$EPID" echo.db-pull.dump    "pulled 57.8 MB"; sleep 0.20
  _log DEBUG "$EPID" echo.db-pull.dump    "pulled 103.2 MB"; sleep 0.20
  _log INFO  "$EPID" echo.db-pull.dump    "dump complete" "size=124.6 MB"; sleep 0.26
  _log INFO  "$EPID" echo.db-pull.restore "creating database"; sleep 0.26
  _log INFO  "$EPID" echo.db-pull.restore "restoring data" "file=muutrade_prod_prod_20260708-173204.dump"; sleep 0.26
  _log DEBUG "$EPID" echo.db-pull.restore 'creating TABLE "public"."res_partner"';            sleep 0.15
  _log DEBUG "$EPID" echo.db-pull.restore 'restoring data for table "public"."account_move"'; sleep 0.15
  _log DEBUG "$EPID" echo.db-pull.restore 'creating INDEX "public"."sale_order_pkey"';         sleep 0.15
  _log INFO  "$EPID" echo.db-pull.restore "neutralizing"; sleep 0.28
  printf '\n'
  _log INFO  "$EPID" echo.db-pull "pull complete" "db=muutrade_prod_prod" "size=124.6 MB" "neutralized=true"
  printf '  %s %s\n' "$(_c "$SUCCESS" '→')" "$(_c "$FG" 'db-use muutrade_prod_prod')"
}

# compare --all: whole-module sync-status table vs the container copy (Unit 86).
# Each file is changed / added / missing (equal is counted, not listed); the
# table closes with a verdict, and on a TTY the differing files feed an
# interactive drill-down into each one's diff (the Unit 80 renderer).
compare() {
  local w=26
  printf '\n  %s  %s\n' "$(_cb "$ACCENT" "$(printf '%-26s' file)")" "$(_cb "$ACCENT" status)"
  _cmprow "$w" 'models/sale_order.py'       changed
  _cmprow "$w" 'views/sale_order_views.xml' changed
  _cmprow "$w" 'report/sale_report.py'       added
  _cmprow "$w" 'i18n/es_MX.po'               missing
  printf '\n'
  _log INFO "$EPID" echo.compare "module compared" "module=sale_extra" "from=docker" \
    "changed=2" "added=1" "missing=1" "equal=17"
  sleep 0.7

  # interactive drill-down: pick a differing file …
  printf '\n  %s\n' "$(_cb "$WARNING" 'Changed files in sale_extra')"
  printf '  %s %s\n' "$(_c "$ACCENT" '❯')" "$(_c "$FG"  'models/sale_order.py')"
  printf '    %s\n'  "$(_c "$DIM" 'views/sale_order_views.xml')"
  printf '    %s\n'  "$(_c "$DIM" 'report/sale_report.py')"
  printf '    %s\n'  "$(_c "$DIM" 'i18n/es_MX.po')"
  printf '\n'; sleep 1.0

  # … and see its unified diff (internal render, container vs local).
  _c "$FAINT" '--- docker/sale_extra/models/sale_order.py'; printf '\n'
  _c "$FAINT" '+++ local/sale_extra/models/sale_order.py';  printf '\n'
  _c "$INFO"  '@@ -18,7 +18,7 @@ class SaleOrder(models.Model)'; printf '\n'
  _c "$FG"    '         @api.depends("order_line.price_total")'; printf '\n'
  _c "$FG"    '         def _compute_tax(self):'; printf '\n'
  _c "$ERROR" '-            rate = 0.16'; printf '\n'
  _c "$SUCCESS" '+            rate = self.company_id.account_tax_rate'; printf '\n'
  _c "$FG"    '             self.amount_tax = self.amount_untaxed * rate'; printf '\n'
}

# _cmprow WIDTH REL STATUS — one status row: fg path padded to WIDTH, then the
# status colored (changed=warn, added=info, missing=err).
_cmprow() {
  local w="$1" rel="$2" st="$3" sc
  case "$st" in
    changed) sc="$WARNING";; added) sc="$INFO";; missing) sc="$ERROR";;
  esac
  printf '  %s  %s\n' "$(_c "$FG" "$(printf "%-${w}s" "$rel")")" "$(_c "$sc" "$st")"
}

# logview: interactive alt-screen browser over the per-command log history
# (Unit 82). A stage-tinted `│` bar frames every line, matching the help pager.
# The sim plays three beats: the run list → open the top run (enter) → live
# text filter ("translation"). Mirrors internal/repl/logview.go View().
logview() {
  local bar; bar="$(_c "$SUCCESS" '│ ')"

  # 1) run list — time · cmd · status · line count · db. Cursor sits on the
  #    failed `test` run (the one worth opening).
  printf '%s%s%s\n' "$bar" "$(_c "$DIM" 'logview — 6 runs')" "$(_c "$FAINT" '  (7d retention)')"
  printf '%s%s%s\n'  "$bar" "$(_c "$FAINT" 'filter › ')" "$(_c "$FAINT" 'type to filter…')"
  printf '%s\n' "$bar"
  _lvrun "$bar" 0 '17:41:12' 'update sale --i18n'              ok  9  my_shop
  _lvrun "$bar" 0 '17:39:50' 'deploy'                          ok  14 my_shop
  _lvrun "$bar" 0 '17:38:02' 'db-pull --from prod --filestore' ok  22 muutrade_prod_prod
  _lvrun "$bar" 1 '17:35:20' 'test sale --tags=/sale'          err 31 my_shop
  _lvrun "$bar" 0 '17:31:07' 'compare sale_extra --all'        ok  6  my_shop
  _lvrun "$bar" 0 '17:28:44' 'db-backup --with-filestore'      ok  8  my_shop
  printf '%s\n' "$bar"
  printf '%s%s\n' "$bar" "$(_c "$FAINT" '↑↓ move · enter open · type filter · esc close · ctrl+x quit')"
  sleep 2.4

  # 2) open the failed run (enter): stored lines colored as they ran, with a
  #    line cursor (❯). An ERROR entry carries an unleveled traceback under it.
  clear
  _lvhead2 "$bar" 'test sale --tags=/sale — 17:35:20 · err · my_shop' '' all
  _lvd 0 0 INFO  "$OPID" odoo.modules.loading "loading 1 modules..."
  _lvd 0 0 INFO  "$OPID" odoo.tests.common    "Starting TestSaleOrder.test_tax_rounding"
  _lvd 1 0 ERROR "$OPID" odoo.tests.common    "FAIL: TestSaleOrder.test_tax_rounding"
  _lvraw 0 0 'Traceback (most recent call last):'
  _lvraw 0 0 '  File "/odoo/addons/sale/tests/test_sale.py", line 88, in test_tax_rounding'
  _lvraw 0 0 '    self.assertEqual(order.amount_tax, 16.00)'
  _lvraw 0 0 'AssertionError: 15.99 != 16.00'
  _lvd 0 0 INFO  "$OPID" odoo.tests.result   "1 failed, 0 error(s) of 42 tests"
  printf '%s\n' "$bar"
  printf '%s%s\n' "$bar" "$(_c "$FAINT" '↑↓ move · space select · tab level · type filter · ctrl+o copy all · esc back · ctrl+x quit')"
  sleep 2.2

  # 3) space marks the BLOCK under the cursor — the ERROR entry plus its
  #    unleveled traceback lines, tied together with a ✓ gutter.
  clear
  _lvhead2 "$bar" 'test sale --tags=/sale — 17:35:20 · err · my_shop' '' all
  _lvd 0 0 INFO  "$OPID" odoo.modules.loading "loading 1 modules..."
  _lvd 0 0 INFO  "$OPID" odoo.tests.common    "Starting TestSaleOrder.test_tax_rounding"
  _lvd 1 1 ERROR "$OPID" odoo.tests.common    "FAIL: TestSaleOrder.test_tax_rounding"
  _lvraw 0 1 'Traceback (most recent call last):'
  _lvraw 0 1 '  File "/odoo/addons/sale/tests/test_sale.py", line 88, in test_tax_rounding'
  _lvraw 0 1 '    self.assertEqual(order.amount_tax, 16.00)'
  _lvraw 0 1 'AssertionError: 15.99 != 16.00'
  _lvd 0 0 INFO  "$OPID" odoo.tests.result   "1 failed, 0 error(s) of 42 tests"
  printf '%s\n' "$bar"
  printf '%s%s\n' "$bar" "$(_c "$FAINT" '↑↓ move · space select · ctrl+o copy selection · esc clear · ctrl+x quit')"
  sleep 2.2

  # 4) ctrl+o copies exactly the marked block; the browser closes.
  clear
  _log INFO "$EPID" echo.logview "run viewed" "run=test sale --tags=/sale" "lines=5/31" "copied=5"
}

# _lvhead2 BAR HEAD TFILTER LEVEL — detail header for an arbitrary run.
_lvhead2() {
  local bar="$1" head="$2" tf="$3" lvl="$4" tfr
  printf '%s%s\n' "$bar" "$(_c "$DIM" "$head")"
  if [ -n "$tf" ]; then tfr="$(_c "$FG" "$tf")"; else tfr="$(_c "$FAINT" 'type to filter…')"; fi
  printf '%s%s%s%s%s\n' "$bar" "$(_c "$FAINT" 'filter › ')" "$tfr" \
    "$(_c "$FAINT" '   level › ')" "$(_c "$FAINT" "$lvl")"
  printf '%s\n' "$bar"
}

# _lvd CUR SEL LEVEL PID LOGGER MSG [fields...] — one detail log line with the
# 3-col gutter: ❯ cursor (CUR=1) and ✓ selection (SEL=1), both stage-green.
_lvd() {
  local cur sel; cur=' '; sel=' '
  [ "$1" = 1 ] && cur="$(_cb "$SUCCESS" '❯')"
  [ "$2" = 1 ] && sel="$(_c "$SUCCESS" '✓')"
  shift 2
  LOGPREFIX="${bar}${cur}${sel} "
  _log "$@"
  LOGPREFIX=""
}

# _lvraw CUR SEL TEXT — an unleveled continuation line (traceback): plain fg,
# no ts/pid/chip, same 3-col gutter.
_lvraw() {
  local cur sel; cur=' '; sel=' '
  [ "$1" = 1 ] && cur="$(_cb "$SUCCESS" '❯')"
  [ "$2" = 1 ] && sel="$(_c "$SUCCESS" '✓')"
  printf '%s%s%s %s\n' "$bar" "$cur" "$sel" "$(_c "$FG" "$3")"
}

# _lvrun BAR SELECTED TIME CMD STATUS NLINES DB — one run-list row.
_lvrun() {
  local bar="$1" sel="$2" tm="$3" cmd="$4" st="$5" n="$6" db="$7" cur stc
  if [ "$sel" = 1 ]; then cur="$(_c "$SUCCESS" '❯ ')"; else cur='  '; fi
  case "$st" in
    ok)  stc="$(_c "$DIM"   ok)";;
    err) stc="$(_c "$ERROR" err)";;
    *)   stc="$(_c "$FAINT" cancel)";;
  esac
  printf '%s%s%s  %s  %s  %s  %s\n' "$bar" "$cur" "$(_c "$DIM" "$tm")" \
    "$(_c "$FG" "$cmd")" "$stc" "$(_c "$FAINT" "$n lines")" "$(_c "$DIM" "$db")"
}

# _lvhead BAR TEXTFILTER LEVEL — the detail header + filter line.
_lvhead() {
  local bar="$1" tf="$2" lvl="$3" tfr
  printf '%s%s\n' "$bar" "$(_c "$DIM" 'update sale --i18n — 17:41:12 · ok · my_shop')"
  if [ -n "$tf" ]; then tfr="$(_c "$FG" "$tf")"; else tfr="$(_c "$FAINT" 'type to filter…')"; fi
  printf '%s%s%s%s%s\n' "$bar" "$(_c "$FAINT" 'filter › ')" "$tfr" \
    "$(_c "$FAINT" '   level › ')" "$(_c "$FAINT" "$lvl")"
  printf '%s\n' "$bar"
}

# watch: monitor mode (Unit 87). Follows the remote Odoo logs while idle; on a
# new commit it pauses the follow, runs the push+deploy cycle, then resumes.
watch() {
  local DB='muutrade'
  _log INFO "$EPID" echo.watch "watching branch" "branch=develop" "tip=a1b2c3d" "target=develop" "interval=10s"
  sleep 0.30
  _log INFO "$EPID" echo.watch.logs "following logs" "service=odoo"
  sleep 0.25
  # idle: streaming the remote server's logs live
  _log INFO "$OPID" werkzeug                        "127.0.0.1 - - [09/Jul/2026 17:02:11] \"GET /web HTTP/1.1\" 200 -"; sleep 0.30
  _log INFO "$OPID" odoo.addons.base.models.ir_cron "Job 'Mail: Email Queue Manager' done";                            sleep 0.35
  _log INFO "$OPID" werkzeug                        "127.0.0.1 - - [09/Jul/2026 17:02:14] \"POST /web/dataset/call_kw HTTP/1.1\" 200 -"; sleep 0.40
  # a commit lands on develop → pause, run the cycle
  _log INFO "$EPID" echo.watch.logs  "follow paused — running cycle"; sleep 0.30
  _log INFO "$EPID" echo.watch.cycle "commits detected" "commits=1" "modules=sale_extra"; sleep 0.28
  _log INFO "$EPID" echo.push.module "syncing" "module=sale_extra" "dest=/srv/odoo/muutrade/addons/sale_extra"; sleep 0.22
  _treed '└─ '   'models/'
  _treef '     ' changed "$GI_PY" 'sale_order.py'
  sleep 0.28
  _log INFO "$EPID" echo.push.module "synced" "module=sale_extra" "new=0" "changed=1"; sleep 0.25
  _log INFO "$EPID" echo.deploy.compose "stop";  sleep 0.22
  _log INFO "$EPID" echo.deploy.compose "up -d"; sleep 0.26
  _log INFO "$EPID" echo.deploy.odoo    "running module install/update"; sleep 0.24
  _log INFO "$OPID" odoo.modules.loading "Module sale_extra loaded in 0.38s"; sleep 0.22
  _log INFO "$OPID" odoo.modules.loading "Modules loaded.";                   sleep 0.22
  _log INFO "$EPID" echo.watch.cycle "cycle ok" "modules=1" "commits=1"; sleep 0.30
  # resume the follow on a fresh stream
  _log INFO "$EPID" echo.watch.logs "following logs" "service=odoo"; sleep 0.25
  _log INFO "$OPID" werkzeug "127.0.0.1 - - [09/Jul/2026 17:02:31] \"GET /web HTTP/1.1\" 200 -"
}

# _update_build: `update --build` remote- and source-aware (Unit 88). Asks
# WHERE (local / target → --from / link → --remote) and the module SOURCE
# (addons / installed) up front, lists the remote's modules in the picker, then
# composes the line. Reproduced as the huh selects + fuzzy picker + action.
_update_build() {
  # 1) Where to update? — a huh select; develop (a remote target) chosen.
  printf '\n  %s\n' "$(_cb "$FG" 'Where to update?')"
  printf '    %s\n'   "$(_c "$DIM" 'local')"
  printf '  %s %s\n'  "$(_c "$SUCCESS" '❯')" "$(_c "$FG"  'develop  (remote)')"
  printf '    %s\n'   "$(_c "$DIM" 'habitta_prod  (remote)')"
  printf '\n'; sleep 1.0

  # 2) Module source? — installed in the DB (so core modules are pickable).
  printf '  %s\n'    "$(_cb "$FG" 'Module source')"
  printf '    %s\n'  "$(_c "$DIM" 'project addons')"
  printf '  %s %s\n' "$(_c "$SUCCESS" '❯')" "$(_c "$FG" 'installed in the database (--installed)')"
  printf '\n'; sleep 1.0

  # 3) resolve the remote, list its installed modules
  local DB='muutrade'
  _log INFO "$EPID" echo.build "target resolved" "host=erp-develop" "path=/srv/odoo/muutrade"; sleep 0.30
  _log INFO "$EPID" echo.build "listing modules" "source=installed" "count=214"; sleep 0.30

  # 4) fuzzy picker over the REMOTE's installed modules — base is picked.
  printf '\n  %s\n' "$(_cb "$WARNING" 'Modules to update  (214)')"
  printf '  %s %s  %s\n' "$(_c "$ACCENT" '▸')" "$(_c "$SUCCESS" '☑')" "$(_c "$FG"  'base')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'web')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'mail')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'account')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" 'sale_extra')"
  printf '\n'; sleep 0.9

  # 5) flags — only --i18n / --level; --i18n toggled.
  printf '  %s\n' "$(_cb "$WARNING" 'Flags for update (Tab to toggle, Enter to confirm)')"
  printf '  %s %s  %s\n' "$(_c "$ACCENT" '▸')" "$(_c "$SUCCESS" '☑')" "$(_c "$FG"  '--i18n')"
  printf '    %s  %s\n'  "$(_c "$FAINT" '☐')"                         "$(_c "$DIM" '--level')"
  printf '\n'; sleep 0.8

  # 6) composed line + action — --from=develop baked, --installed NOT (explicit).
  printf '  %s %s\n' "$(_c "$DIM" 'Composed:')" \
    "$(_cb "$ACCENT" 'update') $(_c "$FG" 'base') $(_c "$INFO" '--from')$(_c "$DIM" '=develop') $(_c "$INFO" '--i18n')"
  printf '\n  %s\n'  "$(_cb "$FG" 'Apply?')"
  printf '  %s %s\n' "$(_c "$ACCENT" '❯')" "$(_cb "$FG" 'Run it now')"
  printf '    %s\n'  "$(_c "$DIM" 'Copy to clipboard')"
  printf '    %s\n'  "$(_c "$DIM" 'Cancel')"
}
