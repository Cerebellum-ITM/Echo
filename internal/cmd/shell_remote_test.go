package cmd

import (
	"context"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

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

func TestResolveRemoteShellReportsResolution(t *testing.T) {
	remote := newFakeRemote(t)
	remote.writeProfile(t, "stage = \"staging\"\ndb_name = \"stg_db\"\nodoo_version = \"18\"\n")

	var got []RemoteResolution
	prev := OnRemoteResolved
	OnRemoteResolved = func(r RemoteResolution) { got = append(got, r) }
	t.Cleanup(func() { OnRemoteResolved = prev })

	if _, err := resolveRemoteShell(context.Background(), remote.cfg(), theme.PaletteByName(""), t.TempDir(), "stg", nil); err != nil {
		t.Fatalf("resolveRemoteShell: %v", err)
	}
	want := RemoteResolution{Target: "stg", Host: "fakehost", DB: "stg_db", Stage: "staging"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("resolutions = %+v, want one %+v", got, want)
	}

	got = nil
	if _, err := resolveRemoteShell(context.Background(), remote.cfg(), theme.PaletteByName(""), t.TempDir(), "nope", nil); err == nil {
		t.Fatal("unknown target resolved")
	}
	if len(got) != 0 {
		t.Fatalf("a failed resolution was reported: %+v", got)
	}
}
