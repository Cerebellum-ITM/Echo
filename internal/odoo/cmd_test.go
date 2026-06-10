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

func TestWithI18nOverwrite(t *testing.T) {
	base := Cmd{"odoo", "-u", "sale", "--stop-after-init"}

	if got := WithI18nOverwrite(base, false); !reflect.DeepEqual(got, base) {
		t.Errorf("off should be a no-op, got %v", got)
	}

	got := WithI18nOverwrite(base, true)
	want := Cmd{"odoo", "-u", "sale", "--stop-after-init", "--i18n-overwrite"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithI18nOverwrite(base, true) = %v, want %v", got, want)
	}
}
