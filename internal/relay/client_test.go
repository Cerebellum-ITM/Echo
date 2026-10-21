package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "rlt_secret-token-value"

func TestResolve(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"app":"fb","env":"stage","stage":"staging","branch":"stage","odoo_version":"19.0",
			"server":"morwi-fb","ssh":{"host":"54.151.87.198","port":22},"compose_project":"relay-fb-stage",
			"containers":{"odoo":"relay-fb-stage-odoo","db":"relay-fb-stage-db"},"db":{"name":"fuentebuena","user":"odoo"},
			"odoo_conf":"/etc/odoo/odoo.conf","release":null}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", testToken)
	if err != nil {
		t.Fatal(err)
	}
	env, err := c.Resolve(context.Background(), "fb", "stage")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/resolve/fb/stage" || gotAuth != "Bearer "+testToken {
		t.Errorf("request = %s with %q", gotPath, gotAuth)
	}
	if env.Containers.Odoo != "relay-fb-stage-odoo" || env.DB.Name != "fuentebuena" || env.OdooVersion != "19.0" ||
		env.ComposeProject != "relay-fb-stage" || env.OdooConf != "/etc/odoo/odoo.conf" || env.Release != nil {
		t.Errorf("env = %+v", env)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		inMsg  string
	}{
		{"unauthorized", 401, `{"error":{"code":"unauthorized","message":"invalid or revoked token"}}`, ErrUnauthorized, "invalid or revoked token"},
		{"forbidden", 403, `{"error":{"code":"forbidden","message":"a viewer token cannot do this"}}`, ErrForbidden, "viewer"},
		{"unknown env", 404, `{"error":{"code":"unknown_env","message":"unknown env \"qa\" in app \"fb\""}}`, ErrNotFound, "qa"},
		{"other", 500, `{"error":{"code":"internal","message":"could not read"}}`, nil, "internal"},
		{"not relay", 502, `<html>bad gateway</html>`, nil, "HTTP 502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c, _ := New(srv.URL, testToken)
			_, err := c.Resolve(context.Background(), "fb", "qa")
			if err == nil {
				t.Fatal("no error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.inMsg) {
				t.Errorf("err = %v, want it to mention %q", err, tt.inMsg)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("err carries the token: %v", err)
			}
		})
	}
}

func TestCreateAndReadDeploy(t *testing.T) {
	var body DeployRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/deploys":
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":12,"app":"fb","env":"stage","state":"queued","ref":"refs/heads/stage"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/deploys/12":
			_, _ = io.WriteString(w, `{"id":12,"state":"succeeded","steps":[{"seq":1,"name":"resolve","state":"succeeded"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testToken)

	d, err := c.CreateDeploy(context.Background(), DeployRequest{App: "fb", Env: "stage", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if body != (DeployRequest{App: "fb", Env: "stage", DryRun: true}) || d.ID != 12 || d.Finished() {
		t.Errorf("sent %+v, got %+v", body, d)
	}
	d, err = c.Deploy(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Finished() || len(d.Steps) != 1 || d.Steps[0].Name != "resolve" {
		t.Errorf("deploy = %+v", d)
	}
	if got := c.DeployURL(12); got != srv.URL+"/deploys/12" {
		t.Errorf("DeployURL = %s", got)
	}
}

func TestNewNeedsURLAndToken(t *testing.T) {
	for _, tc := range [][2]string{{"", testToken}, {"https://relay.test", " "}} {
		if _, err := New(tc[0], tc[1]); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("New(%q, %q) = %v, want ErrNotConfigured", tc[0], tc[1], err)
		}
	}
}
