# Unit 18: Connect Command (CDP)

## Goal

Add a REPL command `connect [<login>] [--all] [--force]` that opens the
Odoo web client in a browser **already logged in as any user** of the
configured DB, **without typing their password, without opening any new
port, and without installing anything into Odoo**.

The flow has two halves that run on (potentially) two different machines:

1. **Mint** the session server-side (where the Odoo `data_dir` lives).
   Local Odoo → mint via local `compose exec`. Remote Odoo → mint via
   `ssh <host> '<compose> exec …'`. Reuses the existing `connect_mint.py`.
2. **Land the cookie** client-side, on the dev's own machine, by driving
   a Chrome/Chromium instance through the **Chrome DevTools Protocol
   (CDP)**: set the `session_id` cookie, then navigate to
   `<web.base.url>/odoo`.

CDP is the mechanism that makes this work where everything else failed:
it writes the cookie at the browser's network layer, so Odoo's
**HttpOnly** `session_id` cookie can be created/overwritten — something
a JavaScript bookmarklet cannot do. And because the cookie is injected
into a local browser, **no inbound port on the server is ever needed**
(this is what killed the previous helper-server design).

## What this rewrite replaces

The previous Unit 18 (the WIP that was reset off this branch) landed the
cookie with a local HTTP **helper server** on `:8003` that the browser
had to reach at the public host — requiring an open inbound port. Direct
mode needed 8003 open on the firewall; tunnel mode needed an SSH `-L`
forward and broke because Odoo rewrote the `/odoo` redirect to
`web.base.url`, dropping the `localhost`-scoped cookie.

**Removed by this rewrite:**

- `internal/cmd/connect_helper.go` (the `:8003` server, `helperConfig`,
  `openBrowser`, `runConnectHelper`, the port/TTL constants).
- `--tunnel` mode, `buildHelperConfig`, `docker.ContainerIP` /
  `internal/docker/inspect.go`, the `TunnelLocalOdooPort` machinery, and
  all `web.base.url`→`localhost` redirect rewriting.
- `ConnectResult` fields `HelperURL`, `Hit`, `BrowserErr`, `Tunnel`,
  `SSHCommand`, `SessionFile`.

**Reused unchanged:**

- `internal/cmd/scripts/connect_mint.py` — mints the session via
  `root.session_store.new()` + `_compute_session_token`.
- `internal/cmd/scripts/connect_list_users.py` — lists users.
- The selection UX (fuzzy picker / direct login), prod confirmation,
  `mintResult`/`userRow` decoding, registry/help wiring, `connectDBEnv`,
  `execPythonInOdoo`, `lastNonEmptyLine`.

## Core model

```
Echo (dev laptop)
  │
  ├─ 1. MINT  ── local Odoo:  <compose> exec -T <odoo> python3 - < connect_mint.py
  │           └ remote Odoo:  ssh <host> 'cd <remote_path> && <compose> exec -T <odoo> python3 -'
  │                              ↳ stdout JSON: { sid, login, uid, base_url, session_file }
  │
  └─ 2. LAND  ── launch Chrome with --remote-debugging-port + temp profile
                 CDP Network.setCookie  session_id=<sid>  (HttpOnly, domain from base_url)
                 CDP Page.navigate      <base_url>/odoo
                              ↳ Chrome window opens already logged in
```

