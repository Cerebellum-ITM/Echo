package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/pascualchavez/echo/internal/config"
)

// landSessionCookie sets the Odoo `session_id` cookie via CDP and opens
// `<baseURL>/odoo` already logged in. It reuses a persistent Echo-dedicated
// Chrome instance when one is already running — opening a new tab — instead
// of spawning a fresh window every time. When newWindow is true it opens an
// isolated incognito window (its own cookie jar) so several users can be
// impersonated at once; otherwise it adds a tab to the shared profile.
func landSessionCookie(ctx context.Context, cfg *config.Config, baseURL, sid string, newWindow bool) error {
	port, launched, err := ensureChrome(cfg)
	if err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	c, err := dialBrowserCDP(dialCtx, port)
	if err != nil {
		return err
	}
	defer c.close()

	// On a fresh launch Chrome pops an initial about:blank window; capture
	// it so we can tidy it up once our real tab/window is in place.
	var blanks []string
	if launched {
		blanks, _ = c.blankPageTargets(dialCtx)
	}

	sessionID, err := c.createWorkingTarget(dialCtx, newWindow)
	if err != nil {
		return err
	}
	if err := c.setSessionCookieOn(dialCtx, sessionID, baseURL, sid); err != nil {
		return err
	}
	if err := c.navigateOn(dialCtx, sessionID, baseURL+"/odoo"); err != nil {
		return err
	}

	if launched {
		for _, id := range blanks {
			_ = c.closeTarget(dialCtx, id)
		}
	}
	return nil
}

// connectChromeProfile is the persistent, Echo-dedicated Chrome profile
// directory used for `connect`. It is kept separate from the user's normal
// Chrome so impersonation never touches their everyday cookies. Overridable
// with $ECHO_CONNECT_CHROME_PROFILE.
func connectChromeProfile(cfg *config.Config) string {
	if p := strings.TrimSpace(os.Getenv("ECHO_CONNECT_CHROME_PROFILE")); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "echo", "connect-chrome")
	}
	return filepath.Join(os.TempDir(), "echo-connect-chrome")
}

// ensureChrome returns a DevTools port for the persistent Chrome profile,
// reusing a running instance when possible (launched=false) and launching a
// new one otherwise (launched=true).
func ensureChrome(cfg *config.Config) (port int, launched bool, err error) {
	profile := connectChromeProfile(cfg)
	if p, ok := runningDevToolsPort(profile); ok {
		return p, false, nil
	}
	p, err := launchPersistentChrome(cfg, profile)
	if err != nil {
		return 0, false, err
	}
	return p, true, nil
}

// runningDevToolsPort reads the profile's DevToolsActivePort and verifies an
// instance is actually listening there (the file can be left stale after a
// crash). Returns the live port, or ok=false when nothing is reachable.
func runningDevToolsPort(profile string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		return 0, false
	}
	line := data
	if i := indexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(line)))
	if err != nil {
		return 0, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	if err != nil {
		return 0, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	return port, true
}

// chromeBinary resolves a Chrome/Chromium executable: the config override
// first, then OS-specific defaults, then a $PATH lookup.
func chromeBinary(cfg *config.Config) (string, error) {
	if p := strings.TrimSpace(cfg.ConnectChromePath); p != "" {
		return p, nil
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
	default: // linux and friends
		candidates = nil
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Chrome/Chromium found — set [connect].chrome_path")
}

// launchPersistentChrome starts Chrome on the dedicated profile with remote
// debugging on an OS-picked loopback port, then returns that port read from
// the profile's DevToolsActivePort file. The process is detached so it
// outlives the command (the dev keeps the window). A stale port file is
// removed first so we wait for the freshly written one.
func launchPersistentChrome(cfg *config.Config, profile string) (int, error) {
	bin, err := chromeBinary(cfg)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return 0, fmt.Errorf("create chrome profile dir: %w", err)
	}
	portFile := filepath.Join(profile, "DevToolsActivePort")
	_ = os.Remove(portFile)

	cmd := exec.Command(bin,
		"--remote-debugging-port=0",
		"--user-data-dir="+profile,
		"--no-first-run",
		"--no-default-browser-check",
		"about:blank",
	)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("launch chrome: %w", err)
	}

	port, err := readDevToolsPort(portFile, 5*time.Second)
	if err != nil {
		return 0, fmt.Errorf("chrome did not report a debugging port: %w", err)
	}
	return port, nil
}

// readDevToolsPort polls Chrome's DevToolsActivePort file until it
// appears, then returns the port from its first line.
func readDevToolsPort(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			line := data
			if i := indexByte(data, '\n'); i >= 0 {
				line = data[:i]
			}
			if port, perr := strconv.Atoi(strings.TrimSpace(string(line))); perr == nil {
				return port, nil
			}
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timed out reading %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// cdpClient is a minimal Chrome DevTools Protocol client over a single
// browser-level WebSocket. Commands are matched to replies by id; page-level
// commands carry a flattened sessionId obtained via Target.attachToTarget.
type cdpClient struct {
	conn *websocket.Conn
	id   int
}

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// dialBrowserCDP opens the browser-level DevTools WebSocket (from
// /json/version) so we can create and drive targets ourselves rather than
// hijacking whatever page happens to be open.
func dialBrowserCDP(ctx context.Context, port int) (*cdpClient, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query devtools version: %w", err)
	}
	defer resp.Body.Close()

	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode devtools version: %w", err)
	}
	if info.WebSocketDebuggerURL == "" {
		return nil, fmt.Errorf("chrome exposed no browser websocket")
	}

	conn, _, err := websocket.Dial(ctx, info.WebSocketDebuggerURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial devtools websocket: %w", err)
	}
	conn.SetReadLimit(8 << 20) // CDP replies can be large
	return &cdpClient{conn: conn}, nil
}

