package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestBatBinary(t *testing.T) {
	orig := lookPath
	defer func() { lookPath = orig }()

	lookPath = func(name string) (string, error) {
		if name == "batcat" {
			return "/usr/bin/batcat", nil
		}
		return "", os.ErrNotExist
	}
	if got := batBinary(); got != "batcat" {
		t.Fatalf("batBinary() = %q, want batcat", got)
	}

	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if got := batBinary(); got != "" {
		t.Fatalf("batBinary() = %q, want empty", got)
	}
}

func TestSkipViewPath(t *testing.T) {
	cases := map[string]bool{
		"models/sale.py":             false,
		"__pycache__/sale.cpython.pyc": true,
		"models/__pycache__/x.pyc":   true,
		"models/sale.pyc":            true,
		".git/config":                true,
		"views/sale.xml":             false,
	}
	for in, want := range cases {
		if got := skipViewPath(in); got != want {
			t.Fatalf("skipViewPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestModuleFilesHost(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "addons", "sale")
	mustWrite(t, filepath.Join(mod, "__manifest__.py"), "{}")
	mustWrite(t, filepath.Join(mod, "models", "sale.py"), "x")
	mustWrite(t, filepath.Join(mod, "views", "sale.xml"), "<x/>")
	mustWrite(t, filepath.Join(mod, "__pycache__", "sale.pyc"), "junk")
	mustWrite(t, filepath.Join(mod, "models", "sale.pyc"), "junk")

	opts := ViewOpts{
		Cfg:  &config.Config{AddonsMode: addonsModeHost, AddonsPaths: []string{"addons"}},
		Root: root,
	}
	files, err := moduleFiles(context.Background(), opts, "addons", "sale", false)
	if err != nil {
		t.Fatalf("moduleFiles: %v", err)
	}
	want := []string{"__manifest__.py", "models/sale.py", "views/sale.xml"}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files = %v, want %v", files, want)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
