package cmd

import "testing"

func TestRemoteFlagsIn(t *testing.T) {
	cases := []struct {
		in     []string
		from   string
		remote bool
	}{
		{nil, "", false},
		{[]string{"script.py"}, "", false},
		{[]string{"--remote"}, "", true},
		{[]string{"--from", "prod"}, "prod", false},
		{[]string{"--from=prod", "script.py"}, "prod", false},
		{[]string{"script.py", "--from", "staging", "--remote"}, "staging", true},
		{[]string{"--from"}, "", false}, // dangling value → not remote by name
	}
	for _, tc := range cases {
		from, remote := remoteFlagsIn(tc.in)
		if from != tc.from || remote != tc.remote {
			t.Errorf("remoteFlagsIn(%v) = (%q, %v), want (%q, %v)",
				tc.in, from, remote, tc.from, tc.remote)
		}
	}
}

func TestRemoteExecInteractive(t *testing.T) {
	got := remoteExecInteractive("/srv/odoo/my shop", "docker compose", "odoo-1",
		[]string{"odoo", "shell", "-d", "erp", "--no-http"})
	want := `cd '/srv/odoo/my shop' && docker compose exec 'odoo-1' 'odoo' 'shell' '-d' 'erp' '--no-http'`
	if got != want {
		t.Fatalf("remoteExecInteractive = %q, want %q", got, want)
	}
}
