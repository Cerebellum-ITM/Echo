#!/usr/bin/env bash
# glyphs.sh — Nerd Font PUA glyphs, embedded as literal UTF-8 runes extracted
# from Echo's Go source (internal/banner/header.go, internal/repl/prompt.go).
# Literal bytes (not \u escapes) so they render under bash 3.2 too.

G_LOGO=''   # U+EA85
G_DOCKER=''   # U+F308
G_PG=''   # U+F703
G_TOOLS=''   # U+EB6D (cod-tools) sequence builder marker

# --- seti/md file-type glyphs for the push change tree (internal/repl/icons.go) ---
GI_FOLDER=''   # U+E613
GI_FILE=''   # U+E64E
GI_PY=''   # U+E606
GI_XML=''   # U+E619
GI_CSV=''   # U+E64A
GI_PO='󰗊'   # U+F05CA
GI_MD=''   # U+E609
GI_JSON=''   # U+E60B
GI_YML=''   # U+E6A8
