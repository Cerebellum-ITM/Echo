package repl

import (
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

// renderOdooLog must produce a self-contained styled line carrying the
// logger and message — this is what the shell transform uses to reformat
// loose-severity stderr (wkhtmltopdf `Warn:` …) into Echo's Odoo style.
func TestRenderOdooLogLooseSeverity(t *testing.T) {
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageFromString("dev"))

	ll, ok := parseLooseSeverity("Warn: Can't find .pfb for face 'Courier'")
	if !ok || ll.level != "WARNING" {
		t.Fatalf("parseLooseSeverity = %+v ok=%v", ll, ok)
	}
	out := renderOdooLog(ll.level, looseSeverityLogger, ll.message, nil, s, p, "habitta_prod")
	for _, want := range []string{looseSeverityLogger, "Can't find .pfb", "habitta_prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered loose line missing %q: %q", want, out)
		}
	}
}
