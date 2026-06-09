package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ReportLine is one captured output line of a recipe step, tagged with its
// parsed log level (DEBUG/INFO/WARNING/ERROR/CRITICAL, or "" when the line
// carries no level token).
type ReportLine struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// StepReport is one recipe step's captured output, for `report`.
type StepReport struct {
	Index  int          `json:"index"` // 1-based
	Cmd    string       `json:"cmd"`
	Status string       `json:"status"` // ok/failed/cancelled/skipped
	Lines  []ReportLine `json:"lines"`
}

// RunReport is the queryable record of the last `echo run`, persisted so
// the separate `report` process can inspect/copy it by step and level.
type RunReport struct {
	Recipe string       `json:"recipe"`
	Steps  []StepReport `json:"steps"`
}

// runReportPath is the single global last-run record:
// ~/.config/echo/run-logs/last-run.json.
func runReportPath() (string, error) {
	dir, err := RunLogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last-run.json"), nil
}

// SaveRunReport writes the record atomically, creating the run-logs dir if
// needed. Best-effort: callers ignore the error so a write failure never
// breaks the run.
func SaveRunReport(r RunReport) error {
	path, err := runReportPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// LoadRunReport reads the last-run record and whether one exists. A missing
// or unparseable file yields (zero, false) — never an error.
func LoadRunReport() (RunReport, bool) {
	path, err := runReportPath()
	if err != nil {
		return RunReport{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RunReport{}, false
	}
	var r RunReport
	if err := json.Unmarshal(data, &r); err != nil {
		return RunReport{}, false
	}
	return r, true
}
