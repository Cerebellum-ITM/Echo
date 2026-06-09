# Unit 28: connect-session-cache

## Goal

Stop re-querying users and re-minting an Odoo session on every `connect`.
Cache the minted session locally and, on a repeat `connect <login>`, reuse
the cookie directly — validating it with one cheap HTTP probe and re-minting
only when it's stale or rejected. Make the interactive picker offer recently
used logins first so even `connect` (no login) can skip the `res.users`
query.

## Design

### What gets cached

One entry per **(target, login)**, stored locally because the browser is
always local. The cache holds exactly what's needed to re-land a cookie
without touching Odoo: `login`, `uid`, `sid` (the Odoo `session_id`),
`base_url` (already HTTPS-preferred at mint time), and `minted_at`.

Odoo sessions are files in the container's session store and live ~7 days
(GC by age); the `session_token` is recomputed per request from the user.
So a cached `sid` stays valid for the session lifetime unless the password
changes or the file is GC'd — which the probe detects.

### Storage

`~/.config/echo/connect-sessions/<key>.toml`, a `[sessions.<login>]` table.
`<key>` is `ProjectKey` (sha256) of the destination identity:

- local: `local:<project-root>:<db>`
- remote: `ssh:<ssh_host>:<remote_path>:<db>`

Distinct destinations never share a file. Storage lives in the **config
package** (`internal/config/connect_session.go`, new file — `config.go`
untouched) reusing `configRoot()` + `writeAtomic()`. Load is best-effort: a
missing/corrupt file yields an empty map, never an error.

### Validation (probe always)

Before reusing, `probeSession` does a `GET <base>/odoo` with
`Cookie: session_id=<sid>` and **no redirect-follow**. A logged-in Odoo
answers 2xx; an expired session redirects (303) to the login page. 2xx →
reuse; anything else (or a transport error) → re-mint. A 5-day TTL
(`connectSessionTTL`, below Odoo's 7-day GC) is a pre-filter so obviously
stale entries skip the probe and go straight to re-mint.

### Flow (RunConnect)

1. Resolve target + prod confirm (unchanged).
2. `parseConnectArgs` now also returns `fresh` (`--fresh`).
3. Load the target's cache.
4. `resolveConnectSelection` picks who to connect as with the fewest
   queries:
   - `connect <login>` + cache hit → reuse cached `uid`, **no user query**.
   - `connect <login>` + miss → query users, resolve login→uid.
   - interactive + cache non-empty → `pickRecentSessions` (recent logins +
     "↻ Fetch all users…"); only "fetch all" runs the query.
   - interactive + empty cache → original query + picker.
5. Fast path: if `!fresh` and the selection carries a non-expired cached
   session that **probes valid** → `landSessionCookie` and return
   `Reused: true`. No mint.
6. Otherwise mint, `preferHTTPS`, land, and **save** the session to the
   cache (best-effort).

### REPL surfacing + progress logging

`RunConnect` previously ran silently and the REPL printed a few plain
`info` lines at the end. Now `ConnectOpts.Log` (`ConnectLogger`) lets the
command emit Odoo-style progress events through the REPL's `emitOdooLog`,
so every step matches the rest of the CLI's log stream:

- `echo.connect: target resolved mode=… container=…`
- `echo.connect: querying users` / `N user(s) found` (only when a query
  actually runs)
- `echo.connect.cache: cache hit … / validating … / valid — reusing / past
  TTL — re-minting / invalid — re-minting`
- `echo.connect.mint: minting session … / session minted`
- `echo.connect: opening chrome url=…`
- closing summary `echo.connect: session minted|reused (cached) login=… uid=…
  mode=… mfa=bypassed`, then the usual `connect completed` from `finalize`.

`sub` is the logger suffix (`""`/`cache`/`mint`); the REPL adds the
`echo.connect` prefix, styles, and db. `ConnectResult.DBName` carries the
resolved db so the summary shows the right one for a remote target. A nil
`Log` is a no-op (non-REPL callers/tests don't supply one). `--fresh` is
registered in `commandFlags["connect"]` (highlight + Tab complete) and in
the help.

## Implementation

### `internal/config/connect_session.go` (new)
- `ConnectSession` struct (toml-tagged) + `connectSessionsFile`.
- `LoadConnectSessions(key) map[string]ConnectSession` (best-effort).
- `SaveConnectSession(key, ConnectSession)` — load, upsert by login,
  `writeAtomic`.

### `internal/cmd/connect_cache.go` (new)
- `connectCacheKey(opts, target)` — identity → `config.ProjectKey`.
- `connectSessionExpired(s)` — TTL check.
- `probeSession(ctx, base, sid)` — HTTP validity probe.
- `pickRecentSessions(cache, palette)` — recent-first picker +
  `fetchAllLabel` sentinel.

### `internal/cmd/connect.go`
- `ConnectResult.Reused`.
- `parseConnectArgs` returns `fresh`.
- `RunConnect` restructured around the cache (fast path + save).
- New `connectSelection` + `resolveConnectSelection`.

### `internal/repl/` (`commands.go`, `repl.go`)
- `commandFlags["connect"]` += `--fresh`; help entry; reused-vs-minted log.

## Dependencies

None new. Reuses `net/http`, `BurntSushi/toml`, the existing picker,
`config.ProjectKey`, `configRoot`, `writeAtomic`.

## Verify when done

- [ ] A second `connect <login>` reuses the cookie: no `res.users` query, no
      mint, log says `Session reused (cached)`, Chrome opens logged in.
- [ ] After the cached session is invalidated server-side (logout / GC), the
      next `connect <login>` probes, fails, and re-mints transparently.
- [ ] `connect <login> --fresh` always re-mints and refreshes the cache.
- [ ] Interactive `connect` with cached sessions lists recent logins first;
      "↻ Fetch all users…" falls back to the full query + picker.
- [ ] Local and remote targets (and different DBs) use separate cache files;
      a remote `web.base.url` over HTTPS probes/reuses correctly.
- [ ] `--fresh` highlights as a known flag and Tab-completes; appears in help.
- [ ] Every connect run narrates its steps in the Odoo log format
      (`echo.connect[.cache|.mint]`): a fresh run shows `querying users` → `N
      user(s) found` → `minting session` → `session minted`; a reuse shows
      `cache hit` → `validating` → `valid — reusing cookie`. No more silent
      gaps or plain `info` lines.
- [ ] Table tests: `parseConnectArgs` (`--fresh`), `connectCacheKey`
      distinctness/determinism, `connectSessionExpired`, and a config
      round-trip/upsert for the cache file.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.
