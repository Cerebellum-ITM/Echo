package cmd

import (
	"reflect"
	"testing"
)

func TestParseTestArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		modules []string
		tags    string
		update  bool
		from    string
		remote  bool
		wantErr bool
	}{
		{
			name:    "local single module",
			args:    []string{"sale"},
			modules: []string{"sale"},
		},
		{
			name:    "local multi module with update",
			args:    []string{"sale", "account", "--update"},
			modules: []string{"sale", "account"},
			update:  true,
		},
		{
			name:    "tags override",
			args:    []string{"sale", "--tags", ":TestX.test_y"},
			modules: []string{"sale"},
			tags:    ":TestX.test_y",
		},
		{
			name:    "tags equals form",
			args:    []string{"sale", "--tags=:TestX"},
			modules: []string{"sale"},
			tags:    ":TestX",
		},
		{
			name:    "from strips value token, not a module",
			args:    []string{"sale", "--from", "prod"},
			modules: []string{"sale"},
			from:    "prod",
		},
		{
			name:    "from equals form",
			args:    []string{"sale", "--from=prod"},
			modules: []string{"sale"},
			from:    "prod",
		},
		{
			name:    "remote flag interleaved before module",
			args:    []string{"--remote", "sale", "--update"},
			modules: []string{"sale"},
			update:  true,
			remote:  true,
		},
		{
			name:    "remote with tags and update",
			args:    []string{"sale", "account", "--from", "prod", "--tags", ":T", "--update"},
			modules: []string{"sale", "account"},
			tags:    ":T",
			update:  true,
			from:    "prod",
		},
		{
			name:    "no modules, bare remote",
			args:    []string{"--remote"},
			modules: nil,
			remote:  true,
		},
		{
			name:    "unknown flag ignored",
			args:    []string{"sale", "--whatever"},
			modules: []string{"sale"},
		},
		{
			name:    "tags without value errors",
			args:    []string{"sale", "--tags"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			modules, tags, update, from, remote, err := parseTestArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(modules, tc.modules) {
				t.Errorf("modules = %#v, want %#v", modules, tc.modules)
			}
			if tags != tc.tags {
				t.Errorf("tags = %q, want %q", tags, tc.tags)
			}
			if update != tc.update {
				t.Errorf("update = %v, want %v", update, tc.update)
			}
			if from != tc.from {
				t.Errorf("from = %q, want %q", from, tc.from)
			}
			if remote != tc.remote {
				t.Errorf("remote = %v, want %v", remote, tc.remote)
			}
		})
	}
}
