package repl

import (
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseReportArgs(t *testing.T) {
	cases := []struct {
		args    []string
		want    reportArgs
		wantErr bool
	}{
		{[]string{}, reportArgs{}, false},
		{[]string{"--step=2"}, reportArgs{step: 2}, false},
		{[]string{"--level=warn"}, reportArgs{level: "WARNING"}, false},
		{[]string{"--level=warning"}, reportArgs{level: "WARNING"}, false},
		{[]string{"--min-level=error"}, reportArgs{minLevel: "ERROR"}, false},
		{[]string{"--step=1", "--level=critical", "--copy"}, reportArgs{step: 1, level: "CRITICAL", copy: true}, false},
		{[]string{"--level=warn", "--min-level=error"}, reportArgs{}, true}, // mutually exclusive
		{[]string{"--level=bogus"}, reportArgs{}, true},
		{[]string{"--step=0"}, reportArgs{}, true},
		{[]string{"--step=x"}, reportArgs{}, true},
		{[]string{"--bogus"}, reportArgs{}, true},
	}
	for _, c := range cases {
		got, err := parseReportArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("parseReportArgs(%v) err=%v wantErr=%v", c.args, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("parseReportArgs(%v) = %+v, want %+v", c.args, got, c.want)
		}
	}
}

func sampleReport() config.RunReport {
	return config.RunReport{
		Recipe: "r",
		Steps: []config.StepReport{
			{Index: 1, Cmd: "update sale", Status: "ok", Lines: []config.ReportLine{
				{Level: "INFO", Text: "info-1"},
				{Level: "WARNING", Text: "warn-1"},
				{Level: "ERROR", Text: "error-1"},
				{Level: "", Text: "plain-1"},
			}},
			{Index: 2, Cmd: "restart", Status: "ok", Lines: []config.ReportLine{
				{Level: "WARNING", Text: "warn-2"},
				{Level: "CRITICAL", Text: "crit-2"},
			}},
		},
	}
}

func texts(lines []config.ReportLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

func TestFilterReport(t *testing.T) {
	rep := sampleReport()

	cases := []struct {
		name string
		args reportArgs
		want []string
	}{
		{"all", reportArgs{}, []string{"info-1", "warn-1", "error-1", "plain-1", "warn-2", "crit-2"}},
		{"step 1", reportArgs{step: 1}, []string{"info-1", "warn-1", "error-1", "plain-1"}},
		{"exact warning", reportArgs{level: "WARNING"}, []string{"warn-1", "warn-2"}},
		{"exact error (not critical)", reportArgs{level: "ERROR"}, []string{"error-1"}},
		{"min error (incl critical)", reportArgs{minLevel: "ERROR"}, []string{"error-1", "crit-2"}},
		{"step 1 + exact warning", reportArgs{step: 1, level: "WARNING"}, []string{"warn-1"}},
		{"min warning excludes plain/info", reportArgs{minLevel: "WARNING"}, []string{"warn-1", "error-1", "warn-2", "crit-2"}},
	}
	for _, c := range cases {
		got, err := filterReport(rep, c.args)
		if err != nil {
			t.Errorf("%s: unexpected err %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(texts(got), c.want) {
			t.Errorf("%s: got %v, want %v", c.name, texts(got), c.want)
		}
	}
}

func TestFilterReportStepOutOfRange(t *testing.T) {
	if _, err := filterReport(sampleReport(), reportArgs{step: 9}); err == nil {
		t.Error("step out of range must error")
	}
}

func TestLineLevel(t *testing.T) {
	cases := []struct{ text, want string }{
		{"2026-06-02 18:34:47,606 3675 WARNING develop odoo.x: hi", "WARNING"},
		{"2026-06-02 18:34:47,606 3675 ERROR develop odoo.x: boom", "ERROR"},
		{"2026-06-02 18:34:47.606 | CRITICAL | m:f:1 - x", "CRITICAL"},
		{"Warn: Can't find .pfb for face 'Courier'", "WARNING"},
		{"just some plain output", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := lineLevel(c.text); got != c.want {
			t.Errorf("lineLevel(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
