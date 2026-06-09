package cmd

import "testing"

func TestDBNameFromBackup(t *testing.T) {
	cases := []struct {
		name, want string
	}{
		// Odoo database-manager download: <db>_YYYY-MM-DD_HH-MM-SS.zip
		{"habitta_prod_2026-06-08_23-42-53.zip", "habitta_prod"},
		{"mydb_2026-01-02_03-04-05.zip", "mydb"},
		// Echo's own backup: <db>_YYYYMMDD-HHMMSS.{dump,zip}
		{"habitta_prod_20260608-234253.dump", "habitta_prod"},
		{"odoo_20260101-120000.zip", "odoo"},
		// Underscores in the db name must survive.
		{"a_b_c_2026-06-08_23-42-53.zip", "a_b_c"},
		// No recognizable timestamp → basename without extension.
		{"plain.dump", "plain"},
		{"weird_name.zip", "weird_name"},
	}
	for _, c := range cases {
		if got := dbNameFromBackup(c.name); got != c.want {
			t.Errorf("dbNameFromBackup(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIsHexPrefix(t *testing.T) {
	valid := []string{"99", "ab", "0d", "ff", "7f"}
	for _, s := range valid {
		if !isHexPrefix(s) {
			t.Errorf("isHexPrefix(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "a", "abc", "AB", "g0", "habitta_prod", "z9"}
	for _, s := range invalid {
		if isHexPrefix(s) {
			t.Errorf("isHexPrefix(%q) = true, want false", s)
		}
	}
}
