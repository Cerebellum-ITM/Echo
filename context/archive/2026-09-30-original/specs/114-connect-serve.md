# Unit 114: `connect --serve` — the session as a local URL

## Goal

Let a headless agent (or any browser that is not the one `connect` drives)
navigate a dev instance **without ever receiving a credential**. `connect
--serve` mints the session exactly as today, then instead of landing the cookie
in Chrome it publishes the authenticated instance at a loopback URL and blocks.
Whoever gets that URL is logged in; whoever reads the transcript learns nothing.

## Why this shape and not `connect --json`

The obvious alternative — print `session_id` and let the caller do what it wants
with it — was prototyped and rejected in the session that produced this spec:

- The sid in stdout is the sid in the agent's context, in the transcript, in the
  session summary, in whatever log the caller keeps. `connect`'s whole premise is
  that the password never travels; handing out the cookie instead just moves the
  secret rather than removing it.
- Claude Code's own permission classifier blocked the extraction when it was
  attempted by hand. That is the correct instinct encoded in a tool, and the
  design should not need to be argued past it.
- The proxy is also strictly *less* work for the caller: navigate to a URL
  versus fetch a cache file, parse TOML, inject a cookie, and handle the
  HttpOnly case.

The secret stays inside the Echo process for the life of that process. That is
the property this unit buys.

## Behavior

```
connect [<login>] --serve [--port <N>] [--fresh] [--all] [--force] [--from <target>]
```

Everything before the browser step is unchanged: target resolution, the prod
gate, the user picker, the cache TTL and its HTTP probe, the mint, the HTTPS
upgrade of `web.base.url`, and the cache write. `--serve` only replaces
`landSessionCookie` as the terminal action.

**Output.** One line when the listener is up, then the run blocks:

```
2026-08-27 09:14:02,118 71204 INFO muutrade-dev echo.connect.serve: listening url=http://127.0.0.1:54123/x7f2a9c31 login=admin db=muutrade-dev
```

The URL is the whole contract — an agent reads that line and navigates. On
`Ctrl-C` (or a cancelled context) the listener closes and the run ends `0`:

```
2026-08-27 09:31:44,003 71204 INFO muutrade-dev echo.connect.serve: stopped requests=214
```

**Foreground, no lifecycle.** `--serve` blocks like `watch` does — the SIGINT
handler cancels a derived context, the server shuts down, `RunConnect` returns
`nil`. No pidfile, no `--serve-stop`, no daemon to orphan. A caller that wants it
in the background backgrounds the process itself.

**`--serve` and `--new-window` are mutually exclusive** — `ErrUsage`. One opens a
browser, the other deliberately opens nothing.

### The proxy contract

| Concern | Rule |
|---|---|
| Bind | `127.0.0.1` only, never `0.0.0.0`. Port `0` (ephemeral) by default; `--port <N>` pins it. The announced port is always the one the OS actually gave. |
| Path prefix | 8 hex chars from `crypto/rand`, fresh per run. Every request must start with `/<prefix>/`; the prefix is stripped before forwarding. |
| Miss | Anything outside the prefix answers `404` with an empty body — never `403`, which would confirm the port is a live Echo proxy to a local scanner. |
| Cookie in | The client's own `Cookie` header is **dropped**, then `session_id=<sid>` is set. A caller can neither displace the session nor smuggle one in. |
| Cookie out | `Set-Cookie` is stripped from every response, so the session never lands in the caller's cookie jar and cannot outlive the process. |
| `Location` | Absolute (`<base>/x`) and host-absolute (`/x`) redirects are rewritten to `/<prefix>/x`. Without this the first 303 walks the caller off the proxy and onto the login page. |
| Bodies | Never rewritten. Odoo's UI is relative-URL enough to navigate as-is; body rewriting would be a parser this unit does not need. |
| Streaming | No buffering — responses stream through. Report downloads and long polls must not be held in memory. |

**WebSockets come almost free.** `httputil.ReverseProxy` has handled protocol
upgrades since Go 1.12: a `101` response hands back a `ReadWriteCloser` and the
bidirectional copy is the standard library's problem, not ours. Without it
Odoo's bus never connects — no live notifications, no chatter, and a console
full of reconnect errors that read to an agent like real bugs. `/websocket`
needs no special case beyond not stripping `Connection`/`Upgrade`, which
`ReverseProxy` already gets right.

