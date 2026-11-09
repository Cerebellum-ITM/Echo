package cmd

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRemoteServiceArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"bare remote", []string{"--remote"}, []string{}},
		{"from with value", []string{"--from", "prod"}, []string{}},
		{"from equals", []string{"--from=prod"}, []string{}},
		{"force stripped", []string{"--remote", "--force"}, []string{}},
		{"service kept", []string{"--from", "prod", "web"}, []string{"web"}},
		{"multiple services", []string{"db", "--remote", "web"}, []string{"db", "web"}},
		{"service before from-value not swallowed", []string{"web", "--from", "prod"}, []string{"web"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := remoteServiceArgs(tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("remoteServiceArgs(%v) = %#v, want %#v", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseLogsArgs(t *testing.T) {
	tests := []struct {
		name                  string
		args                  []string
		follow, copyMode, all bool
		tail                  string
		services              []string
	}{
		{"defaults", nil, true, false, false, "100", []string{}},
		{"no-follow", []string{"--no-follow"}, false, false, false, "100", []string{}},
		{"copy clears follow", []string{"-c"}, false, true, false, "100", []string{}},
		{"tail value", []string{"-t", "200"}, true, false, false, "200", []string{}},
		{"all", []string{"--all"}, true, false, true, "100", []string{}},
		{"remote flags stripped", []string{"--from", "prod", "--remote"}, true, false, false, "100", []string{}},
		{"from-value not a service", []string{"--from", "prod", "web"}, true, false, false, "100", []string{"web"}},
		{"service kept with tail", []string{"db", "-t", "50"}, true, false, false, "50", []string{"db"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			follow, copyMode, all, tail, services := parseLogsArgs(tc.args)
			if follow != tc.follow || copyMode != tc.copyMode || all != tc.all || tail != tc.tail {
				t.Errorf("parseLogsArgs(%v) = follow=%v copy=%v all=%v tail=%q; want %v %v %v %q",
					tc.args, follow, copyMode, all, tail, tc.follow, tc.copyMode, tc.all, tc.tail)
			}
			if !reflect.DeepEqual(services, tc.services) {
				t.Errorf("parseLogsArgs(%v) services = %#v, want %#v", tc.args, services, tc.services)
			}
		})
	}
}

// TestRemoteComposeCmdRestartLogs pins the exact remote command strings the
// restart/logs branches assemble through remoteComposeCmd.
func TestRemoteComposeCmdRestartLogs(t *testing.T) {
	restart := remoteComposeCmd("/srv/odoo", "docker compose",
		append([]string{"restart"}, "odoo")...)
	if want := `cd '/srv/odoo' && docker compose 'restart' 'odoo'`; restart != want {
		t.Errorf("restart cmd = %q, want %q", restart, want)
	}

	logsArgs := []string{"logs", "--no-log-prefix", "-f", "--tail", "200", "odoo"}
	logs := remoteComposeCmd("/srv/odoo", "docker compose", logsArgs...)
	want := `cd '/srv/odoo' && docker compose 'logs' '--no-log-prefix' '-f' '--tail' '200' 'odoo'`
	if logs != want {
		t.Errorf("logs cmd = %q, want %q", logs, want)
	}
}

func TestRemoteUpCmdNeverRecreatesTheDB(t *testing.T) {
	rsc := testRSC()
	cases := []struct {
		services []string
		want     string
	}{
		{nil, "cd '/srv/odoo' && docker compose 'up' '--detach' '--no-recreate' && docker compose 'up' '--detach' '--no-deps' 'odoo'"},
		{[]string{"odoo"}, "cd '/srv/odoo' && docker compose 'up' '--detach' '--no-recreate' 'db' && docker compose 'up' '--detach' '--no-deps' 'odoo'"},
	}
	for _, c := range cases {
		if got := remoteUpCmd(rsc, c.services...); got != c.want {
			t.Errorf("remoteUpCmd(%q) =\n%s\nwant\n%s", c.services, got, c.want)
		}
	}

	rsc.target.dbExec = dbExecDocker
	want := "cd '/srv/odoo' && docker compose 'up' '--detach' '--no-deps' 'odoo'"
	if got := remoteUpCmd(rsc, "odoo"); got != want {
		t.Errorf("db outside compose: %s", got)
	}
}

func TestWarnRemoteDBImageDrift(t *testing.T) {
	orig := ckptRunSSH
	t.Cleanup(func() { ckptRunSSH = orig })
	var calls []string
	out := "postgres:16\tsha256:0123456789abcdef\tsha256:fedcba9876543210\n"
	ckptRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		calls = append(calls, remoteCmd)
		return []byte(out), nil
	}

	rec := &logRecorder{}
	warnRemoteDBImageDrift(context.Background(), testRSC(), rec.log)
	want := "WARNING compose db image changed on the host — keeping the running container service=db image=postgres:16 running=0123456789ab new=fedcba987654"
	if len(rec.lines) != 1 || rec.lines[0] != want {
		t.Errorf("log = %q", rec.lines)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "cd '/srv/odoo' && sh -c ") || !strings.HasSuffix(calls[0], " sh 'docker compose' 'db'") {
		t.Errorf("calls = %q", calls)
	}

	out = "postgres:16\tsha256:0123456789abcdef\tsha256:0123456789abcdef\n"
	rec = &logRecorder{}
	warnRemoteDBImageDrift(context.Background(), testRSC(), rec.log)
	if len(rec.lines) != 0 {
		t.Errorf("same image warned: %q", rec.lines)
	}
}
