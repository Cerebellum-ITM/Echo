package cmd

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseRemoteUninstallFlags(t *testing.T) {
	tests := []struct {
		name      string
		rest      []string
		installed bool
		modules   []string
		wantErr   bool
	}{
		{"explicit modules", []string{"sale", "account"}, false, []string{"sale", "account"}, false},
		{"installed picker source", []string{"--installed"}, true, nil, false},
		{"--from value not a module", []string{"--from", "prod", "sale"}, false, []string{"sale"}, false},
		{"--from= and --remote consumed", []string{"--from=prod", "--remote", "sale"}, false, []string{"sale"}, false},
		{"-E value not a module", []string{"-E", "app/env", "sale"}, false, []string{"sale"}, false},
		{"--force and --json consumed", []string{"sale", "--force", "--json"}, false, []string{"sale"}, false},
		{"--all rejected", []string{"--all"}, false, nil, true},
		{"--last rejected", []string{"--last"}, false, nil, true},
		{"--i18n rejected", []string{"sale", "--i18n"}, false, nil, true},
		{"unknown flag rejected", []string{"--nope"}, false, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			installed, modules, err := parseRemoteUninstallFlags(tc.rest)
			if tc.wantErr {
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("err = %v, want ErrUsage for %v", err, tc.rest)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if installed != tc.installed || !reflect.DeepEqual(modules, tc.modules) {
				t.Errorf("got installed=%v modules=%v; want installed=%v modules=%v",
					installed, modules, tc.installed, tc.modules)
			}
		})
	}
}