## Implementation

### `internal/cmd/connect_serve.go` (new)

```go
func serveConnectSession(ctx context.Context, opts ConnectOpts, base, sid, db string, port int) error
```

- `prefix`: 8 hex chars from `crypto/rand`.
- `httputil.ReverseProxy` with `Rewrite` (Go 1.20+ form, not the deprecated
  `Director`): set `r.Out.URL` to `base` + the path with `/<prefix>` trimmed,
  `r.Out.Host` to the upstream host, delete `Cookie`, set the injected one.
- `ModifyResponse`: delete `Set-Cookie`; rewrite `Location`.
- `http.Server` on `net.Listen("tcp", "127.0.0.1:"+port)`; read the real port
  back off `ln.Addr()` before announcing.
- A `sync/atomic` request counter for the `stopped` line.
- Block on `<-ctx.Done()`, then `srv.Shutdown` with a short timeout.

Rendering rule: the announce and stop lines go through `opts.log`, never
`fmt.Print` — the proxy is not exempt from the theme.

### `internal/cmd/connect.go`

- `parseConnectArgs` gains `serve bool, port int` (`--serve`, `--port <N>` /
  `--port=<N>`). Its current signature returns four unnamed bools; convert it to
  a `connectArgs` struct rather than growing the tuple to six.
- Validate `--serve` + `--new-window` → `ErrUsage`.
- Both terminal branches (cache hit at ~line 150 and fresh mint at ~line 190)
  choose `serveConnectSession` over `landSessionCookie`. The cache write still
  happens **before** serving, so a `Ctrl-C` an hour later does not cost the
  session.
- `ConnectResult` gains `ServeURL string` (empty when not serving), so the REPL
  can log without re-deriving it.

### `internal/repl/repl.go`

`runConnect` currently emits `session minted … mfa=bypassed` after `RunConnect`
returns. When serving, that return means the listener *stopped* — the line would
read as if the work were just beginning. Emit the summary before the blocking
call instead: pass it through `opts.Log` from inside `serveConnectSession`.

### `internal/repl/commands.go:52`

`"connect": {"--all", "--force", "--fresh", "--new-window", "--serve", "--port"}`.

### Docs

- `README.md` — the Connect section gains `--serve`, and the status table's
  Connect row mentions the local URL.
- `CHANGELOG.md` — `[Unreleased] → Added`, same commit as the code.
- `context/architecture.md` — add invariant 11: *no command may print a session
  id, or any other minted credential, to stdout, the log stream, or a `--json`
  payload; minted secrets live only inside the process that minted them.* This
  unit is the first place that rule is load-bearing.

## Dependencies

None. `net/http`, `net/http/httputil`, `crypto/rand`, `sync/atomic` — all
standard library.

## Verify when done

- [ ] `echo_cli connect develop admin --serve` prints one `listening url=…` line
      with an ephemeral port and an 8-hex prefix, then blocks.
- [ ] Navigating that URL + `/odoo` in a browser that has **never** seen the
      instance lands in the backend logged in as `admin` — no login form.
- [ ] `curl` to the same port on a wrong prefix returns `404` with an empty body.
- [ ] A response carries no `Set-Cookie`; after closing the proxy the caller's
      browser has no session for `127.0.0.1`.
- [ ] The `/odoo` redirect chain stays on `127.0.0.1` — no hop to the real host.
- [ ] `/websocket` reaches `101` and the browser console shows no bus reconnect
      loop.
- [ ] `Ctrl-C` prints `stopped requests=N` and exits `0`; the port is free
      immediately after.
- [ ] `--serve --new-window` exits `2` (usage) without minting anything.
- [ ] `--serve` on a `prod` target still hits the prod confirmation, and still
      refuses on a non-TTY without `--force`.
- [ ] No session id appears anywhere in the run's output at any log level.
- [ ] `go build ./...` and `go test ./...` pass; `go vet ./...` is clean.
