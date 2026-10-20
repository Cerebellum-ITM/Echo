package odoo

import (
	"reflect"
	"strings"
	"testing"
)

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

// testRunLog is a `test` run as Odoo 17, 18 and 19 print it; result.py,
// suite.py and the netsvc format are byte-identical across the three.
// OdooTestResult.logError logs "%s: %s\n%s" (flavour, description,
// traceback) on the test module's logger, after an INFO line of '='; a
// setUpClass failure comes from suite._ErrorHolder, logged on
// odoo.tests.suite. Python's assertEqual on strings appends a diff whose
// lines are not indented.
const testRunLog = `2026-10-19 10:00:00,100 7 INFO demo odoo.modules.loading: loading 82 modules...
2026-10-19 10:00:00,200 7 WARNING demo odoo.fields: Field sale.order.line.x_note: unknown parameter 'tracking'
2026-10-19 10:00:01,000 7 INFO demo odoo.addons.sale_custom.tests.test_order: Starting TestOrder.test_confirm ...
2026-10-19 10:00:01,050 7 INFO demo odoo.addons.sale_custom.tests.test_order: ======================================================================
2026-10-19 10:00:01,050 7 ERROR demo odoo.addons.sale_custom.tests.test_order: FAIL: TestOrder.test_confirm
Traceback (most recent call last):
  File "/mnt/extra-addons/sale_custom/tests/test_order.py", line 88, in test_confirm
    self.assertEqual(order.state, 'sale')
AssertionError: 'draft' != 'sale'
- draft
+ sale

2026-10-19 10:00:01,100 7 INFO demo odoo.addons.sale_custom.tests.test_order: Starting TestOrder.test_cancel ...
2026-10-19 10:00:01,150 7 ERROR demo odoo.addons.sale_custom.tests.test_order: ERROR: TestOrder.test_cancel
Traceback (most recent call last):
  File "/mnt/extra-addons/sale_custom/tests/test_order.py", line 95, in test_cancel
    order._action_cancel()
  File "/mnt/extra-addons/sale_custom/models/sale_order.py", line 42, in _action_cancel
    raise UserError("locked")
odoo.exceptions.UserError: locked
2026-10-19 10:00:01,200 7 ERROR demo odoo.tests.suite: ERROR: setUpClass (odoo.addons.stock_custom.tests.test_move.TestMove)
Traceback (most recent call last):
  File "/mnt/extra-addons/stock_custom/tests/test_move.py", line 12, in setUpClass
    cls.product = cls.env.ref('stock_custom.missing')
ValueError: External ID not found in the system: stock_custom.missing
2026-10-19 10:00:01,300 7 ERROR demo odoo.sql_db: bad query: INSERT INTO "res_partner" ("ref") VALUES ('A')
ERROR: duplicate key value violates unique constraint "res_partner_ref_uniq"
DETAIL:  Key (ref)=(A) already exists.

2026-10-19 10:00:01,400 7 ERROR demo odoo.modules.loading: At least one test failed when loading the modules.
2026-10-19 10:00:02,000 7 ERROR demo odoo.tests.result: 1 failed, 2 error(s) of 5 tests when loading database 'demo'
`

func feedAll(text string) *Outcome {
	var o Outcome
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		o.Feed(line)
	}
	return &o
}

func TestOutcomeTestRun(t *testing.T) {
	o := feedAll(testRunLog)

	if !o.Tallied || o.Tests != (TestTally{Run: 5, Failed: 1, Errors: 2}) {
		t.Errorf("tally = %+v (seen %v)", o.Tests, o.Tallied)
	}
	want := []TestFailure{
		{Kind: "FAIL", Test: "TestOrder.test_confirm", Module: "sale_custom",
			File: "/mnt/extra-addons/sale_custom/tests/test_order.py", Line: 88,
			Message: "AssertionError: 'draft' != 'sale'"},
		{Kind: "ERROR", Test: "TestOrder.test_cancel", Module: "sale_custom",
			File: "/mnt/extra-addons/sale_custom/models/sale_order.py", Line: 42,
			Message: "odoo.exceptions.UserError: locked"},
		{Kind: "ERROR", Test: "TestMove.setUpClass", Module: "stock_custom",
			File: "/mnt/extra-addons/stock_custom/tests/test_move.py", Line: 12,
			Message: "ValueError: External ID not found in the system: stock_custom.missing"},
	}
	if len(o.Failures) != len(want) {
		t.Fatalf("failures = %d, want %d: %+v", len(o.Failures), len(want), o.Failures)
	}
	for i, w := range want {
		if *o.Failures[i] != w {
			t.Errorf("failure %d = %+v\nwant %+v", i, *o.Failures[i], w)
		}
	}

	if len(o.Errors) != 2 {
		t.Fatalf("errors = %d, want the sql_db and loading errors only: %+v", len(o.Errors), o.Errors)
	}
	sql := o.Errors[0]
	if sql.Logger != "odoo.sql_db" || sql.Exception != "" || len(sql.Frames) != 0 {
		t.Errorf("a traceback-less error got an exception or frames: %+v", sql)
	}
	if o.Errors[1].Message != "At least one test failed when loading the modules." {
		t.Errorf("message = %q", o.Errors[1].Message)
	}
	if len(o.Warnings) != 1 || o.Warnings[0].Logger != "odoo.fields" {
		t.Errorf("warnings = %+v", o.Warnings)
	}
}

