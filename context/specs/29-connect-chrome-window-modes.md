# Unit 29: connect-chrome-window-modes

## Goal

Stop `connect` from spawning a brand-new Chrome window (and a throwaway temp
profile) on every run. Reuse a persistent, Echo-dedicated Chrome instance and
open the session in a **new tab** by default; add `--new-window` to open it
in an **isolated incognito window** instead (its own cookie jar, so several
users can be impersonated at once).

## Design

### Persistent dedicated profile

A fixed profile dir, separate from the user's everyday Chrome so connect
never touches their personal cookies:

`~/.local/share/echo/connect-chrome` (override: `$ECHO_CONNECT_CHROME_PROFILE`).

> Driving the user's *normal* Chrome was considered and rejected: Odoo's
> `session_id` is HttpOnly, so it can only be injected over CDP, which needs
> Chrome launched with `--remote-debugging-port` — not the case for a daily
> browser. A dedicated profile is the only viable approach.

### Reuse vs launch

`ensureChrome` decides:

- Read the profile's `DevToolsActivePort` and probe `/json/version` on it.
  If an instance answers → **reuse** it (no new window).
- Otherwise remove the stale port file and **launch** Chrome on the profile
  with `--remote-debugging-port=0`, waiting for the freshly written port.

### Browser-level CDP

The cookie used to be set by hijacking whatever page the fresh instance
opened. Now we connect at the **browser** level (`/json/version` →
`webSocketDebuggerUrl`) and create our own target, so reuse never disturbs an
existing tab. `cdpClient.call` gains an optional flattened `sessionId`
(`Target.attachToTarget {flatten:true}`) so page commands (`Network.enable`,
`Network.setCookie`, `Page.navigate`) address the target we created.

### Window modes

- **default (tab):** `Target.createTarget {url:"about:blank"}` → a new tab in
  the shared default context. Cookie lands in the persistent profile's jar
  (one Odoo session at a time per profile).
- **`--new-window` (incognito):** `Target.createBrowserContext` (isolated jar)
  → `Target.createTarget {browserContextId, newWindow:true}`. Each invocation
  gets a fresh context, so multiple users can be live simultaneously. The
  context is created with `disposeOnDetach:false` so closing our debugging
  connection leaves the window open.

In both cases the working target starts on `about:blank`, the cookie is set,
then it navigates to `<base>/odoo` (so the first real request carries the
cookie). On a fresh launch the throwaway `about:blank` window Chrome popped is
closed (`Target.closeTarget`) once our target is ready.

### Flag plumbing & logging

`parseConnectArgs` returns `newWindow` (`--new-window`); `RunConnect` threads
it into both `landSessionCookie` call sites (reuse fast-path and fresh mint)
and tags the `opening chrome` log with `window=tab|incognito`. The projectless
path (`parseDirectArgs`) passes `--new-window` (and `--fresh`, previously
missing) through. Registered in `commandFlags["connect"]` and the help.

## Implementation

### `internal/cmd/connect_cdp.go` (rewritten)
- `landSessionCookie(ctx, cfg, baseURL, sid, newWindow bool)`.
- `connectChromeProfile`, `ensureChrome`, `runningDevToolsPort`,
  `launchPersistentChrome` (replaces `launchChrome`).
- `dialBrowserCDP` (replaces `dialCDP`); `cdpClient.call` takes `sessionID`.
- `listTargets` / `blankPageTargets` / `createWorkingTarget` /
  `closeTarget` / `setSessionCookieOn` / `navigateOn`.
- `preferHTTPS` unchanged.

### `internal/cmd/connect.go`
- `parseConnectArgs` → `newWindow`; `connectWindowMode`.
- `RunConnect` passes `newWindow` to both `landSessionCookie` calls; logs
  `window=`.

### `internal/cmd/connect_direct.go`
- `parseDirectArgs` passes `--new-window` and `--fresh` through.

### `internal/repl/` (`commands.go`, `repl.go`)
- `commandFlags["connect"]` += `--new-window`; help entry.

## Dependencies

None new (still `github.com/coder/websocket`).

## Verify when done

- [ ] First `connect` opens one Chrome window; a second `connect` opens a new
      **tab** in the same window — no second window, no temp profiles left in
      `$TMPDIR`.
- [ ] `connect <login> --new-window` opens a separate **incognito** window;
      its session is independent (impersonate two different users at once).
- [ ] Closing Echo's connect-chrome window and reconnecting relaunches it
      cleanly (stale `DevToolsActivePort` handled).
- [ ] Reuse never navigates/hijacks a tab the user already had open.
- [ ] `opening chrome` log shows `window=tab` / `window=incognito`.
- [ ] `--new-window` highlights as a known flag, Tab-completes, in help; the
      projectless `echo connect <name> --new-window` and `--fresh` both work.
- [ ] Table test covers `parseConnectArgs` for `--new-window`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.
