package cmd

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseRemoteUpdateFlags(t *testing.T) {
	tests := []struct {
		name      string
		rest      []string
		all, i18n bool
		installed bool
		noCkpt    bool
		modules   []string
		wantErr   bool
	}{
		{"explicit modules", []string{"sale", "account"}, false, false, false, false, []string{"sale", "account"}, false},
		{"all + i18n", []string{"--all", "--i18n"}, true, true, false, false, nil, false},
		{"installed picker source", []string{"--installed"}, false, false, true, false, nil, false},
		{"--from value not a module", []string{"--from", "prod", "sale"}, false, false, false, false, []string{"sale"}, false},
		{"--from= and --remote consumed", []string{"--from=prod", "--remote", "sale"}, false, false, false, false, []string{"sale"}, false},
		{"--force consumed", []string{"sale", "--force"}, false, false, false, false, []string{"sale"}, false},
		{"--last rejected", []string{"--last"}, false, false, false, false, nil, true},
		{"unknown flag rejected", []string{"--nope"}, false, false, false, false, nil, true},
		{"--no-checkpoint consumed", []string{"sale", "--no-checkpoint"}, false, false, false, true, []string{"sale"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			all, i18n, installed, noCkpt, modules, err := parseRemoteUpdateFlags(tc.rest)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %v", tc.rest)
				}
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("error should wrap ErrUsage, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if all != tc.all || i18n != tc.i18n || installed != tc.installed || noCkpt != tc.noCkpt || !reflect.DeepEqual(modules, tc.modules) {
				t.Errorf("got all=%v i18n=%v installed=%v noCheckpoint=%v modules=%v; want all=%v i18n=%v installed=%v noCheckpoint=%v modules=%v",
					all, i18n, installed, noCkpt, modules, tc.all, tc.i18n, tc.installed, tc.noCkpt, tc.modules)
			}
		})
	}
}
