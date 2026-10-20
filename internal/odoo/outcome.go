package odoo

import (
	"regexp"
	"strconv"
)

// testTally matches the report Odoo logs once per database at the end of a
// --stop-after-init run with tests enabled (odoo/service/server.py, the same
// on 17, 18 and 19; the text is OdooTestResult.__str__):
//
//	… odoo.tests.result: 1 failed, 0 error(s) of 12 tests when loading database 'x'
//
// The level is INFO, WARNING when no test ran and ERROR when one failed.
var testTally = regexp.MustCompile(`odoo\.tests\.(?:result|stats): (\d+) failed, (\d+) error\(s\) of (\d+) tests`)

// TestTally is the count of one Odoo test report.
type TestTally struct {
	Run    int `json:"run"`
	Failed int `json:"failed"`
	Errors int `json:"errors"`
}

// ParseTestTally reads a test report line; ok is false for any other line.
func ParseTestTally(line string) (t TestTally, ok bool) {
	m := testTally.FindStringSubmatch(line)
	if m == nil {
		return TestTally{}, false
	}
	t.Failed, _ = strconv.Atoi(m[1])
	t.Errors, _ = strconv.Atoi(m[2])
	t.Run, _ = strconv.Atoi(m[3])
	return t, true
}
