# demo — README GIFs (VHS)

The animated GIFs in the root `README.md` are recorded with
[charmbracelet/vhs](https://github.com/charmbracelet/vhs) from the `.tape`
scripts in `tapes/`, so anyone can regenerate them.

These GIFs are **simulations**. Echo wraps Docker, Odoo and SSH, so recording
the real binary would need a live stack and would leak private hostnames, DB
names and paths. Instead, the shell functions in `sim/echo-sim.sh` reproduce
Echo's exact on-screen styling with fully invented data — same tokyo palette,
same Odoo log-line layout, same Nerd Font glyphs, no network and nothing real.
Fidelity notes (and the source lines each value mirrors) live at the top of
`sim/echo-sim.sh`.

```
demo/
├── tapes/        # one .tape per GIF (+ _setup.tape with shared settings)
├── sim/          # echo-sim.sh dispatcher + glyphs.sh (PUA glyphs from source)
└── gifs/         # rendered output (committed)
```

## Requirements

```sh
brew install vhs ttyd ffmpeg     # vhs needs ttyd + ffmpeg
```

A [Nerd Font](https://www.nerdfonts.com/) must be installed (the tapes use
`JetBrainsMono Nerd Font Mono`) for the prompt/health glyphs to render.

## Regenerate

Run from the **repo root** (tape paths are repo-relative):

```sh
vhs demo/tapes/hero.tape              # one
for t in demo/tapes/*.tape; do        # all (skip the shared _setup.tape)
  case "$(basename "$t")" in _*) continue;; esac
  vhs "$t"
done
```

Output lands in `demo/gifs/`. To add a command: write a `<cmd>()` sim function
in `sim/echo-sim.sh`, add `tapes/<cmd>.tape` (copy an existing one), render, and
embed the GIF in the root `README.md`.

## Keeping the sims faithful

If Echo's styling changes, update the sim to match:

- **Palette** — `internal/theme/theme.go` (`var Tokyo`) → the `R;G;B` triples.
- **Log line / chips / field colors** — `internal/repl/logrender.go` and
  `logemit.go`.
- **Logger pastel** — `loggerColor` is `FNV-1a(name) % 8` over `loggerPalette`;
  `_logcolor` precomputes that map, so a renamed logger needs its slot recomputed.
- **Glyphs** — `glyphs.sh` holds the PUA runes from `internal/banner/header.go`
  (logo), `internal/repl/prompt.go` (docker/postgres health), and
  `internal/repl/icons.go` (the seti/md file-type glyphs the `push` change tree
  draws).
