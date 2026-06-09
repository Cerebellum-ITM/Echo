package cmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestEmitResolved(t *testing.T) {
	var got []string
	called := 0
	opts := ModulesOpts{OnResolve: func(r []string) { called++; got = r }}
	emitResolved(opts, []string{"sale", "account"})
	if called != 1 || !reflect.DeepEqual(got, []string{"sale", "account"}) {
		t.Fatalf("OnResolve called=%d got=%v", called, got)
	}
	// No-op (no panic) when OnResolve is nil.
	emitResolved(ModulesOpts{}, []string{"x"})
}

// TestRunUpdateLastNotifiesResolved asserts that `update --last` reports
// the resolved module set (loaded from disk) through OnResolve before the
// Odoo subprocess runs — the start-line fix. The subprocess itself can't
// run without docker, so ComposeCmd points at a missing binary: runOdoo
// fails fast, but OnResolve has already fired by then.
func TestRunUpdateLastNotifiesResolved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const key, db = "abc123", "demo"
	if err := config.SaveLastUpdate(key, db, config.LastUpdate{Modules: []string{"sale", "account"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var resolved []string
	opts := ModulesOpts{
		Cfg: &config.Config{
			ProjectKey:    key,
			DBName:        db,
			OdooContainer: "odoo",
			ComposeCmd:    "/nonexistent-echo-test-binary",
		},
		Root:      t.TempDir(),
		Args:      []string{"--last"},
		StreamOut: func(string) {},
		OnResolve: func(r []string) { resolved = r },
	}
	// The run errors at the (missing) subprocess; we only care that
	// OnResolve fired with the disk-resolved set first.
	_, _ = RunUpdate(context.Background(), opts)
	if !reflect.DeepEqual(resolved, []string{"sale", "account"}) {
		t.Fatalf("OnResolve got %v, want [sale account]", resolved)
	}
}

func TestExtractLevel(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantLevel string
		wantRest  []string
		wantErr   error
	}{
		{"none", []string{"sale", "stock"}, "", []string{"sale", "stock"}, nil},
		{"space form", []string{"sale", "--level", "debug"}, "debug", []string{"sale"}, nil},
		{"equals form", []string{"--level=warn", "sale"}, "warn", []string{"sale"}, nil},
		{"with other flags", []string{"--all", "--level", "error"}, "error", []string{"--all"}, nil},
		{"invalid level", []string{"sale", "--level", "nope"}, "", nil, ErrInvalidLogLevel},
		{"bare level no value", []string{"sale", "--level"}, "", nil, nil}, // err checked below
	}
	for _, c := range cases {
		level, rest, err := extractLevel(c.args)
		if c.name == "bare level no value" {
			if err == nil {
				t.Errorf("%s: expected an error for a valueless --level", c.name)
			}
			continue
		}
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected err %v", c.name, err)
			continue
		}
		if level != c.wantLevel || !reflect.DeepEqual(rest, c.wantRest) {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, level, rest, c.wantLevel, c.wantRest)
		}
	}
}

func TestParseAddonsPath(t *testing.T) {
	cases := []struct {
		name string
		conf string
		want []string
	}{
		{
			name: "single path",
			conf: "[options]\naddons_path = /mnt/extra-addons\n",
			want: []string{"/mnt/extra-addons"},
		},
		{
			name: "multiple comma-separated",
			conf: "addons_path = /mnt/extra-addons,/odoo/addons,/mnt/custom\n",
			want: []string{"/mnt/extra-addons", "/odoo/addons", "/mnt/custom"},
		},
		{
			name: "spaces around entries",
			conf: "addons_path =  /a , /b ,/c \n",
			want: []string{"/a", "/b", "/c"},
		},
		{
			name: "commented line ignored",
			conf: "# addons_path = /should/not/win\naddons_path = /real\n",
			want: []string{"/real"},
		},
		{
			name: "semicolon comment ignored",
			conf: "; addons_path = /nope\naddons_path = /yes\n",
			want: []string{"/yes"},
		},
		{
			name: "key absent",
			conf: "[options]\ndb_host = localhost\n",
			want: nil,
		},
		{
			name: "section header present, key later",
			conf: "[options]\ndb_user = odoo\naddons_path = /mnt/extra-addons\nlog_level = info\n",
			want: []string{"/mnt/extra-addons"},
		},
		{
			name: "empty value",
			conf: "addons_path =\n",
			want: nil,
		},
		{
			name: "trailing comma drops empty",
			conf: "addons_path = /a,/b,\n",
			want: []string{"/a", "/b"},
		},
		{
			name: "enterprise entry skipped by default",
			conf: "addons_path = /odoo/addons,/odoo/enterprise,/mnt/extra-addons\n",
			want: []string{"/odoo/addons", "/mnt/extra-addons"},
		},
		{
			name: "enterprise prefix skipped (enterprise-addons), spaces after commas",
			conf: "addons_path = /mnt/extra-addons, /mnt/addons-sam, /mnt/enterprise-addons, /mnt/addons-client\n",
			want: []string{"/mnt/extra-addons", "/mnt/addons-sam", "/mnt/addons-client"},
		},
		{
			name: "enterprise match is case-insensitive and trailing-slash tolerant",
			conf: "addons_path = /mnt/Enterprise-Addons/,/mnt/custom\n",
			want: []string{"/mnt/custom"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseAddonsPath(c.conf)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseAddonsPath() = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestEqualStrings(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{[]string{"a", "b"}, []string{"b", "a"}, false},
		{[]string{"a"}, []string{"a", "b"}, false},
		{[]string{}, nil, true},
	}
	for _, c := range cases {
		if got := equalStrings(c.a, c.b); got != c.want {
			t.Errorf("equalStrings(%#v, %#v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
