package cmd

import (
	"reflect"
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
