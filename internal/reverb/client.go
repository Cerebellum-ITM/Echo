// Package reverb is the client for a Reverb daemon's Echo-facing API:
// the resolve endpoint that describes one environment (ssh host, paths,
// containers, DB credentials) and the compact environment listing.
//
// It exists so Echo can target a Reverb-managed environment by name with
// zero per-environment local configuration — the payload replaces, in a
// single HTTP call, the two SSH reads (`~/.config/echo/*.toml` and the
// project's `.env`) the classic remote path performs.
//
// The field names below mirror Reverb's frozen contract
// (`internal/api/resolve.go` there); they are stable once shipped and
// must not be renamed on a whim.
package reverb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sentinel errors for the API's stable machine codes. Callers branch on
// these to tell a transient state (not_ready) from a configuration
// problem (unauthorized / insufficient_scope) from a typo (not_found).
var (
	// ErrNotReady is 409 not_ready: the environment's create/fork job is
	// still running, so resolve would hand out half-populated credentials.
	// Retryable — see ResolveWithRetry.
	ErrNotReady = errors.New("environment is still provisioning")
	// ErrUnauthorized is 401: no token, or a token Reverb doesn't know.
	ErrUnauthorized = errors.New("reverb rejected the token")
	// ErrForbidden is 403: a valid token without the `echo` scope.
	ErrForbidden = errors.New("token lacks the required scope")
	// ErrNotFound is 404: unknown project or environment.
	ErrNotFound = errors.New("not found")
	// ErrNotConfigured means no [reverb] url/token in the global config.
	ErrNotConfigured = errors.New("reverb is not configured")
)

// Client talks to one Reverb daemon. Token is a secret (it grants the
// environment's DB password through resolve) and is never included in an
// error message or any value this package returns.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client for the given daemon. A missing url or token
// yields ErrNotConfigured — the caller is expected to turn that into a
// message naming the [reverb] section.
func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.TrimSpace(token) == "" {
		return nil, ErrNotConfigured
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("invalid reverb url: %w", err)
	}
	return &Client{
		BaseURL: baseURL,
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// Env is the resolve payload: everything Echo needs to target one
// environment. Mirrors Reverb's resolveView exactly.
type Env struct {
	// ID addresses every route past resolve (/environments/{id}/deploy,
	// /snapshots, /overlay, …). A client holding only <project>/<env> can
	// read but not act, so this is what unlocks the snapshot and lifecycle
	// surfaces.
	ID      int64  `json:"id"`
	Project string `json:"project"`
	Env     string `json:"env"`
	Stage   string `json:"stage"`
	URL     string `json:"url"`
	SSHHost string `json:"ssh_host"`
	// SSHPort is the daemon's public SSH port, omitted when it is 22. Echo
	// does NOT turn it into `ssh -p N`: the transport is configured through
	// the user's own ~/.ssh/config in both modes (see [reverb] ssh_host).
	// It is read purely to diagnose a host that cannot be dialled.
	SSHPort     int        `json:"ssh_port"`
	OdooVersion string     `json:"odoo_version"`
	Containers  Containers `json:"containers"`
	DB          DB         `json:"db"`
	Paths       Paths      `json:"paths"`
	Ports       Ports      `json:"ports"`
	Git         Git        `json:"git"`
	State       State      `json:"state"`
}

// Containers names the two containers Echo execs into.
type Containers struct {
	Odoo string `json:"odoo"`
	DB   string `json:"db"`
}

// DB carries the environment's database and its credentials. Password is
// a pointer because Reverb sends null for browser sessions — only
// echo/admin tokens get the real value.
type DB struct {
	Name     string  `json:"name"`
	User     string  `json:"user"`
	Password *string `json:"password"`
}

// Paths are the host-side directories. Addons is replaced wholesale by
// every Reverb deploy; Overlay is the directory Reverb never writes to
// (where Echo rsyncs uncommitted code, shadowing the git copy);
// ComposeDir holds a compose.yml with its own embedded project name.
type Paths struct {
	Addons     string `json:"addons"`
	Overlay    string `json:"overlay"`
	ComposeDir string `json:"compose_dir"`
}

// Ports are the environment's published HTTP ports (not PostgreSQL's).
type Ports struct {
	HTTP     int `json:"http"`
	Longpoll int `json:"longpoll"`
}

// Git is the environment's tracked branch and last deployed revision.
type Git struct {
	Branch      *string `json:"branch"`
	DeployedRev *string `json:"deployed_rev"`
}

// State is Reverb's desired vs observed reconciliation state.
type State struct {
	Desired  string `json:"desired"`
	Observed string `json:"observed"`
}

// Password returns the DB password, or "" when Reverb withheld it.
func (d DB) PasswordValue() string {
	if d.Password == nil {
		return ""
	}
	return *d.Password
}

// Branch returns the tracked branch, or "" when unset.
func (g Git) BranchValue() string {
	if g.Branch == nil {
		return ""
	}
	return *g.Branch
}

// EnvRef is one row of the compact GET /envs listing — no secrets, so it
// is the cheap call used to infer a project from a bare env name, and to
// recover an environment's id when resolve itself is unavailable (a 409
// while it provisions).
type EnvRef struct {
	ID      int64   `json:"id"`
	Project string  `json:"project"`
	Env     string  `json:"env"`
	Stage   string  `json:"stage"`
	State   string  `json:"state"`
	URL     string  `json:"url"`
	Branch  *string `json:"branch"`
}

// errorBody is Reverb's uniform error envelope.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// get performs an authenticated GET and decodes the JSON body into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// post performs an authenticated POST with a JSON body (nil for none)
// and decodes the response into out (nil to discard it).
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	return c.do(ctx, http.MethodPost, path, raw, out)
}

