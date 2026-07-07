package cmd

import "testing"

// TestParseViewArgs pins the flag/positional split, especially that the
// remote-mode switches are consumed and never leak into the module name.
func TestParseViewArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		module  string
		copy    bool
		from    string
		remote  bool
		wantErr bool
	}{
		{"empty", nil, "", false, "", false, false},
		{"module only", []string{"sale"}, "sale", false, "", false, false},
		{"copy", []string{"sale", "--copy"}, "sale", true, "", false, false},
		{"from with value", []string{"sale", "--from", "prod"}, "sale", false, "prod", false, false},
		{"from-value not a module", []string{"--from", "prod"}, "", false, "prod", false, false},
		{"from equals", []string{"sale", "--from=prod"}, "sale", false, "prod", false, false},
		{"bare remote", []string{"--remote", "sale"}, "sale", false, "", true, false},
		{"remote copy", []string{"sale", "--remote", "--copy"}, "sale", true, "", true, false},
		{"unknown flag", []string{"sale", "--nope"}, "", false, "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			module, copyFlag, from, remote, err := parseViewArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseViewArgs(%v) err = nil, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseViewArgs(%v) err = %v", tc.args, err)
			}
			if module != tc.module || copyFlag != tc.copy || from != tc.from || remote != tc.remote {
				t.Errorf("parseViewArgs(%v) = (%q, %v, %q, %v); want (%q, %v, %q, %v)",
					tc.args, module, copyFlag, from, remote,
					tc.module, tc.copy, tc.from, tc.remote)
			}
		})
	}
}

// TestRemoteModuleDir pins the two directory layouts: joined under
// remotePath for the host filesystem, absolute base for the container.
func TestRemoteModuleDir(t *testing.T) {
	rv := remoteView{rsc: remoteShellContext{remotePath: "/srv/odoo"}}
	if got := remoteModuleDir(rv, "addons", "sale", false); got != "/srv/odoo/addons/sale" {
		t.Errorf("host dir = %q, want /srv/odoo/addons/sale", got)
	}
	if got := remoteModuleDir(rv, ".", "sale", false); got != "/srv/odoo/sale" {
		t.Errorf("host dir (dot base) = %q, want /srv/odoo/sale", got)
	}
	if got := remoteModuleDir(rv, "/mnt/extra-addons/", "sale", true); got != "/mnt/extra-addons/sale" {
		t.Errorf("container dir = %q, want /mnt/extra-addons/sale", got)
	}
}

// TestParseViewArgsFirstPositionalWins guards that extra positionals after
// the module don't override it (mirrors RunView's single-module behavior).
func TestParseViewArgsFirstPositionalWins(t *testing.T) {
	module, _, _, _, err := parseViewArgs([]string{"sale", "account"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if module != "sale" {
		t.Errorf("module = %q, want sale", module)
	}
}
