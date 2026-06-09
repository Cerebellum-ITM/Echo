package odoo

import (
	"reflect"
	"testing"
)

func TestWithLogLevel(t *testing.T) {
	base := Cmd{"odoo", "-u", "sale", "--stop-after-init"}

	if got := WithLogLevel(base, ""); !reflect.DeepEqual(got, base) {
		t.Errorf("empty level should be a no-op, got %v", got)
	}

	got := WithLogLevel(base, "debug")
	want := Cmd{"odoo", "-u", "sale", "--stop-after-init", "--log-level=debug"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithLogLevel(base, debug) = %v, want %v", got, want)
	}
}
