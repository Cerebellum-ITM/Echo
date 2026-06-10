package repl

import (
	"reflect"
	"testing"
)

func TestMigrationTrackerDetectsAndDedupes(t *testing.T) {
	mt := &migrationTracker{}
	// Three phases of the same module/version collapse into one record.
	mt.observe("2026-06-10 00:09:01,699 12 INFO habitta_prod odoo.modules.migration: module real_state_bits_finance_quote: Running migration [18.0.0.6>] pre-migration")
	mt.observe("2026-06-10 00:09:02,000 12 INFO habitta_prod odoo.modules.migration: module real_state_bits_finance_quote: Running migration [18.0.0.6>] post-migration")
	// A different module is a separate record.
	mt.observe("2026-06-10 00:09:03,000 12 INFO habitta_prod odoo.modules.migration: module sale: Running migration [18.0.1.0] end-migration")
	// Non-migration lines are ignored.
	mt.observe("2026-06-10 00:09:04,000 12 INFO habitta_prod odoo.modules.loading: loading module sale")

	got := mt.migrations()
	want := []migration{
		{module: "real_state_bits_finance_quote", version: "18.0.0.6", phases: []string{"pre", "post"}},
		{module: "sale", version: "18.0.1.0", phases: []string{"end"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrations()\n got: %+v\nwant: %+v", got, want)
	}
}

func TestMigrationTrackerNoMatch(t *testing.T) {
	mt := &migrationTracker{}
	mt.observe("2026-06-10 00:09:01,699 12 INFO db odoo.modules.loading: nothing here")
	if got := mt.migrations(); len(got) != 0 {
		t.Fatalf("expected no migrations, got %+v", got)
	}
}

func TestCollectMigrationsOrderAndTrim(t *testing.T) {
	texts := []string{
		"x odoo.modules.migration: module b: Running migration [1.0>] post-migration",
		"x odoo.modules.migration: module a: Running migration [2.0] pre-migration",
		"x odoo.modules.migration: module b: Running migration [1.0>] pre-migration",
	}
	got := collectMigrations(texts)
	want := []migration{
		{module: "b", version: "1.0", phases: []string{"post", "pre"}},
		{module: "a", version: "2.0", phases: []string{"pre"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectMigrations\n got: %+v\nwant: %+v", got, want)
	}
}