// do performs an authenticated request and decodes the JSON body into
// out. A non-2xx response is mapped to a sentinel by its machine code
// (with the HTTP status as the fallback), carrying Reverb's message for
// context. The token never appears in the returned error.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// url.Error stringifies the request URL, which never carries the
		// token (it travels in a header) — safe to surface.
		return fmt.Errorf("reverb unreachable: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("reverb response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, respBody)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("reverb sent an unreadable response: %w", err)
	}
	return nil
}

// apiError maps a failed response to a sentinel. It prefers the stable
// machine code in the envelope and falls back to the HTTP status when the
// body isn't Reverb's (a proxy error page, say).
func apiError(status int, body []byte) error {
	var e errorBody
	_ = json.Unmarshal(body, &e)
	code, msg := e.Error.Code, strings.TrimSpace(e.Error.Message)

	var base error
	switch {
	case code == "not_ready" || status == http.StatusConflict:
		base = ErrNotReady
	case code == "unauthorized" || status == http.StatusUnauthorized:
		base = ErrUnauthorized
	case code == "insufficient_scope" || status == http.StatusForbidden:
		base = ErrForbidden
	case code == "not_found" || status == http.StatusNotFound:
		base = ErrNotFound
	default:
		if msg == "" {
			return fmt.Errorf("reverb returned HTTP %d", status)
		}
		return fmt.Errorf("reverb returned HTTP %d: %s", status, msg)
	}
	if msg == "" {
		return base
	}
	return fmt.Errorf("%w: %s", base, msg)
}

// Resolve returns the full payload for one environment.
func (c *Client) Resolve(ctx context.Context, project, env string) (Env, error) {
	var out Env
	path := "/api/v1/resolve/" + url.PathEscape(project) + "/" + url.PathEscape(env)
	if err := c.get(ctx, path, &out); err != nil {
		return Env{}, err
	}
	return out, nil
}

// ListEnvs returns every environment across projects, ordered by
// (project, env). Carries no secrets — used to infer the project of a
// bare env name.
func (c *Client) ListEnvs(ctx context.Context) ([]EnvRef, error) {
	var out []EnvRef
	if err := c.get(ctx, "/api/v1/envs", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveWithRetry resolves an environment, waiting out a provisioning
// job instead of failing on it: a 409 not_ready is retried up to
// `attempts` times, `delay` apart. onWait (nil-safe) is called before
// each wait so the caller can log the pause. Every other error returns
// immediately — only not_ready is transient.
func (c *Client) ResolveWithRetry(ctx context.Context, project, env string, attempts int, delay time.Duration, onWait func(attempt, total int)) (Env, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 1; i <= attempts; i++ {
		got, err := c.Resolve(ctx, project, env)
		if err == nil {
			return got, nil
		}
		if !errors.Is(err, ErrNotReady) {
			return Env{}, err
		}
		lastErr = err
		if i == attempts {
			break
		}
		if onWait != nil {
			onWait(i, attempts)
		}
		select {
		case <-ctx.Done():
			return Env{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return Env{}, lastErr
}

// FindEnv locates the environment named `env` across all projects, for
// the bare `-E <env>` form. An unambiguous match returns its row (project
// AND id — the id is what the action routes take); several matches error
// naming every candidate so the user can qualify the reference.
func FindEnv(refs []EnvRef, env string) (EnvRef, error) {
	var hits []EnvRef
	for _, r := range refs {
		if r.Env == env {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return EnvRef{}, fmt.Errorf("%w: no reverb environment named %q", ErrNotFound, env)
	case 1:
		return hits[0], nil
	default:
		names := make([]string, 0, len(hits))
		for _, h := range hits {
			names = append(names, h.Project+"/"+h.Env)
		}
		return EnvRef{}, fmt.Errorf("environment %q exists in several projects (%s) — qualify it as <project>/<env>",
			env, strings.Join(names, ", "))
	}
}
