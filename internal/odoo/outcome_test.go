package odoo

import "testing"

// Report lines as odoo/service/server.py logs them on 17.0, 18.0 and 19.0:
// `"%s when loading database %r"` with OdooTestResult.__str__, through the
// netsvc format `%(asctime)s %(pid)s %(levelname)s %(dbname)s %(name)s:
// %(message)s %(perf_info)s` (perf_info is empty outside werkzeug, hence the
// trailing space).
func TestParseTestTally(t *testing.T) {
	cases := []struct {
		line string
		want TestTally
		ok   bool
	}{
		{"2026-10-19 10:00:00,123 7 INFO demo odoo.tests.result: 0 failed, 0 error(s) of 12 tests when loading database 'demo' ",
			TestTally{Run: 12}, true},
		{"2026-10-19 10:00:00,123 7 ERROR demo odoo.tests.result: 1 failed, 2 error(s) of 30 tests when loading database 'demo' ",
			TestTally{Run: 30, Failed: 1, Errors: 2}, true},
		{"2026-10-19 10:00:00,123 7 WARNING ? odoo.tests.result: 0 failed, 0 error(s) of 0 tests when loading database 'demo' ",
			TestTally{}, true},
		{"2026-10-19 10:00:00,123 7 INFO demo odoo.tests.stats: sale: 12 tests 1.20s 340 queries ", TestTally{}, false},
		{"2026-10-19 10:00:00,123 7 INFO demo odoo.service.server: 12 post-tests in 1.20s, 340 queries ", TestTally{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseTestTally(tc.line)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseTestTally(%q) = %+v, %v; want %+v, %v", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}
