package config

import (
	"reflect"
	"testing"
)

func TestRunReportRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	in := RunReport{
		Recipe: "deploy",
		Steps: []StepReport{
			{Index: 1, Cmd: "update sale", Status: "ok", Lines: []ReportLine{
				{Level: "INFO", Text: "loading"},
				{Level: "WARNING", Text: "deprecated field"},
			}},
			{Index: 2, Cmd: "restart", Status: "ok"},
		},
	}
	if err := SaveRunReport(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := LoadRunReport()
	if !ok {
		t.Fatal("record missing after save")
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestLoadRunReportMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := LoadRunReport(); ok {
		t.Fatal("missing record must report ok=false")
	}
}
