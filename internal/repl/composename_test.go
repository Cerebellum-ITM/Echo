package repl

import "testing"

func TestNormalizeProjectName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"echo", "echo"},
		{"Echo-Odoo17", "echo_odoo17"},
		{"  spaced name  ", "spacedname"},
		{"odoo--17__dev", "odoo_17_dev"},
		{"___leading_trailing___", "leading_trailing"},
		{"!!!", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeProjectName(c.in); got != c.want {
			t.Errorf("normalizeProjectName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncateName(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactlength", 11, "exactlength"},
		{"toolongname", 8, "toolong…"},
		{"abcdef", 2, "abc…"}, // max clamped to 4
		{"áéíóú", 4, "áéí…"},  // multi-byte runes preserved
	}
	for _, c := range cases {
		if got := truncateName(c.in, c.max); got != c.want {
			t.Errorf("truncateName(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
