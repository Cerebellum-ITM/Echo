package cmd

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseMD5Sums(t *testing.T) {
	// coreutils uses two spaces; BusyBox one; binary mode prefixes '*'.
	out := "" +
		"d41d8cd98f00b204e9800998ecf8427e  /mnt/extra-addons/sale/__init__.py\n" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa /mnt/extra-addons/sale/models/x.py\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb *hello\n" + // outside prefix → dropped
		"cccccccccccccccccccccccccccccccc  /mnt/extra-addons/sale/__pycache__/x.pyc\n" + // noise → dropped
		"\n"
	got := parseMD5Sums(out, "/mnt/extra-addons/sale/")
	want := map[string]string{
		"__init__.py": "d41d8cd98f00b204e9800998ecf8427e",
		"models/x.py": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMD5Sums = %v, want %v", got, want)
	}
}

func TestDiffModuleSets(t *testing.T) {
	local := map[string]string{
		"a.py":   "h1",
		"b.py":   "h2",   // changed
		"c.py":   "same", // equal
		"new.py": "h4",   // added
	}
	container := map[string]string{
		"a.py":    "h1",        // equal
		"b.py":    "different", // changed
		"c.py":    "same",      // equal
		"gone.py": "h5",        // missing (container only)
	}
	rows, equal := diffModuleSets(local, container)
	if equal != 2 {
		t.Fatalf("equal = %d, want 2", equal)
	}
	want := []FileStatus{
		{"b.py", "changed"},
		{"new.py", "added"},
		{"gone.py", "missing"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
}

func TestDiffModuleSetsAllEqual(t *testing.T) {
	m := map[string]string{"a.py": "x", "b.py": "y"}
	rows, equal := diffModuleSets(m, m)
	if len(rows) != 0 {
		t.Fatalf("expected no rows for identical sets, got %v", rows)
	}
	if equal != 2 {
		t.Fatalf("equal = %d, want 2", equal)
	}
}

func TestLocalModuleHashes(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("__manifest__.py", "{'name': 'x'}")
	writeFile("models/sale.py", "class Sale: pass")
	writeFile("__pycache__/sale.cpython-311.pyc", "junk") // skipped

	got, err := localModuleHashes(dir)
	if err != nil {
		t.Fatalf("localModuleHashes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 hashed files (pycache skipped), got %d: %v", len(got), got)
	}
	wantManifest := fmt.Sprintf("%x", md5.Sum([]byte("{'name': 'x'}")))
	if got["__manifest__.py"] != wantManifest {
		t.Fatalf("manifest hash = %q, want %q", got["__manifest__.py"], wantManifest)
	}
	if _, ok := got["__pycache__/sale.cpython-311.pyc"]; ok {
		t.Fatal("__pycache__ file should have been skipped")
	}
}

func TestParseCompareArgsAll(t *testing.T) {
	module, copyFlag, all, from, remote, err := parseCompareArgs([]string{"sale", "--all", "--from", "prod", "--copy"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if module != "sale" || !all || !copyFlag || from != "prod" || remote {
		t.Fatalf("parse = (%q, copy=%v, all=%v, from=%q, remote=%v)", module, copyFlag, all, from, remote)
	}
}
