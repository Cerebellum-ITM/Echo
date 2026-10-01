package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

func TestDBExecCmd(t *testing.T) {
	target := connectTarget{composeCmd: "docker compose", odooContainer: "odoo", dbContainer: "db"}
	argv := odoo.Cmd{"psql", "-U", "odoo", "-d", "prod", "-At", "-c", "SELECT 1"}

	cases := []struct {
		name string
		mode string
		want string
	}{
		{
			name: "compose runs from the project dir",
			mode: dbExecCompose,
			want: "cd '/srv/odoo' && docker compose exec -T 'db' 'psql' '-U' 'odoo' '-d' 'prod' '-At' '-c' 'SELECT 1'",
		},
		{
			name: "docker resolves the container by name",
			mode: dbExecDocker,
			want: "docker exec -i 'db' 'psql' '-U' 'odoo' '-d' 'prod' '-At' '-c' 'SELECT 1'",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dbExecCmd("/srv/odoo", target, c.mode, argv); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestRemoteConnectTargetDBExec(t *testing.T) {
	classic := remoteConnectTarget(config.RemoteProfile{ComposeCmd: "docker compose", DBContainer: "db"})
	if classic.dbExecMode() != dbExecCompose {
		t.Errorf("classic target uses %q, want compose", classic.dbExecMode())
	}
	managed := remoteConnectTarget(config.RemoteProfile{
		ComposeCmd:  "docker compose",
		DBContainer: "reverb-iza-db",
		Reverb:      &config.ReverbMarker{EnvID: 116, Project: "iza", Env: "staging"},
	})
	if managed.dbExecMode() != dbExecDocker {
		t.Errorf("Reverb target uses %q, want docker", managed.dbExecMode())
	}
}

func TestWithDBExecFallback(t *testing.T) {
	t.Run("retries once with docker on no such service", func(t *testing.T) {
		var modes []string
		err := withDBExecFallback(connectTarget{}, func(mode string) error {
			modes = append(modes, mode)
			if mode == dbExecCompose {
				return errors.New("exit status 1: no such service: reverb-iza-db")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("fallback did not recover: %v", err)
		}
		if len(modes) != 2 || modes[0] != dbExecCompose || modes[1] != dbExecDocker {
			t.Errorf("modes = %v, want [compose docker]", modes)
		}
	})

	t.Run("leaves an unrelated failure alone", func(t *testing.T) {
		calls := 0
		err := withDBExecFallback(connectTarget{}, func(string) error {
			calls++
			return errors.New("exit status 1: FATAL: password authentication failed")
		})
		if err == nil {
			t.Fatal("want the original error")
		}
		if calls != 1 {
			t.Errorf("ran %d times, want 1", calls)
		}
	})

	t.Run("a declared docker target never falls back", func(t *testing.T) {
		var modes []string
		err := withDBExecFallback(connectTarget{dbExec: dbExecDocker}, func(mode string) error {
			modes = append(modes, mode)
			return errors.New("no such service: db")
		})
		if err == nil {
			t.Fatal("want the original error")
		}
		if len(modes) != 1 || modes[0] != dbExecDocker {
			t.Errorf("modes = %v, want [docker]", modes)
		}
	})
}

func TestRunRemoteDBCmdFallsBack(t *testing.T) {
	var sent []string
	run := func(_ context.Context, _, remoteCmd string, _ []byte) ([]byte, error) {
		sent = append(sent, remoteCmd)
		if len(sent) == 1 {
			return nil, errors.New("no such service: db")
		}
		return []byte("1\n"), nil
	}
	out, err := runRemoteDBCmd(context.Background(), run, "host", "/srv/odoo",
		connectTarget{composeCmd: "docker compose", dbContainer: "db"},
		odoo.Cmd{"psql", "-At", "-c", "SELECT 1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "1\n" {
		t.Errorf("out = %q, want the retry's output", out)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d commands, want 2", len(sent))
	}
	if sent[1] != "docker exec -i 'db' 'psql' '-At' '-c' 'SELECT 1'" {
		t.Errorf("retry = %q", sent[1])
	}
}
