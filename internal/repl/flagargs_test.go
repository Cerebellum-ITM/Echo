package repl

import "testing"

func TestFlagArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"single flag", []string{"real_estate_bits", "--i18n"}, "--i18n"},
		{"flag with value", []string{"sale", "--level=debug"}, "--level=debug"},
		{"several flags, order preserved", []string{"--all", "--i18n", "--level=info"}, "--all --i18n --level=info"},
		{"no flags", []string{"sale", "stock"}, ""},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		if got := flagArgs(c.args); got != c.want {
			t.Errorf("%s: flagArgs(%v) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}
