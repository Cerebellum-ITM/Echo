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

// landSessionCookie launches an isolated Chrome instance, sets the Odoo
// `session_id` cookie via CDP, and navigates it to `<baseURL>/odoo`. The
// browser is left running so the dev keeps the logged-in window.
func landSessionCookie(ctx context.Context, cfg *config.Config, baseURL, sid string) error {
	port, err := launchChrome(cfg)
	if err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	c, err := dialCDP(dialCtx, port)
	if err != nil {
		return err
	}
	defer c.close()

	if err := c.setSessionCookie(dialCtx, baseURL, sid); err != nil {
		return err
	}
	return c.navigate(dialCtx, baseURL+"/odoo")
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

// launchChrome starts Chrome with a throwaway profile and remote
// debugging on an OS-picked loopback port, then returns that port read
// from the profile's DevToolsActivePort file. The process is detached
// from ctx so it outlives the command (the dev keeps the window).
func launchChrome(cfg *config.Config) (int, error) {
	bin, err := chromeBinary(cfg)
	if err != nil {
		return 0, err
	}
	profile, err := os.MkdirTemp("", "echo-connect-*")
	if err != nil {
		return 0, fmt.Errorf("create temp profile: %w", err)
	}

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

	port, err := readDevToolsPort(filepath.Join(profile, "DevToolsActivePort"), 5*time.Second)
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
// WebSocket: it issues a handful of commands and matches replies by id.
type cdpClient struct {
	conn *websocket.Conn
	id   int
}

type cdpTarget struct {
	Type                 string `json:"type"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// dialCDP discovers the page target on the given DevTools port and opens
// a WebSocket to it.
func dialCDP(ctx context.Context, port int) (*cdpClient, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query devtools targets: %w", err)
	}
	defer resp.Body.Close()

	var targets []cdpTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, fmt.Errorf("decode devtools targets: %w", err)
	}
	var wsURL string
	for _, t := range targets {
		if t.Type == "page" && t.WebSocketDebuggerURL != "" {
			wsURL = t.WebSocketDebuggerURL
			break
		}
	}
	if wsURL == "" {
		return nil, fmt.Errorf("no page target exposed by chrome")
	}

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial devtools websocket: %w", err)
	}
	conn.SetReadLimit(8 << 20) // CDP replies can be large
	return &cdpClient{conn: conn}, nil
}

func (c *cdpClient) close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// call sends a CDP command and returns the raw `result` of the matching
// reply, skipping unrelated protocol events.
func (c *cdpClient) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	c.id++
	id := c.id
	payload, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
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
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID != id {
			continue // an event or another command's reply
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

// setSessionCookie installs the Odoo session cookie scoped to the host of
// baseURL. `secure` mirrors the scheme so the same call works for a local
// http Odoo and a remote https one.
func (c *cdpClient) setSessionCookie(ctx context.Context, baseURL, sid string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("parse base url %q: %w", baseURL, err)
	}
	if _, err := c.call(ctx, "Network.enable", nil); err != nil {
		return err
	}
	res, err := c.call(ctx, "Network.setCookie", map[string]any{
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

func (c *cdpClient) navigate(ctx context.Context, target string) error {
	_, err := c.call(ctx, "Page.navigate", map[string]any{"url": target})
	return err
}
