package repl

import "testing"

func TestParseLooseSeverity(t *testing.T) {
	tests := []struct {
		name string
		line string
		want looseLine
		ok   bool
	}{
		{
			name: "the wkhtmltopdf font warning",
			line: "Warn: Can't find .pfb for face 'Courier'",
			want: looseLine{level: "WARNING", message: "Can't find .pfb for face 'Courier'"},
			ok:   true,
		},
		{
			name: "Warning spelled out",
			line: "Warning: something",
			want: looseLine{level: "WARNING", message: "something"},
			ok:   true,
		},
		{
			name: "Error maps to ERROR",
			line: "Error: Failed loading page",
			want: looseLine{level: "ERROR", message: "Failed loading page"},
			ok:   true,
		},
		{
			name: "Err short form",
			line: "Err: boom",
			want: looseLine{level: "ERROR", message: "boom"},
			ok:   true,
		},
		{
			name: "Critical",
			line: "Critical: out of memory",
			want: looseLine{level: "CRITICAL", message: "out of memory"},
			ok:   true,
		},
		{
			name: "Fatal maps to CRITICAL",
			line: "Fatal: cannot continue",
			want: looseLine{level: "CRITICAL", message: "cannot continue"},
			ok:   true,
		},
		{
			name: "Info",
			line: "Info: done",
			want: looseLine{level: "INFO", message: "done"},
			ok:   true,
		},
		{
			name: "Debug",
			line: "Debug: x=1",
			want: looseLine{level: "DEBUG", message: "x=1"},
			ok:   true,
		},
		{
			name: "case-insensitive lower",
			line: "warn: lowercased",
			want: looseLine{level: "WARNING", message: "lowercased"},
			ok:   true,
		},
		{
			name: "case-insensitive upper",
			line: "ERROR: shouting",
			want: looseLine{level: "ERROR", message: "shouting"},
			ok:   true,
		},
		{
			name: "no space after colon",
			line: "Warn:tight",
			want: looseLine{level: "WARNING", message: "tight"},
			ok:   true,
		},
		{
			name: "real odoo line is not captured",
			line: "2026-06-02 18:34:47,606 3675802 WARNING develop odoo.modules.loading: x",
			ok:   false,
		},
		{
			name: "loguru line is not captured",
			line: "2026-06-02 18:34:47.606 | WARNING | mod:fn:12 - x",
			ok:   false,
		},
		{
			name: "ps table row is not captured",
			line: "NAME                    IMAGE       STATUS",
			ok:   false,
		},
		{
			name: "prose without a leading severity token",
			line: "Cannot find the file you asked for",
			ok:   false,
		},
		{
			name: "a word that merely starts with a keyword is not captured",
			line: "Warnings are disabled",
			ok:   false,
		},
		{
			name: "blank line is not captured",
			line: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLooseSeverity(tt.line)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestRunStatsLooseSeverity asserts a loose Warn: counts as a warning
// while a loose Error: does not count as a failure (so a noisy tool's
// stderr can't flip a healthy run to ✗).
func TestRunStatsLooseSeverity(t *testing.T) {
	stats := &runStats{}
	sink := stats.wrap(func(string) {})

	sink("Warn: Can't find .pfb for face 'Courier'")
	sink("Error: Failed loading page")
	sink("Info: nothing to see")

	if stats.warnings != 1 {
		t.Errorf("warnings = %d, want 1", stats.warnings)
	}
	if stats.errors != 0 {
		t.Errorf("errors = %d, want 0 (loose Error must not fail the run)", stats.errors)
	}
}
