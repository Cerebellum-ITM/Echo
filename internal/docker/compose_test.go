package docker

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpCallsNeverRecreateTheDB(t *testing.T) {
	cases := []struct {
		name     string
		services []string
		odoo, db string
		want     [][]string
	}{
		{"whole stack", nil, "odoo", "db", [][]string{
			{"up", "--detach", "--no-recreate"},
			{"up", "--detach", "--no-deps", "odoo"},
		}},
		{"whole stack without an odoo service", nil, "", "db", [][]string{
			{"up", "--detach", "--no-recreate"},
		}},
		{"odoo alone", []string{"odoo"}, "odoo", "db", [][]string{
			{"up", "--detach", "--no-recreate", "db"},
			{"up", "--detach", "--no-deps", "odoo"},
		}},
		{"db alone", []string{"db"}, "odoo", "db", [][]string{
			{"up", "--detach", "--no-recreate", "db"},
		}},
		{"db among others", []string{"db", "odoo", "nginx"}, "odoo", "db", [][]string{
			{"up", "--detach", "--no-recreate", "db"},
			{"up", "--detach", "--no-deps", "odoo", "nginx"},
		}},
		{"db outside compose", []string{"odoo"}, "odoo", "", [][]string{
			{"up", "--detach", "--no-deps", "odoo"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UpCalls(c.services, c.odoo, c.db); !reflect.DeepEqual(got, c.want) {
				t.Errorf("UpCalls = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseDBImageDrift(t *testing.T) {
	drift, ok := ParseDBImageDrift("postgres:16\tsha256:aaa\tsha256:bbb\n")
	if !ok || drift.Image != "postgres:16" || drift.Running != "sha256:aaa" || drift.Current != "sha256:bbb" {
		t.Errorf("drift = %+v, %v", drift, ok)
	}
	for _, out := range []string{"", "postgres:16\tsha256:aaa\tsha256:aaa\n", "postgres:16\tsha256:aaa\t\n"} {
		if _, ok := ParseDBImageDrift(out); ok {
			t.Errorf("%q reported as drift", out)
		}
	}
	if got := ShortImageID("sha256:0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("ShortImageID = %q", got)
	}
}

func TestCheckDBImageDriftRunsTheScript(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
  "compose ps") [ "$5" = db ] && echo c0ffee ;;
  "inspect -f") case "$3" in *Config.Image*) echo postgres:16 ;; *) echo sha256:old ;; esac ;;
  "image inspect") [ "$5" = postgres:16 ] && echo "$FAKE_CURRENT" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Setenv("FAKE_CURRENT", "sha256:new")
	drift, ok := CheckDBImageDrift(context.Background(), "docker compose", t.TempDir(), "db")
	if !ok || drift != (DBImageDrift{Image: "postgres:16", Running: "sha256:old", Current: "sha256:new"}) {
		t.Errorf("drift = %+v, %v", drift, ok)
	}
	t.Setenv("FAKE_CURRENT", "sha256:old")
	if _, ok := CheckDBImageDrift(context.Background(), "docker compose", t.TempDir(), "db"); ok {
		t.Error("same image reported as drift")
	}
	if _, ok := CheckDBImageDrift(context.Background(), "docker compose", t.TempDir(), "postgres"); ok {
		t.Error("missing container reported as drift")
	}
}
