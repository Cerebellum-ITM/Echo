package repl

import "testing"

func TestStripFlag(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		flag      string
		want      []string
		wantFound bool
	}{
		{"present", []string{"sale", "--last"}, "--last", []string{"sale"}, true},
		{"absent", []string{"sale", "--copy"}, "--last", []string{"sale", "--copy"}, false},
		{"with copy", []string{"--last", "--copy"}, "--last", []string{"--copy"}, true},
		{"repeated", []string{"--last", "x", "--last"}, "--last", []string{"x"}, true},
		{"empty", nil, "--last", []string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := stripFlag(c.args, c.flag)
			if found != c.wantFound {
				t.Fatalf("found = %v, want %v", found, c.wantFound)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}