func (c *cdpClient) close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// call sends a CDP command (optionally scoped to a flattened sessionId) and
// returns the raw `result` of the matching reply, skipping unrelated events.
func (c *cdpClient) call(ctx context.Context, sessionID, method string, params map[string]any) (json.RawMessage, error) {
	c.id++
	id := c.id
	msg := map[string]any{"id": id, "method": method, "params": params}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if err := c.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return nil, fmt.Errorf("%s write: %w", method, err)
	}
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s read: %w", method, err)
		}
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &reply); err != nil {
			continue
		}
		if reply.ID != id {
			continue // an event or another command's reply
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, reply.Error.Message)
		}
		return reply.Result, nil
	}
}

// listTargets returns every DevTools target Chrome currently exposes.
func (c *cdpClient) listTargets(ctx context.Context) ([]cdpTarget, error) {
	res, err := c.call(ctx, "", "Target.getTargets", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		TargetInfos []cdpTarget `json:"targetInfos"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("decode targets: %w", err)
	}
	return out.TargetInfos, nil
}

// blankPageTargets returns the ids of page targets sitting on about:blank —
// the throwaway window a fresh Chrome launch opens, which we close once our
// real tab is ready.
func (c *cdpClient) blankPageTargets(ctx context.Context) ([]string, error) {
	targets, err := c.listTargets(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, t := range targets {
		if t.Type == "page" && strings.HasPrefix(t.URL, "about:blank") {
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

// createWorkingTarget creates the page we'll drive: a new tab in the shared
// profile, or — when incognito is true — a new window in a fresh, isolated
// browser context. It returns the flattened sessionId to address that page.
func (c *cdpClient) createWorkingTarget(ctx context.Context, incognito bool) (string, error) {
	params := map[string]any{"url": "about:blank"}
	if incognito {
		res, err := c.call(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": false})
		if err != nil {
			return "", err
		}
		var bc struct {
			BrowserContextID string `json:"browserContextId"`
		}
		if err := json.Unmarshal(res, &bc); err != nil {
			return "", fmt.Errorf("decode browser context: %w", err)
		}
		params["browserContextId"] = bc.BrowserContextID
		params["newWindow"] = true
	}

	res, err := c.call(ctx, "", "Target.createTarget", params)
	if err != nil {
		return "", err
	}
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(res, &t); err != nil {
		return "", fmt.Errorf("decode created target: %w", err)
	}

	res, err = c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true})
	if err != nil {
		return "", err
	}
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &a); err != nil {
		return "", fmt.Errorf("decode attach: %w", err)
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("chrome returned an empty session id")
	}
	return a.SessionID, nil
}

func (c *cdpClient) closeTarget(ctx context.Context, targetID string) error {
	_, err := c.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID})
	return err
}

// setSessionCookieOn installs the Odoo session cookie on the given page
// session, scoped to the host of baseURL. `secure` mirrors the scheme so the
// same call works for a local http Odoo and a remote https one.
func (c *cdpClient) setSessionCookieOn(ctx context.Context, sessionID, baseURL, sid string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("parse base url %q: %w", baseURL, err)
	}
	if _, err := c.call(ctx, sessionID, "Network.enable", nil); err != nil {
		return err
	}
	res, err := c.call(ctx, sessionID, "Network.setCookie", map[string]any{
		"name":     "session_id",
		"value":    sid,
		"domain":   u.Hostname(),
		"path":     "/",
		"httpOnly": true,
		"secure":   u.Scheme == "https",
		"sameSite": "Lax",
	})
	if err != nil {
		return err
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return fmt.Errorf("decode setCookie result: %w", err)
	}
	if !out.Success {
		return fmt.Errorf("chrome refused the session cookie")
	}
	return nil
}

func (c *cdpClient) navigateOn(ctx context.Context, sessionID, target string) error {
	_, err := c.call(ctx, sessionID, "Page.navigate", map[string]any{"url": target})
	return err
}

// preferHTTPS upgrades an `http://` base URL to `https://` on the same
// host when that HTTPS endpoint is actually reachable, so connect lands
// on a secure session whenever the deployment supports it. An `https://`
// base is returned untouched, and a host with no working HTTPS (e.g. a
// local `http://localhost:8069`) keeps its original scheme.
func preferHTTPS(ctx context.Context, baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" {
		return baseURL
	}
	httpsURL := "https://" + u.Host + u.Path

	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, httpsURL, nil)
	if err != nil {
		return baseURL
	}
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return baseURL // no HTTPS / bad cert / unreachable → keep http
	}
	_ = resp.Body.Close()
	return httpsURL
}
