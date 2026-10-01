package repl

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/cmd"
	"github.com/pascualchavez/echo/internal/theme"
)

func TestFinishDeployJSONStalePlan(t *testing.T) {
	sess := cmdLogSession(t)
	res := cmd.DeployResult{
		Target:    "stg",
		Plan:      &cmd.DeployPlan{Schema: 1},
		PlanStale: []cmd.PlanChange{{What: "lock", Planned: "3f2a9c1", Now: "b77d0e4"}},
	}
	err := fmt.Errorf("%w: 1 change since p.json was saved", cmd.ErrPlanStale)

	out := captureStdout(t, func() { sess.finishDeployJSON(res, &runStats{errors: 1}, err) })
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 {
		t.Fatalf("want one object on stdout, got %q", out)
	}
	var got struct {
		Plan      map[string]any   `json:"plan"`
		PlanStale []map[string]any `json:"plan_stale"`
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", jerr, out)
	}
	if got.Plan == nil || len(got.PlanStale) != 1 || got.PlanStale[0]["what"] != "lock" {
		t.Errorf("object = %s", out)
	}
	if sess.exitCode != exitError {
		t.Errorf("exit = %d, want %d", sess.exitCode, exitError)
	}
}

func TestAgeValueRedFromOneHour(t *testing.T) {
	p := theme.PaletteByName("")
	for value, red := range map[string]bool{"45s": false, "59m": false, "1h": true, "3h20m": true, "2d": true} {
		if _, styled := valueStyleFor("age", value, p); styled != red {
			t.Errorf("age=%s: red=%v, want %v", value, styled, red)
		}
	}
}
