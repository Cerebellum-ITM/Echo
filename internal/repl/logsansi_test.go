package repl

import (
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/theme"
)

// A `docker compose logs` line can carry Odoo's ColoredFormatter SGR codes
// (the container ran Odoo attached to a TTY). Those codes break the Odoo
// prefix regex, so the line would print verbatim with docker's native colors
// instead of Echo's per-segment styling — the exact gap between `logs` and
// `update`. emitStreamLine strips ANSI first so both go through formatOdooLine.
func TestLogsAnsiRoutesThroughFormatter(t *testing.T) {
	p := theme.PaletteByName("")
	s := theme.New(p, theme.StageDev)

	// Same line, once with SGR codes (TTY) and once plain (exec -T).
	plain := "2026-06-11 20:19:10,569 1 INFO habitta_prod odoo.addons.base.models.ir_cron: Job 'Invoice OCR' completed"
	withANSI := "\x1b[1m\x1b[32m2026-06-11 20:19:10,569\x1b[0m 1 \x1b[32mINFO\x1b[0m habitta_prod \x1b[34modoo.addons.base.models.ir_cron\x1b[0m: Job 'Invoice OCR' completed"

	// Raw ANSI must NOT match — that is why logs looked different from update.
	if _, ok := formatOdooLine(withANSI, s, p); ok {
		t.Fatal("expected the raw ANSI line to miss formatOdooLine")
	}
	// After the strip emitStreamLine applies, it must match and render
	// segment-styled, identical to what `update` (plain) produces.
	got, ok := formatOdooLine(stripANSISeq(withANSI), s, p)
	if !ok {
		t.Fatal("expected the ANSI-stripped line to match formatOdooLine")
	}
	want, _ := formatOdooLine(plain, s, p)
	if got != want {
		t.Fatalf("stripped logs line must render identically to update\n got: %q\nwant: %q", got, want)
	}
	// And the source SGR codes are gone from the output.
	if strings.Contains(stripANSISeq(withANSI), "\x1b[") {
		t.Fatal("stripANSISeq left escape codes behind")
	}
}