// updateFailureLog is a failing `-u` run: the registry load raises, the
// exception is logged with exc_info (traceback appended after the message),
// and the server logs the chained failure as CRITICAL.
const updateFailureLog = `2026-10-19 11:00:00,000 9 INFO demo odoo.modules.loading: loading 82 modules...
2026-10-19 11:00:01,000 9 ERROR demo odoo.modules.registry: Failed to load registry
Traceback (most recent call last):
  File "/usr/lib/python3/dist-packages/odoo/modules/registry.py", line 89, in new
    odoo.modules.load_modules(registry, force_demo, status, update_module)
  File "/mnt/extra-addons/sale_custom/models/sale_order.py", line 30, in _compute_total
    raise ValueError("bad total")
ValueError: bad total
2026-10-19 11:00:01,100 9 CRITICAL demo odoo.service.server: Failed to initialize database ` + "`demo`" + `.
Traceback (most recent call last):
  File "/mnt/extra-addons/sale_custom/models/sale_order.py", line 30, in _compute_total
    raise ValueError("bad total")
ValueError: bad total

During handling of the above exception, another exception occurred:

Traceback (most recent call last):
  File "/usr/lib/python3/dist-packages/odoo/service/server.py", line 1316, in preload_registries
    registry = Registry.new(dbname, update_module=update_module)
psycopg2.errors.UniqueViolation: duplicate key value violates unique constraint "x"
DETAIL:  Key (name)=(a) already exists.
`

func TestOutcomeUpdateFailure(t *testing.T) {
	o := feedAll(updateFailureLog)
	if o.Tallied || len(o.Failures) != 0 {
		t.Errorf("an update run reported tests: %+v %+v", o.Tests, o.Failures)
	}
	if len(o.Errors) != 2 {
		t.Fatalf("errors = %d: %+v", len(o.Errors), o.Errors)
	}
	reg := o.Errors[0]
	wantFrames := []Frame{
		{File: "/usr/lib/python3/dist-packages/odoo/modules/registry.py", Line: 89, Func: "new"},
		{File: "/mnt/extra-addons/sale_custom/models/sale_order.py", Line: 30, Func: "_compute_total"},
	}
	if reg.Logger != "odoo.modules.registry" || reg.Message != "Failed to load registry" ||
		reg.Exception != "ValueError: bad total" || !reflect.DeepEqual(reg.Frames, wantFrames) {
		t.Errorf("registry error = %+v", reg)
	}
	if reg.Time != "2026-10-19 11:00:01,000" || reg.DB != "demo" || len(reg.Lines) != 7 {
		t.Errorf("registry error header: time %q db %q lines %d", reg.Time, reg.DB, len(reg.Lines))
	}
	crit := o.Errors[1]
	if crit.Exception != `psycopg2.errors.UniqueViolation: duplicate key value violates unique constraint "x"` {
		t.Errorf("chained exception = %q, want the last one without its DETAIL line", crit.Exception)
	}
	if len(crit.Frames) != 2 {
		t.Errorf("chained frames = %d, want both tracebacks' frames", len(crit.Frames))
	}
}

func TestOutcomeCapsWarnings(t *testing.T) {
	var o Outcome
	for range maxWarnings + 7 {
		o.Feed("2026-10-19 10:00:00,000 7 WARNING demo odoo.models: deprecated ")
	}
	if len(o.Warnings) != maxWarnings || o.WarningsTruncated != 7 {
		t.Errorf("warnings = %d truncated = %d", len(o.Warnings), o.WarningsTruncated)
	}
}

func TestOutcomeTalliesAddUp(t *testing.T) {
	var o Outcome
	o.Feed("2026-10-19 10:00:00,000 7 INFO a odoo.tests.result: 0 failed, 0 error(s) of 3 tests when loading database 'a' ")
	o.Feed("2026-10-19 10:00:00,000 7 ERROR b odoo.tests.result: 1 failed, 0 error(s) of 4 tests when loading database 'b' ")
	if o.Tests != (TestTally{Run: 7, Failed: 1}) || len(o.Errors) != 0 {
		t.Errorf("tests = %+v errors = %+v", o.Tests, o.Errors)
	}
}

func TestOutcomeJoinsMessageAfterColon(t *testing.T) {
	var o Outcome
	for _, line := range []string{
		"2026-10-20 15:15:31,367 2556 ERROR muutrade-dev odoo.tests.suite: ERROR: setUpClass (odoo.addons.sale.tests.test_sale_flow.TestSaleFlow)",
		"Traceback (most recent call last):",
		`  File "/usr/lib/python3/dist-packages/odoo/addons/sale/tests/test_sale_flow.py", line 9, in setUpClass`,
		"    super().setUpClass()",
		`  File "/usr/lib/python3/dist-packages/odoo/addons/base_vat/models/res_partner.py", line 161, in _run_vat_checks`,
		"    raise ValidationError(msg)",
		"odoo.exceptions.ValidationError: ",
		"Parece que el número VAT [MUU140617ES0] para contacto [MUUTRADE] no es válido.  ",
		"Nota: El formato esperado es BE0477472701",
	} {
		o.Feed(line)
	}
	if len(o.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(o.Failures))
	}
	want := "odoo.exceptions.ValidationError: Parece que el número VAT [MUU140617ES0] para contacto [MUUTRADE] no es válido."
	if got := o.Failures[0].Message; got != want {
		t.Errorf("message = %q\nwant      %q", got, want)
	}
}
