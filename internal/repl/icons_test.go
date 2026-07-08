package repl

import "testing"

func TestResolveIcons(t *testing.T) {
	tests := []struct {
		name     string
		env, cfg string
		isTTY    bool
		term     string
		want     bool
	}{
		{"env on wins over everything", "on", "off", false, "linux", true},
		{"env off wins over everything", "0", "on", true, "xterm", false},
		{"config on", "", "on", false, "dumb", true},
		{"config off", "", "off", true, "xterm-256color", false},
		{"auto: interactive rich terminal", "", "", true, "xterm-256color", true},
		{"auto: piped/redirected", "", "", false, "xterm-256color", false},
		{"auto: linux console", "", "auto", true, "linux", false},
		{"auto: dumb terminal", "", "", true, "dumb", false},
		{"auto: empty TERM", "", "", true, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveIcons(tc.env, tc.cfg, tc.isTTY, tc.term); got != tc.want {
				t.Errorf("resolveIcons(%q,%q,%v,%q) = %v, want %v",
					tc.env, tc.cfg, tc.isTTY, tc.term, got, tc.want)
			}
		})
	}
}

func TestFileIcon(t *testing.T) {
	// Known extensions resolve to their mapped glyph; case-insensitive.
	if fileIcon("models/sale.py") != fileIcons[".py"] {
		t.Error("py icon mismatch")
	}
	if fileIcon("views/SALE.XML") != fileIcons[".xml"] {
		t.Error("uppercase extension should map like lowercase")
	}
	if fileIcon("i18n/es_MX.po") != fileIcons[".po"] {
		t.Error("po icon mismatch")
	}
	// Unknown / no extension → the generic default glyph.
	if fileIcon("LICENSE") != defaultFileIcon {
		t.Error("no-extension file should use the default icon")
	}
	if fileIcon("weird.zzz") != defaultFileIcon {
		t.Error("unmapped extension should use the default icon")
	}
}
