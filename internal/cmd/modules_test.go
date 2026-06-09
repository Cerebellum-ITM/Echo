package cmd

import (
	"reflect"
	"testing"
)

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
			name: "enterprise match is case-insensitive and trailing-slash tolerant",
			conf: "addons_path = /mnt/Enterprise/,/mnt/custom\n",
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
