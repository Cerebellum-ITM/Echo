package main

import "testing"

func TestProjectlessOneShot(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
		want bool
	}{
		// Purely informational / remote-only commands: no compose project.
		{"help needs no project", "help", nil, true},
		{"i18n-pull always projectless", "i18n-pull", nil, true},
		{"deploy always projectless", "deploy", nil, true},

		// Remote-mode group: projectless only with a remote selector.
		{"update --remote", "update", []string{"sale", "--remote"}, true},
		{"update --from target", "update", []string{"--from", "prod"}, true},
		{"update --from=target", "update", []string{"--from=prod", "sale"}, true},
		{"update local needs a project", "update", []string{"sale"}, false},
		{"test --remote", "test", []string{"--remote"}, true},
		{"test local needs a project", "test", []string{"sale"}, false},
		// logview/report read the local history store keyed by cwd — always
		// projectless (remote flag switches the source, not the requirement).
		{"logview --remote", "logview", []string{"--remote"}, true},
		{"logview --from target", "logview", []string{"--from", "prod"}, true},
		{"logview local is projectless", "logview", nil, true},
		{"report local is projectless", "report", nil, true},

		// Local-only commands never qualify.
		{"install never projectless", "install", []string{"--remote"}, false},
		{"ps local", "ps", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectlessOneShot(tc.cmd, tc.args); got != tc.want {
				t.Errorf("projectlessOneShot(%q, %v) = %v, want %v", tc.cmd, tc.args, got, tc.want)
			}
		})
	}
}

func TestHasRemoteFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--remote"}, true},
		{[]string{"--from", "prod"}, true},
		{[]string{"--from=prod"}, true},
		{[]string{"sale", "account"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := hasRemoteFlag(c.args); got != c.want {
			t.Errorf("hasRemoteFlag(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
