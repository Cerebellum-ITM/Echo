package repl

import (
	"reflect"
	"sort"
	"testing"
)

func TestRegistryUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range Registry {
		if seen[name] {
			t.Fatalf("duplicate command in Registry: %s", name)
		}
		seen[name] = true
	}
}

func TestRegistryMatchesHelp(t *testing.T) {
	want := sortedSet(Registry)
	got := sortedSet(helpCommandNames())
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Registry vs help command names mismatch\nregistry: %v\nhelp:     %v", want, got)
	}
}

func TestRegistryMatchesDispatch(t *testing.T) {
	want := sortedSet(filter(Registry, func(s string) bool {
		return s != "exit" && s != "quit"
	}))
	got := sortedSet(dispatchNames)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Registry (minus exit/quit) vs dispatchNames mismatch\nregistry: %v\ndispatch: %v", want, got)
	}
}

func TestMatchPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"zz", nil},
		{"in", []string{"init", "install"}},
		{"db-", []string{"db-backup", "db-restore", "db-drop", "db-list"}},
		{"install", []string{"install"}},
	}
	for _, tc := range cases {
		got := matchPrefix(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("matchPrefix(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLongestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"install"}, "install"},
		{[]string{"db-backup", "db-restore", "db-drop", "db-list"}, "db-"},
		{[]string{"db-backup", "db-restore"}, "db-re"[:3]}, // "db-"
		{[]string{"up", "update", "uninstall"}, "u"},
		{[]string{"foo", "bar"}, ""},
	}
	for _, tc := range cases {
		got := longestCommonPrefix(tc.in)
		if got != tc.want {
			t.Errorf("longestCommonPrefix(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func sortedSet(s []string) []string {
	cp := append([]string(nil), s...)
	sort.Strings(cp)
	return cp
}

func filter(s []string, keep func(string) bool) []string {
	var out []string
	for _, v := range s {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}
