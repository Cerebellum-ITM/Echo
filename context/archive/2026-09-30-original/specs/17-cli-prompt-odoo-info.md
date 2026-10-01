# Unit 17: cli-prompt-odoo-info

## Goal

Replace the current REPL prompt (`<project>-<id> [stage/version.0]:~$`)
with an Odoo-aware, configurable prompt that surfaces the **compose
project name**, **DB name**, **stage** (colored), and **live container
health** for the Odoo and Postgres containers. Segments are toggled
through `~/.config/echo/global.toml`; expensive segments (health) read
through a TTL cache so prompt render latency stays imperceptible.

Final shape with all default segments enabled:

```
 echo-odoo17_dev [17.0 · echo_dev]  dev    :~$
```

Where ` ` is `nf-dev-docker` (Odoo container health) and ` ` is
`nf-dev-postgresql` (DB container health), each colored per state.

## Design

### Segment composition

The prompt is built from an ordered list of segments declared in
`global.toml`:

```toml
[prompt]
segments  = ["name", "version_db", "stage", "health"]
name_max  = 18           # max chars of the compose project name
health_ttl = "5s"        # TTL of the docker inspect cache
```

Unknown segments are ignored with a single `warn` line at startup;
duplicates are deduped preserving first occurrence. Missing `[prompt]`
table falls back to the default `segments` and `name_max=18`,
`health_ttl=5s`.

Each segment renders to a `string` (already styled). The final prompt
joins them with a single space, prefixed by the existing logo icon
(`banner.LogoIcon(cfg.Logo)`) and suffixed by `:~$ ` rendered exactly
as today (`info` tilde, `fg` dollar). The `~` is **not** replaced by a
path — keep current behavior.

### Segment specs

| Segment       | Style token(s)                  | Render                                                          |
|---------------|---------------------------------|-----------------------------------------------------------------|
| `name`        | `accent` bold                   | `echo-<truncated-compose-project>`                              |
| `version_db`  | `Bracket` (existing) + `dim`    | `[<odoo_version>.0 · <db_name>]`                                |
| `stage`       | stage-color bold                | bare stage word: `dev` / `staging` / `prod`                     |
| `health`      | per-state                       | two Nerd Font glyphs separated by a single space                |

Stage color reuses the existing `theme.StageColor(stage)` mapping
(`dev`→`success`, `staging`→`warning`, `prod`→`error`). Bracket style
already exists in `theme.Styles` — reuse `s.Bracket`.

Health glyphs (Nerd Fonts v3):

| State        | Glyph | Codepoint | Style token |
|--------------|-------|-----------|-------------|
| running      | `` / `` | U+E7B0 / U+E76E | `success` |
| stopped      | same       | same      | `dim`       |
| restarting   | same       | same      | `warning`   |
| unknown      | `?`        | —         | `faint`     |

`` is `nf-dev-docker` (Odoo container slot), `` is
`nf-dev-postgresql` (DB container slot). The choice is fixed in code,
not configurable in v1.

### Compose project name resolution

Resolved once per session at REPL start, stored on `session`:

1. `os.Getenv("COMPOSE_PROJECT_NAME")` if non-empty.
2. `cfg.ComposeProject` if set (new optional TOML field — see below).
3. Normalize `filepath.Base(cfg.ProjectPath)`:
   - lowercase
   - drop characters not in `[a-z0-9_-]`
   - collapse runs of `-`/`_` into one `_`
   - trim leading/trailing `_`-`-`

If the result is empty after normalization, fall back to the project
SHA-256 key truncated to 8 chars. The resolution does **not** call
`docker inspect`; it is pure config/path math.

### Truncation

`name_max` defaults to 18. If the resolved name is longer than
`name_max`, **right-cut with ellipsis**: take the first `name_max-1`
runes and append `…`. Runes, not bytes — preserve multi-byte safety.
Values `< 4` are clamped silently to 4 to keep the ellipsis useful.

### Stage color in the bracket

