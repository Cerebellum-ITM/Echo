package theme

import "testing"

func TestMiddleTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short unchanged", "my_shop", 20, "my_shop"},
		{"exact unchanged", "exactlytwenty_chars1", 20, "exactlytwenty_chars1"},
		{"odoo.sh dump", "mycompany-main-prod_2026-06-18_23-42-53", 20, "mycompany-…_23-42-53"},
		{"max 1 unchanged", "verylongname", 1, "verylongname"},
		{"max 0 unchanged", "verylongname", 0, "verylongname"},
		{"even max", "abcdefghij", 6, "abc…ij"},
		{"unicode", "ñññññññññññ", 5, "ññ…ññ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MiddleTruncate(c.in, c.max)
			if got != c.want {
				t.Errorf("MiddleTruncate(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
			}
			// Result never exceeds max (when truncation applies).
			if c.max > 1 && len([]rune(got)) > c.max && len([]rune(c.in)) > c.max {
				t.Errorf("result %q exceeds max %d", got, c.max)
			}
		})
	}
}
