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
    echo.modinfo)                       printf '202;255;191';;  # mint
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
# Box geometry mirrors banner.Render: leftW=30, rightW=39, so the inner width
# between the corner glyphs is 1+30+1+1+1+39+1 = 74. Bars are built from that
# width so the corners line up with every row's │.
echo_header() {
  local title='─── Echo v0.13.0 '   # 17 visible cols
  printf '%s%s%s%s\n' "$(_c "$FAINT" '╭')" "$(_c "$ACCENT" "$title")" \
    "$(_c "$FAINT" "$(_dash 57)")" "$(_c "$FAINT" '╮')"
  _hrow ""                                              ""
  _hrow "$(_cb "$FG" '   Welcome back pascual!')"       "$(_cb "$WARNING" 'Tips for getting started')"
  _hrow ""                                              "$(_c "$FG" 'Run ')$(_c "$INFO" 'help')$(_c "$FG" ' to see all commands')"
  _hrow "$(_c "$ACCENT" '   ╔══════════╗')"             "$(_c "$FG" 'Type ')$(_c "$INFO" 'exit')$(_c "$FG" ' or Ctrl+D to quit')"
  _hrow "$(_c "$ACCENT" '   ║  ')$(_cb "$ACCENT" 'ECHO')$(_c "$ACCENT" '    ║')" "$(_c "$FAINT" "$(_dash 37)")"
  _hrow "$(_c "$ACCENT" '   ║   CLI    ║')"             "$(_cb "$WARNING" "What's new")"
  _hrow "$(_c "$ACCENT" '   ╚══════════╝')"             "$(_c "$DIM" '· commit-driven deploy over SSH')"
  _hrow ""                                              "$(_c "$DIM" '· i18n overwrite on update')"
  _hrow "$(_c "$DIM" '   tokyo · dev')"                 ""
  _hrow "$(_c "$DIM" '   ~/dev/my-shop')"               ""
  _hrow ""                                              ""
  printf '%s%s%s\n' "$(_c "$FAINT" '╰')" "$(_c "$FAINT" "$(_dash 74)")" "$(_c "$FAINT" '╯')"
}

# _dash N — N box-drawing dashes (─), built in a loop so it works on bash 3.2.
_dash() { local i s=''; for (( i = 0; i < $1; i++ )); do s+='─'; done; printf '%s' "$s"; }

# _hrow LEFT RIGHT — one header row, padded to the box columns (leftW=30,
# rightW=39). Padding is computed on the plain text, then colored, so ANSI
# escapes never throw off the alignment.
_hrow() {
  local lp rp bar
  bar="$(_c "$FAINT" '│')"
  lp="$(_pad "$1" 30)"; rp="$(_pad "$2" 39)"
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

deploy() {
  _log INFO "$EPID" echo.deploy.remote "target resolved" "host=erp-prod" "path=/srv/odoo/my-shop"
  sleep 0.30
  printf '\n  %s\n' "$(_cb "$WARNING" 'Select commits to deploy')"
  printf '  %s %s  %s\n' "$(_c "$ACCENT" '▸')" "$(_c "$SUCCESS" '☑')" "$(_c "$FG"  'a1b2c3d  [FIX] sale_extra: correct tax rounding')"
  printf '    %s  %s\n'   "$(_c "$SUCCESS" '☑')"                      "$(_c "$FG"  'e4f5a6b  [ADD] website_promo: launch banner')"
  printf '    %s  %s\n'   "$(_c "$FAINT" '☐')"                       "$(_c "$DIM" '0c1d2e3  [IMP] docs: update install notes')"
  printf '\n'; sleep 0.55
  _log INFO "$EPID" echo.deploy         "resolved" "commit=a1b2c3d" "module=sale_extra"    "via=subject"; sleep 0.15
  _log INFO "$EPID" echo.deploy         "resolved" "commit=e4f5a6b" "module=website_promo" "via=diff";    sleep 0.22
  _log INFO "$EPID" echo.deploy         "plan"     "update=sale_extra" "install=website_promo" "skipped=1"; sleep 0.28
  _log INFO "$EPID" echo.deploy.compose "stop";  sleep 0.22
  _log INFO "$EPID" echo.deploy.compose "up -d"; sleep 0.28
  _log INFO "$EPID" echo.deploy.odoo    "running module install/update"; sleep 0.30
  _log INFO "$OPID" odoo.modules.loading "loading 2 modules...";                  sleep 0.25
  _log INFO "$OPID" odoo.modules.loading "Module website_promo loaded in 0.61s";  sleep 0.22
  _log INFO "$OPID" odoo.modules.loading "Modules loaded.";                       sleep 0.20
  printf '\n'
  _log INFO "$EPID" echo.deploy "deploy complete" "update=1" "install=1" "skipped=1"
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