The bracket itself keeps the existing `s.Bracket` style — the
`version_db` segment is not stage-colored anymore. Stage color lives
only in the `stage` segment, which is now an independent chip after
the bracket. This is a deliberate change from current behavior (where
project+bracket both inherit stage color) — the colored stage word is
the new single source of "what env am I on".

### Health cache

A package-private struct in `internal/repl/`:

```go
type healthCache struct {
    mu        sync.Mutex
    odoo      containerHealth
    db        containerHealth
    expiresAt time.Time
}

type containerHealth struct {
    State string // "running" | "exited" | "restarting" | "unknown"
}
```

`healthCache.Read(ctx, cfg)` returns the two states. If `time.Now()` is
past `expiresAt`, refresh:

- Run `docker inspect -f '{{.State.Status}}' <odoo_container> <db_container>`
  with a **500 ms** context timeout.
- Parse two lines. Any failure (binary missing, container missing,
  timeout) → `unknown` for the affected slot.
- Reset `expiresAt = time.Now().Add(cfg.HealthTTL)`.

The cache is **synchronous** in v1 — `renderPrompt` blocks up to 500 ms
on cache misses. If profiling shows this is noticeable, move to async
refresh (kick off a goroutine, return the previous values) in a
follow-up. Do not pre-optimize.

The cache is invalidated explicitly after `up`, `down`, `restart`
finish (the cmd dispatcher calls `sess.health.Invalidate()` after
those handlers return). This makes the next prompt reflect the new
state immediately without waiting for the TTL.

### Telemetry / no-op for non-docker contexts

If `cfg.OdooContainer == ""` or `cfg.DBContainer == ""`, the `health`
segment is silently omitted from the rendered prompt, even if it
appears in `segments`. No warning — this just means the project hasn't
been `init`-ed yet.

## Implementation

### `internal/config/config.go`

Add to `Config`:

```go
ComposeProject string
PromptSegments []string
PromptNameMax  int
HealthTTL      time.Duration
```

Add to `globalFile`:

```go
ComposeCmd string `toml:"compose_cmd"`
Prompt     *promptFile `toml:"prompt"`

type promptFile struct {
    Segments  []string `toml:"segments"`
    NameMax   int      `toml:"name_max"`
    HealthTTL string   `toml:"health_ttl"` // parsed via time.ParseDuration
}
```

Add to `projectFile`:

```go
ComposeProject string `toml:"compose_project"`
```

In `Load`: parse the prompt block, apply defaults (`["name",
"version_db", "stage", "health"]`, `name_max=18`, `health_ttl=5s`).
Invalid `health_ttl` → log a `charmbracelet/log` warning and use 5s.

In `applyDefaults` (defaults.go): set the prompt defaults when the
block is absent or empty.

`SaveGlobal` writes the prompt block back. `SaveProject` writes
`compose_project` when non-empty.

### `internal/repl/prompt.go` (new file)

Pull the prompt-rendering logic out of `repl.go` into its own file.
Exposes:

```go
type promptBuilder struct {
    styles   theme.Styles
    cfg      *config.Config
    name     string         // resolved compose project name (truncated)
    stage    theme.Stage
    version  string
    health   *healthCache
    logoIcon string
}

func newPromptBuilder(sess *session) *promptBuilder
func (p *promptBuilder) Render(ctx context.Context) string
```

`Render` iterates `cfg.PromptSegments`, calls a `renderSegment(name)`
helper for each, joins with single spaces, and wraps with the icon
prefix and `:~$ ` suffix.

`renderSegment` is a `switch` on the segment name. Unknown segments
return `""` (already validated at config-load time, so this should
never happen at runtime).

### `internal/repl/repl.go`

- Replace `sess.id` usages tied to the prompt with the new compose
  project name. Keep `id` on the session for now if any other code
  reads it; remove only after a full grep confirms it's unused. The
  prompt no longer renders it.
