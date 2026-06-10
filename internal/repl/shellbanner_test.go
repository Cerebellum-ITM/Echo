package repl

import (
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

func TestStyleShellBanner(t *testing.T) {
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageFromString("dev"))

	matched := []string{
		"env: <odoo.api.Environment object at 0x7f183e756570>",
		"odoo: <module 'odoo' from '/usr/lib/python3/dist-packages/odoo/__init__.py'>",
		"openerp: <module 'odoo' from '/usr/lib/python3/dist-packages/odoo/__init__.py'>",
		"self: res.users(1,)",
		"Python 3.12.3 (main, Nov  6 2024, 18:32:19) [GCC 13.2.0]",
		"Type 'copyright', 'credits' or 'license' for more information",
		"IPython 9.11.0 -- An enhanced Interactive Python. Type '?' for help.",
		"Tip: Use the IPython.lib.demo.Demo class to load any Python script as an interactive demo.",
	}
	for _, l := range matched {
		if _, ok := styleShellBanner(l, s, p); !ok {
			t.Errorf("expected styleShellBanner to match: %q", l)
		}
	}

	passthrough := []string{
		"",
		"In [1]: ",
		"some random eval output",
		"2026-06-10 02:22:50,219 55 INFO ? odoo: log line",
	}
	for _, l := range passthrough {
		if _, ok := styleShellBanner(l, s, p); ok {
			t.Errorf("expected passthrough (no match): %q", l)
		}
	}
}

func TestStyleShellBannerGlobalKeepsValue(t *testing.T) {
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageFromString("dev"))
	out, ok := styleShellBanner("self: res.users(1,)", s, p)
	if !ok {
		t.Fatal("self: line should match")
	}
	// The value text must survive (only styling added around it).
	if !strings.Contains(out, "res.users(1,)") {
		t.Fatalf("value dropped: %q", out)
	}
	if !strings.Contains(out, "self") {
		t.Fatalf("key dropped: %q", out)
	}
}