The cookie injection is **always local** (the browser is always on the
dev's machine). Only the **mint location** varies (local compose vs SSH).

## Target DB

Always `cfg.DBName`, same rule as Unit 10. Multi-DB out of scope.

## Selection UX (unchanged from the scaffolding)

- `connect` (no args) → list active users, `runSingleFuzzyPicker` with
  title `"Select user to impersonate"`. Row: `<flag> <login>  <name>`,
  `<flag>` is space for active, `!` for inactive (shown with `--all`).
- `connect <login>` → skip the picker; no match → `no user with login
  "<login>"`.
- `connect --all` → include inactive users.
- Esc → `ErrCancelled` → `WARNING echo.connect.cancelled`.

## Prod confirmation (unchanged)

`maybeConfirmProd(opts, "connect")` — red `huh.Confirm` when `cfg.Stage
== "prod"`; `--force` bypasses.

## Local vs remote selection

A new per-project `[connect]` config section decides where the mint runs:

```toml
# <project>.toml
[connect]
ssh_host       = "deploy@erp.example.com"  # empty/absent → mint locally
remote_path    = "/opt/odoo/erp"           # dir on the server holding the compose file
remote_compose = "docker compose"          # optional; defaults to cfg.ComposeCmd
chrome_path    = ""                          # optional Chrome/Chromium override
```

Resolution rule in `RunConnect`:

- `ssh_host == ""` → **local mode**: existing `execPythonInOdoo`,
  passing `ECHO_DB_*` from the local `.env` (current behavior).
- `ssh_host != ""` → **remote mode**: run the script over SSH; DB creds
  come from the **remote** `.env`, read once over SSH.

If `ssh_host` is set without `remote_path`: `connect: [connect].remote_path
is required when ssh_host is set`.

### Config additions

- `config.Config` gains: `ConnectSSHHost`, `ConnectRemotePath`,
  `ConnectRemoteCompose`, `ConnectChromePath string`.
- `projectFile` gains a nested `Connect *connectFile` with toml keys
  `ssh_host`, `remote_path`, `remote_compose`, `chrome_path`.
- `Load` maps them through; all optional, all default empty.
- No new global config; no `init` form changes in v1 (dev edits the
  project TOML by hand — documented in verify). A future unit can add a
  `connect --config` form.

## Minting over SSH

New in `internal/cmd/connect.go`:

```go
// execPythonRemote runs the embedded script inside the Odoo container on
// a remote host over SSH:
//   ssh -o BatchMode=yes <host> 'cd <remote_path> && <compose> exec -T [-e K=V ...] <odoo> python3 -'
// The script is piped through ssh's stdin. Returns combined stdout.
func execPythonRemote(ctx context.Context, opts ConnectOpts, script []byte, env map[string]string) ([]byte, error)
```

- Remote command string: `cd <remote_path> && <remote_compose> exec -T
  <-e K=V …> <odoo> python3 -`; each `-e K=V` shell-quoted;
  `remote_compose` defaults to `cfg.ComposeCmd`.
- `ssh -o BatchMode=yes <host> <remote-cmd>` with `cmd.Stdin =
  bytes.NewReader(script)`. BatchMode → fail fast, never hang on a
  password prompt.
- Capture stderr for error context; `lastNonEmptyLine` extracts the JSON.

Dispatcher used by `mintConnectSession` / `listConnectUsers`:

```go
func execConnectScript(ctx, opts, script, env) ([]byte, error) {
    if opts.Cfg.ConnectSSHHost != "" {
        return execPythonRemote(ctx, opts, script, env)
    }
    return execPythonInOdoo(ctx, opts, script, env)
}
```

### Remote DB env

Add `connectDBEnvRemote(ctx, opts)`: `ssh -o BatchMode=yes <host> cat
<remote_path>/.env`, parse with the existing `env` parser, build the same
`ECHO_DB_*` map. `ECHO_DB_HOST` still comes from `cfg.DBContainer` (the
compose service name resolves on the remote docker network). If the
remote `.env` is unreadable, pass **no** `ECHO_DB_*` and let the
container's `ODOO_RC` resolve the DB (emit a `dim` note).

## Landing the cookie via CDP

New file `internal/cmd/connect_cdp.go`.

### Chrome discovery & launch

```go
func chromeBinary(cfg *config.Config) (string, error)
```

- `[connect].chrome_path` override first.
- macOS: `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
  then `…/Chromium.app/…`.
- Linux: `$PATH` lookup of `google-chrome`, `google-chrome-stable`,
  `chromium`, `chromium-browser`.
- Windows: standard `Program Files` paths.
- None → `connect: no Chrome/Chromium found — set [connect].chrome_path`.

Launch isolated so the dev's normal profile is untouched:

```
<chrome> --remote-debugging-port=0 \
         --user-data-dir=<tmp> \
         --no-first-run --no-default-browser-check \
         about:blank
```

- Port 0 → OS picks a free loopback port; read it from
  `<tmp>/DevToolsActivePort` (line 1), polling with a ~5s ceiling.
- `<tmp>` = `os.MkdirTemp("", "echo-connect-*")`. Chrome is **not** killed
  on command return (dev keeps the window), so the temp profile outlives
  the command and is left for the OS tmp reaper.

### CDP client

Minimal CDP over a WebSocket — no heavyweight dependency.

- **New dependency:** `github.com/coder/websocket` — the one module Unit
  18 adds. (Over `chromedp`, which pulls the full `cdproto` tree and a
  process manager we don't need; we issue exactly three commands.)
- Discover the page target: `GET http://127.0.0.1:<port>/json` → first
  `"type":"page"` target → its `webSocketDebuggerUrl`.
- Over the WS, each command `{"id":N,"method":…,"params":…}`, await the
  matching `id`:
  1. `Network.enable`.
  2. `Network.setCookie`:
     ```json
     { "name":"session_id", "value":"<sid>", "domain":"<host-of-base_url>",
       "path":"/", "httpOnly":true, "secure":<https?>, "sameSite":"Lax" }
     ```
     Assert `result.success == true`.
  3. `Page.navigate` `{"url":"<base_url>/odoo"}`.
- Close the WS (leave Chrome running).

```go
type cdpClient struct { /* ws conn, id counter */ }
func dialCDP(ctx context.Context, devtoolsPort int) (*cdpClient, error)
func (c *cdpClient) setSessionCookie(ctx context.Context, baseURL, sid string) error
func (c *cdpClient) navigate(ctx context.Context, url string) error
func (c *cdpClient) close() error
```

### Cookie scope detail

`domain` = hostname parsed from `base_url` (no port, no scheme). `secure`
mirrors the scheme: `https` → `secure:true`; `http://localhost:8069` →
`secure:false`. This single switch makes the same path serve local and
remote.

## `RunConnect` (rewritten)

```go
func RunConnect(ctx context.Context, opts ConnectOpts) (ConnectResult, error) {
    // 1. requireOdooConfig + maybeConfirmProd("connect")
    // 2. login, includeInactive := parseConnectArgs(args)   (drop tunnel)
    // 3. users := listConnectUsers(...)        (execConnectScript)
    // 4. target := pickConnectUser(...)
    // 5. minted := mintConnectSession(...)     (execConnectScript)
    // 6. base := trim(minted.BaseURL); if "" → error (no fallback)
    // 7. port := launchChrome(ctx, opts.Cfg)
    // 8. c := dialCDP(ctx, port); c.setSessionCookie(base, sid); c.navigate(base+"/odoo"); c.close()
    // 9. return ConnectResult{Login, UID, BaseURL: base, Remote: ssh != ""}
}
```

`ConnectResult` shrinks to:

```go
type ConnectResult struct {
    Login   string
    UID     int
    BaseURL string
    Remote  bool   // true when minted over SSH (log line only)
}
```

If `base == ""`: `web.base.url is empty — set it in Odoo (Settings →
Technical → Parameters → System Parameters) before using connect`.

## REPL handler (`internal/repl/repl.go`)

```go
func (sess *session) runConnect(ctx context.Context, args []string) {
    sess.startLog("connect", args)
    res, err := cmd.RunConnect(ctx, cmd.ConnectOpts{Cfg, Root, Args, Palette})
    switch {
    case errors.Is(err, cmd.ErrCancelled), errors.Is(err, huh.ErrUserAborted):
        sess.finalize("connect", 0, 0, err); return
    case err != nil:
        sess.connectFailureLog(err); return
    }
    where := "local"; if res.Remote { where = "remote (ssh)" }
    sess.print(Line{Kind:"info", Text: fmt.Sprintf("Session minted for %q (uid=%d) — %s", res.Login, res.UID, where)})
    sess.print(Line{Kind:"info", Text: "Opening Chrome at " + res.BaseURL + "/odoo (logged in)"})
    sess.finalize("connect", 0, 0, nil)
}
```

No streaming, no `runStats` — same family as `shell`/`bash`. Keep the
`connectFailureLog` helper from the scaffolding.

### Output

```
INFO echo.connect.start: connect
INFO echo.connect: Session minted for "demo" (uid=6) — remote (ssh)
INFO echo.connect: Opening Chrome at https://erp.example.com/odoo (logged in)
INFO echo.connect: connect completed
```

Empty base url / no chrome / ssh failure → `ERROR echo.connect.error: …`.

## MFA note

Minting bypasses `Session.authenticate()`, so 2FA is skipped — intended
for dev. The prod-confirmation guard (`--force`) is the only friction.

## Security notes

- The minted session is a real, valid Odoo session, written to the server
  `data_dir` and injected only into the dev's local browser. Nothing is
  exposed on a network port.
- SSH uses `-o BatchMode=yes`; Echo never handles the SSH key/password —
  it defers to the dev's SSH config/agent.
- The temp Chrome profile isolates the impersonation session.

## Dependencies

- **New:** `github.com/coder/websocket` (minimal CDP transport).
- stdlib: `net/http` (CDP `/json`), `os/exec` (ssh, chrome),
  `encoding/json`, `net/url`.

## Implementation order

1. Config: `[connect]` fields + `Load` wiring + config test.
2. Remote mint: `execPythonRemote`, `execConnectScript`,
   `connectDBEnvRemote`. Keep the local path intact.
3. CDP: `connect_cdp.go` (chrome discovery, launch, DevToolsActivePort,
   minimal CDP client). Add `coder/websocket` to `go.mod`.
4. Rewrite `RunConnect`; shrink `ConnectResult`; drop `--tunnel`,
   `buildHelperConfig`, `connect_helper.go`, `docker/inspect.go`.
5. REPL handler + help summary (no Registry change; drop `--tunnel` from
   help text).
6. Build, vet, `go test ./internal/...`.

## Verify when done

- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
- [ ] **Local Odoo**: `connect` picks a user → new Chrome window opens
      logged in at `http://localhost:<port>/odoo` — no login, no MFA.
- [ ] `connect <login>` skips the picker; `connect no_existe` →
      `ERROR echo.connect.error: … no user with login "no_existe"`.
- [ ] `connect --all` includes inactive users (marked `!`).
- [ ] **Remote Odoo** (`[connect].ssh_host` + `remote_path` set): mints
      over SSH, opens local Chrome logged in at public `https://…/odoo`.
      BatchMode fails fast when the SSH key is missing.
- [ ] The dev's normal Chrome profile is untouched (temp profile window).
- [ ] `Network.setCookie` reply asserts `success: true`; missing Chrome →
      error with the `chrome_path` hint.
- [ ] `cfg.Stage == "prod"` → red `huh.Confirm`; `--force` bypasses.
- [ ] Empty `web.base.url` → abort before launching Chrome.
- [ ] No inbound port is ever opened (verify with `lsof -i`: only Chrome's
      loopback DevTools port and the outbound SSH connection exist).