- Add `health *healthCache` to `session`.
- In `Start`, call `resolveComposeProject(cfg)` once, store it as
  `sess.composeName`, and instantiate `healthCache` with TTL from
  `cfg`.
- Replace the body of `renderPrompt()` with a single call:
  `return sess.prompt.Render(ctx)`.
- After the `up`, `down`, `restart` handlers return, call
  `sess.health.Invalidate()`.

### `internal/repl/composename.go` (new file)

```go
func resolveComposeProject(cfg *config.Config) string
func normalizeProjectName(raw string) string
func truncateName(name string, max int) string
```

Pure functions, easily unit-testable.

### `internal/repl/health.go` (new file)

The `healthCache` struct and its `Read` / `Invalidate` methods. Uses
`exec.CommandContext` with a 500 ms timeout as described. Reads the
compose flavor (`cfg.ComposeCmd`) is **not** needed here — `docker
inspect` is `docker`, not `docker compose`.

### `internal/theme/styles.go`

Add a helper (if not already present):

```go
func StageColor(s theme.Styles, stage Stage) lipgloss.Style
```

Returns a bold style colored per stage. If it already exists under a
different name, reuse it — do not duplicate.

### Tests

- `composename_test.go` — table-driven for `normalizeProjectName` and
  `truncateName` (empty input, non-ASCII runes, all special chars,
  length boundary at `max`, `max < 4` clamp).
- `prompt_test.go` — render with each segment in isolation and the
  full default set; assert plain-text output (strip ANSI) matches
  expected strings. Mock `healthCache` with a stub that returns
  fixed states.
- `health_test.go` — fake `exec.CommandContext` via a test-only hook
  (function variable in the package) to assert TTL behavior and
  timeout fallback to `unknown`.

### `CHANGELOG.md`

Append to `[Unreleased]` under `Added`:

> Odoo-aware REPL prompt: shows compose project name, version, DB,
> colored stage chip, and live Odoo/DB container health (Nerd Font
> glyphs). Segments are configurable via the new `[prompt]` block in
> `global.toml`.

And under `Changed`:

> Prompt no longer renders the per-session id; project identity now
> comes from the docker-compose project name (overridable via
> `COMPOSE_PROJECT_NAME` env or `compose_project` in the per-project
> TOML).

## Dependencies

- none — `os/exec`, `sync`, `time`, and the existing `BurntSushi/toml`,
  `lipgloss`, and `charmbracelet/log` are sufficient.

## Verify when done

- [ ] Default prompt with all segments matches the documented shape on
      a working stack (icon + name + bracket + colored stage chip +
      two health glyphs + `:~$ `).
- [ ] Removing `health` from `segments` removes both glyphs and skips
      the docker inspect call entirely (verify with `strace`/`dtruss`
      or by stopping the docker daemon — prompt must still render).
- [ ] Stage color: `dev` green, `staging` amber, `prod` red, applied
      to the bare stage word only (not the bracket).
- [ ] Compose name resolution priority verified: env var overrides
      TOML override overrides path-derived name.
- [ ] Truncation cuts at `name_max-1` runes with `…`; `name_max < 4`
      clamped silently to 4; multi-byte runes not split mid-character.
- [ ] Health cache: first prompt after start blocks up to 500 ms; next
      prompts within TTL are instant (instrument with `time.Since`).
- [ ] After `up`/`down`/`restart`, the next prompt reflects the new
      container state without waiting for the TTL.
- [ ] If `OdooContainer` or `DBContainer` is empty, the health segment
      is omitted silently — no error, no `?` glyphs.
- [ ] Invalid `health_ttl` in `global.toml` logs a single warning and
      falls back to 5s; binary keeps running.
- [ ] `go test ./internal/repl/... ./internal/config/...` passes.
- [ ] `go vet ./...` clean, `go build ./...` succeeds.
- [ ] `CHANGELOG.md` `[Unreleased]` entries added in the same commit
      as the code.
- [ ] `context/progress-tracker.md` updated to reflect Unit 17
      completion (via `/spec-driven-dev update progress`).
