package reverb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// resolveJSON is a full payload in Reverb's frozen shape.
const resolveJSON = `{
  "project": "acme",
  "env": "feature-x",
  "stage": "dev",
  "url": "https://feature-x-acme.dev.example.com",
  "ssh_host": "deploy@dev.example.com",
  "odoo_version": "18.0",
  "containers": {"odoo": "reverb-acme-feature-x-odoo", "db": "reverb-acme-db"},
  "db": {"name": "acme_feature_x", "user": "acme_role", "password": "s3cret"},
  "paths": {"addons": "/data/acme/envs/feature-x/addons",
            "overlay": "/data/acme/envs/feature-x/overlay",
            "compose_dir": "/data/acme/envs/feature-x"},
  "ports": {"http": 20003, "longpoll": 20004},
  "git": {"branch": "feature-x", "deployed_rev": "abc123"},
  "state": {"desired": "running", "observed": "running"}
}`

// newTestClient points a client at an httptest server.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "rvb_test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRequiresURLAndToken(t *testing.T) {
	for _, tc := range []struct{ url, token string }{
		{"", "rvb_x"}, {"https://r.example.com", ""}, {"  ", "  "},
	} {
		if _, err := New(tc.url, tc.token); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("New(%q,%q) err = %v, want ErrNotConfigured", tc.url, tc.token, err)
		}
	}
	c, err := New("https://r.example.com/", "  rvb_x  ")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.BaseURL != "https://r.example.com" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.Token != "rvb_x" {
		t.Errorf("Token = %q, want trimmed", c.Token)
	}
}

func TestResolveDecodesFrozenPayload(t *testing.T) {
	var gotPath, gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resolveJSON))
	})

	env, err := c.Resolve(context.Background(), "acme", "feature-x")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotPath != "/api/v1/resolve/acme/feature-x" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer rvb_test" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if env.SSHHost != "deploy@dev.example.com" {
		t.Errorf("SSHHost = %q", env.SSHHost)
	}
	if env.Paths.Overlay != "/data/acme/envs/feature-x/overlay" {
		t.Errorf("Paths.Overlay = %q", env.Paths.Overlay)
	}
	if env.Paths.ComposeDir != "/data/acme/envs/feature-x" {
		t.Errorf("Paths.ComposeDir = %q", env.Paths.ComposeDir)
	}
	if env.Containers.Odoo != "reverb-acme-feature-x-odoo" || env.Containers.DB != "reverb-acme-db" {
		t.Errorf("Containers = %+v", env.Containers)
	}
	if env.DB.Name != "acme_feature_x" || env.DB.User != "acme_role" || env.DB.PasswordValue() != "s3cret" {
		t.Errorf("DB = %+v", env.DB)
	}
	if env.Stage != "dev" || env.OdooVersion != "18.0" {
		t.Errorf("Stage/OdooVersion = %q/%q", env.Stage, env.OdooVersion)
	}
	if env.Git.BranchValue() != "feature-x" {
		t.Errorf("Git.Branch = %q", env.Git.BranchValue())
	}
}

func TestPasswordNullFromSession(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"db":{"name":"d","user":"u","password":null}}`))
	})
	env, err := c.Resolve(context.Background(), "p", "e")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.DB.PasswordValue() != "" {
		t.Errorf("PasswordValue = %q, want empty for a null password", env.DB.PasswordValue())
	}
}

func TestErrorCodesMapToSentinels(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusConflict, "not_ready", ErrNotReady},
		{http.StatusUnauthorized, "unauthorized", ErrUnauthorized},
		{http.StatusForbidden, "insufficient_scope", ErrForbidden},
		{http.StatusNotFound, "not_found", ErrNotFound},
	}
	for _, tc := range cases {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"code":"` + tc.code + `","message":"boom"}}`))
		})
		_, err := c.Resolve(context.Background(), "p", "e")
		if !errors.Is(err, tc.want) {
			t.Errorf("code %s: err = %v, want %v", tc.code, err, tc.want)
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Errorf("code %s: err %q drops the server message", tc.code, err)
		}
	}
}

// A non-Reverb error page (proxy, gateway) must still fail cleanly by
// status rather than decoding into a success.
func TestNonEnvelopeErrorFallsBackToStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502</html>"))
	})
	_, err := c.Resolve(context.Background(), "p", "e")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want an HTTP 502 error", err)
	}
}

// The token is a secret: it must never reach an error string.
func TestErrorsNeverCarryTheToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
	})
	_, err := c.Resolve(context.Background(), "p", "e")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "rvb_test") {
		t.Errorf("error leaks the token: %q", err)
	}
}

func TestListEnvs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/envs" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"project":"acme","env":"main","stage":"staging","state":"running","url":"","branch":"main"}]`))
	})
	refs, err := c.ListEnvs(context.Background())
	if err != nil {
		t.Fatalf("ListEnvs: %v", err)
	}
	if len(refs) != 1 || refs[0].Project != "acme" || refs[0].Env != "main" {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestResolveWithRetryWaitsOutNotReady(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"not_ready","message":"provisioning"}}`))
			return
		}
		_, _ = w.Write([]byte(resolveJSON))
	})
	var waits int
	env, err := c.ResolveWithRetry(context.Background(), "acme", "feature-x", 3, time.Millisecond,
		func(attempt, total int) { waits++ })
	if err != nil {
		t.Fatalf("ResolveWithRetry: %v", err)
	}
	if env.Project != "acme" {
		t.Errorf("Project = %q", env.Project)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if waits != 2 {
		t.Errorf("waits = %d, want 2", waits)
	}
}

func TestResolveWithRetryGivesUp(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"not_ready","message":"provisioning"}}`))
	})
	_, err := c.ResolveWithRetry(context.Background(), "p", "e", 2, time.Millisecond, nil)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
}

// Only not_ready is transient: any other failure must return at once.
func TestResolveWithRetryDoesNotRetryOtherErrors(t *testing.T) {
	var calls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_scope","message":"need echo scope"}}`))
	})
	_, err := c.ResolveWithRetry(context.Background(), "p", "e", 3, time.Millisecond, nil)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", calls)
	}
}

func TestFindEnv(t *testing.T) {
	refs := []EnvRef{
		{ID: 3, Project: "acme", Env: "main"},
		{ID: 7, Project: "acme", Env: "feature-x"},
		{ID: 9, Project: "beta", Env: "main"},
	}
	// The row carries the id, which is what every action route takes.
	got, err := FindEnv(refs, "feature-x")
	if err != nil || got.Project != "acme" || got.ID != 7 {
		t.Errorf("FindEnv(feature-x) = (%+v, %v), want acme/7", got, err)
	}
	_, err = FindEnv(refs, "main")
	if err == nil || !strings.Contains(err.Error(), "acme/main") || !strings.Contains(err.Error(), "beta/main") {
		t.Errorf("ambiguous err = %v, want both candidates named", err)
	}
	if _, err := FindEnv(refs, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v, want ErrNotFound", err)
	}
}
