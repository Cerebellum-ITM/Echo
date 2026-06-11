package docker

import "testing"

func TestParseModuleStates(t *testing.T) {
	out := "account|installed|17.0.1.2.3|f\n" +
		"sale|to upgrade|17.0.1.0.0|f\n" +
		"never_installed|uninstalled||t\n"
	rows := parseModuleStates(out)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}

	if rows[0] != (ModuleStateRow{Name: "account", State: "installed", Version: "17.0.1.2.3", VersionNull: false}) {
		t.Errorf("row[0] = %+v", rows[0])
	}
	if rows[1].State != "to upgrade" {
		t.Errorf("row[1].State = %q, want %q", rows[1].State, "to upgrade")
	}
	if !rows[2].VersionNull || rows[2].Version != "" {
		t.Errorf("row[2] null = %v version = %q, want null + empty", rows[2].VersionNull, rows[2].Version)
	}
}

func TestParseModuleStatesEmpty(t *testing.T) {
	if rows := parseModuleStates(""); rows != nil {
		t.Errorf("empty input rows = %+v, want nil", rows)
	}
	if rows := parseModuleStates("\n\n"); rows != nil {
		t.Errorf("blank-only input rows = %+v, want nil", rows)
	}
}

func TestParseModuleStatesShortLine(t *testing.T) {
	// A malformed line missing the NULL-flag column is skipped, not panicked.
	rows := parseModuleStates("account|installed|17.0\n")
	if rows != nil {
		t.Errorf("short line rows = %+v, want nil", rows)
	}
}
