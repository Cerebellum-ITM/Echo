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
ssh_host    = "deploy@erp.example.com"  # empty/absent → mint locally
remote_path = "/opt/odoo/erp"           # the project dir on the server (as Echo knows it there)
chrome_path = ""                          # optional Chrome/Chromium override
```

Resolution rule in `RunConnect` (`resolveConnectTarget`):

- `ssh_host == ""` → **local mode**: container/db mapping from the local
  config; `execPythonInOdoo`; `ECHO_DB_*` from the local `.env`.
- `ssh_host != ""` → **remote mode**: the mapping is **read from the
  server's own Echo profile** over SSH — nothing is re-declared locally.
  The mint runs over SSH; DB creds come from the **remote** `.env`.

If `ssh_host` is set without `remote_path`: `connect: [connect].remote_path
is required when ssh_host is set`.

#### Reusing the server's Echo profile (remote mode)

The key idea: the server already runs Echo with the project mapped
(`odoo_container`, `db_container`, `db_name`, and the global
`compose_cmd`). Re-typing all that locally is duplication. Instead, Echo
locates the server's profile by hashing `remote_path` with the **same**
`projectKey` function it uses locally (`sha256(absPath)`), then reads it
over SSH:

```
ssh <host> cat ~/.config/echo/global.toml                    # → compose_cmd
ssh <host> cat ~/.config/echo/projects/<sha256(remote_path)>.toml   # → containers + db + stage
```

`config.ParseRemoteProfile(globalTOML, projectTOML)` decodes the pair
into a `RemoteProfile{ComposeCmd, OdooContainer, DBContainer, DBName,
Stage}`. `RunConnect` folds that (or the local config) into a single
`connectTarget` that the rest of the flow uses uniformly. The only local
input the remote path needs is `remote_path` itself (for the `cd` and the
profile hash); everything else is inherited. If the profile is missing,
`connect` fails with a hint to run `init` on the server. The prod-confirm
guard reads `stage` from the resolved target (the server's, in remote
mode).

### Config additions

- `config.Config` gains: `ConnectSSHHost`, `ConnectRemotePath`,
  `ConnectChromePath string`.
- `projectFile` gains a nested `Connect *connectFile` with toml keys
  `ssh_host`, `remote_path`, `chrome_path`.
- `config` exports `ProjectKey(absPath)` and
  `ParseRemoteProfile(globalTOML, projectTOML)` + the `RemoteProfile`
  type for the remote-profile fetch.
- `Load` maps them through; all optional, all default empty.
- No new global config; no `init` form changes in v1 (dev edits the
  project TOML by hand — documented in verify). A future unit can add a
  `connect --config` form, and storing `project_path` in the profile
  would let remote mode drop `remote_path` and list profiles directly.

## Minting over SSH

New in `internal/cmd/connect.go`:

All exec paths take the resolved `connectTarget` (compose cmd + odoo
container come from there, not from `cfg` — so a remote run uses the
server's values):

```go
// execPythonRemote runs the embedded script inside the Odoo container on
// a remote host over SSH:
//   ssh -o BatchMode=yes <host> 'cd <remote_path> && <target.composeCmd> exec -T [-e K=V ...] <target.odooContainer> python3 -'
func execPythonRemote(ctx context.Context, opts ConnectOpts, target connectTarget, script []byte, env map[string]string) ([]byte, error)
```

- Remote command string: `cd <remote_path> && <target.composeCmd> exec
  -T <-e K=V …> <target.odooContainer> python3 -`; `remote_path` and the
  container are shell-quoted.
- `ssh -o BatchMode=yes <host> <remote-cmd>` with `cmd.Stdin =
  bytes.NewReader(script)`. BatchMode → fail fast, never hang on a
  password prompt.
- Capture stderr for error context; `lastNonEmptyLine` extracts the JSON.

Dispatcher used by `mintConnectSession` / `listConnectUsers`:

```go
func execConnectScript(ctx, opts, target, script, env) ([]byte, error) {
    if target.remote {
        return execPythonRemote(ctx, opts, target, script, env)
    }
    return execPythonInOdoo(ctx, opts, target, script, env)
}
```

### Remote DB env

`connectDBEnvFor(ctx, opts, target)` reads the remote `.env` over SSH
(`ssh -o BatchMode=yes <host> cat <remote_path>/.env`), parses it with the
existing `env` parser, and builds the `ECHO_DB_*` map. `ECHO_DB_HOST` is
`target.dbContainer` (from the server's profile; resolves on the remote
docker network). If the remote `.env` is unreadable, it passes **no**
`ECHO_DB_*` and lets the container's `ODOO_RC` resolve the DB.

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
    // 1. target := resolveConnectTarget(...)   (local cfg OR remote profile via SSH)
    //    validate target.odooContainer/dbName; maybeConfirmConnectProd(target.stage)
    // 2. login, includeInactive := parseConnectArgs(args)
    // 3. users := listConnectUsers(..., target)        (execConnectScript)
    // 4. picked := pickConnectUser(...)
    // 5. minted := mintConnectSession(..., target)     (execConnectScript)
    // 6. base := trim(minted.BaseURL); if "" → error (no fallback)
    // 7. port := launchChrome(ctx, opts.Cfg)
    // 8. c := dialCDP(ctx, port); c.setSessionCookie(base, sid); c.navigate(base+"/odoo"); c.close()
    // 9. return ConnectResult{Login, UID, BaseURL: base, Remote: target.remote}
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

## Direct mode (projectless `echo connect`)

The REPL `connect` requires being inside a local project. For a laptop
that has **no local Odoo checkout** (the Odoo lives only on a server),
that is a dead end. The direct mode fixes it: `echo connect …` is a CLI
subcommand dispatched in `main.go` **before** the `project.FindRoot`
check, so it never needs a local `docker-compose.yml`.

```
echo connect [<name>] [<login>] [--add] [--all] [--force]
```

### Named targets in global config

Remote destinations are stored globally (`global.toml`), not per-project:

```toml
[connect_targets.erp]
ssh_host    = "erp-prod"        # an alias from ~/.ssh/config
remote_path = "/opt/odoo/erp"   # the project dir on that server
db_name     = "erp_prod"        # display only
```

- `config.ConnectTarget{Name, SSHHost, RemotePath, ChromePath, DBName}` +
  `config.LoadGlobal`, `SaveConnectTarget`, `sortedConnectTargets`.
- `RunDirectConnect` (in `internal/cmd/connect_direct.go`) resolves a
  target by name, by a picker of registered targets, or by registering a
  new one, then builds a minimal `config.Config` carrying only
  `ConnectSSHHost`/`ConnectRemotePath`/`ConnectChromePath` and calls the
  normal `RunConnect` (remote path). Output is one-shot to stdout; no REPL.

### Registering a target (SSH-config driven)

Echo never manages SSH — it only references aliases:

1. `sshConfigHosts()` parses `~/.ssh/config` for concrete `Host` aliases
   (wildcard/negated patterns skipped) → fuzzy picker.
2. `remoteEchoProjects(host)` reads the server's **own Echo profiles**
   over SSH (`for f in ~/.config/echo/projects/*.toml; do … cat … done`)
   and keeps those that recorded a `project_path`. No docker scanning —
   only Echo config. If none qualify → error telling the dev to run/update
   Echo on the server so profiles store their path.
3. Picker of `<db_name>  <project_path>` → `huh` input for the target
   name → `SaveConnectTarget`.

### `project_path` persistence + self-migration

To locate a server's projects by something other than the opaque
`sha256(path)` filename, the project profile now persists `project_path`
(`projectFile.ProjectPath`). Existing profiles predating the field
self-heal: on every normal startup `main.go` calls
`config.BackfillProjectPath(cfg)`, which rewrites a pathless profile once
with the known path. So a server's projects become discoverable just by
launching Echo there once — no new migration command, no forced re-init.

## Dependencies

- **New:** `github.com/coder/websocket` (minimal CDP transport).
- stdlib: `net/http` (CDP `/json`), `os/exec` (ssh, chrome),
  `encoding/json`, `net/url`.

## Implementation order

1. Config: `[connect]` fields (`ssh_host`, `remote_path`, `chrome_path`)
   + `Load` wiring + exported `ProjectKey` + `ParseRemoteProfile`/`RemoteProfile`
   + config tests.
2. Remote target + mint: `resolveConnectTarget`, `fetchRemoteProfile`,
   `execPythonRemote`, `execConnectScript`, `connectDBEnvFor`. Keep the
   local path intact.
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
