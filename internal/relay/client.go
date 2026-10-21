// Package relay is the client of a Relay control plane's echo-facing API:
// the resolve endpoint that says where one env lives and what runs there,
// and the deploy routes Echo delegates `deploy` to.
//
// The JSON field names mirror Relay's contract (`internal/api/resolve.go`
// and `internal/api/deploys.go` there) and must not be renamed on a whim.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrNotConfigured means no [relay] url or token.
	ErrNotConfigured = errors.New("relay is not configured")
	// ErrUnauthorized is 401: no token, or one Relay does not know.
	ErrUnauthorized = errors.New("relay rejected the token")
	// ErrForbidden is 403: a valid token whose scope cannot do this.
	ErrForbidden = errors.New("token scope cannot do this")
	// ErrNotFound is 404: unknown app, env or deploy.
	ErrNotFound = errors.New("not found")
)

// Client talks to one Relay. Token is a secret and never appears in an
// error or in anything this package returns.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client; a missing url or token is ErrNotConfigured.
func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" || token == "" {
		return nil, ErrNotConfigured
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("invalid relay url: %w", err)
	}
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}, nil
}

// Env is the resolve payload of one env.
type Env struct {
	App            string     `json:"app"`
	Env            string     `json:"env"`
	Stage          string     `json:"stage"`
	Branch         string     `json:"branch"`
	OdooVersion    string     `json:"odoo_version"`
	Server         string     `json:"server"`
	SSH            SSH        `json:"ssh"`
	ComposeProject string     `json:"compose_project"`
	Containers     Containers `json:"containers"`
	DB             DB         `json:"db"`
	// OdooConf is the odoo.conf inside the odoo container; it holds the db
	// credentials, which the payload never carries.
	OdooConf string   `json:"odoo_conf"`
	Release  *Release `json:"release"`
}

// SSH is the receiver's address as Relay knows it. It has no user: Echo
// connects through the user's own ssh config.
type SSH struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type Containers struct {
	Odoo string `json:"odoo"`
	DB   string `json:"db"`
}

type DB struct {
	Name string `json:"name"`
	User string `json:"user"`
}

// Release is what the env runs now; nil for an env never deployed.
type Release struct {
	DeployID  int64     `json:"deploy_id"`
	AppSHA    string    `json:"app_sha"`
	Image     string    `json:"image"`
	CreatedAt time.Time `json:"created_at"`
}

// DeployRequest is the body of POST /api/v1/deploys. An empty Ref deploys
// the env's branch.
type DeployRequest struct {
	App    string `json:"app"`
	Env    string `json:"env"`
	Ref    string `json:"ref,omitempty"`
	DryRun bool   `json:"dry_run"`
}

// Deploy is a deploy as GET /api/v1/deploys/{id} returns it.
type Deploy struct {
	ID      int64  `json:"id"`
	App     string `json:"app"`
	Env     string `json:"env"`
	State   string `json:"state"`
	Ref     string `json:"ref"`
	AppSHA  string `json:"app_sha"`
	DryRun  bool   `json:"dry_run"`
	Error   string `json:"error"`
	Steps   []Step `json:"steps"`
	Actor   string `json:"actor"`
	Trigger string `json:"trigger"`
}

type Step struct {
	Seq   int    `json:"seq"`
	Name  string `json:"name"`
	State string `json:"state"`
	Error string `json:"error"`
}

// Deploy states the follow loop needs.
const (
	StateSucceeded        = "succeeded"
	StateFailed           = "failed"
	StateRolledBack       = "rolled_back"
	StateCancelled        = "cancelled"
	StateAwaitingApproval = "awaiting_approval"
)

// Finished reports whether the deploy reached a state it never leaves.
func (d Deploy) Finished() bool {
	switch d.State {
	case StateSucceeded, StateFailed, StateRolledBack, StateCancelled:
		return true
	}
	return false
}

// Resolve returns where app's env lives.
func (c *Client) Resolve(ctx context.Context, app, env string) (Env, error) {
	var out Env
	err := c.do(ctx, http.MethodGet, "/api/v1/resolve/"+url.PathEscape(app)+"/"+url.PathEscape(env), nil, &out)
	return out, err
}

// CreateDeploy queues a deploy and returns it as Relay accepted it.
func (c *Client) CreateDeploy(ctx context.Context, req DeployRequest) (Deploy, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Deploy{}, err
	}
	var out Deploy
	err = c.do(ctx, http.MethodPost, "/api/v1/deploys", body, &out)
	return out, err
}

// Deploy returns one deploy with its steps.
func (c *Client) Deploy(ctx context.Context, id int64) (Deploy, error) {
	var out Deploy
	err := c.do(ctx, http.MethodGet, "/api/v1/deploys/"+strconv.FormatInt(id, 10), nil, &out)
	return out, err
}

// DeployURL is the deploy's page in Relay's web UI.
func (c *Client) DeployURL(id int64) string {
	return c.BaseURL + "/deploys/" + strconv.FormatInt(id, 10)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

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
		return fmt.Errorf("relay unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("relay response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("relay sent an unreadable response: %w", err)
	}
	return nil
}

// apiError maps Relay's error envelope to a sentinel, keeping Relay's
// message and machine code for the operator.
func apiError(status int, body []byte) error {
	var e errorBody
	_ = json.Unmarshal(body, &e)
	code, msg := e.Error.Code, strings.TrimSpace(e.Error.Message)
	var base error
	switch status {
	case http.StatusUnauthorized:
		base = ErrUnauthorized
	case http.StatusForbidden:
		base = ErrForbidden
	case http.StatusNotFound:
		base = ErrNotFound
	default:
		if msg == "" {
			return fmt.Errorf("relay returned HTTP %d", status)
		}
		return fmt.Errorf("relay returned HTTP %d (%s): %s", status, code, msg)
	}
	if msg == "" {
		return base
	}
	return fmt.Errorf("%w: %s", base, msg)
}
