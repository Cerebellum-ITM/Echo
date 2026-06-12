package repl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPythonScriptsIn(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.py", "b.py", "notes.txt", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "pkg.py"), 0o755); err != nil { // a dir that ends in .py
		t.Fatal(err)
	}

	got, err := pythonScriptsIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 .py files, got %v", got)
	}
	set := map[string]bool{got[0]: true, got[1]: true}
	if !set["a.py"] || !set["b.py"] {
		t.Fatalf("expected a.py and b.py, got %v", got)
	}
	for _, n := range got {
		if n == "notes.txt" || n == "README.md" || n == "pkg.py" {
			t.Fatalf("non-.py file or directory leaked into list: %v", got)
		}
	}
}

func TestScriptOutputLines(t *testing.T) {
	lines := []Line{
		{Kind: "info", Text: "2026-06-11 23:37:51,280 181 INFO ? odoo: Odoo version 18.0-20241118"},
		{Kind: "info", Text: "2026-06-11 23:37:55,084 181 INFO habitta_prod odoo.modules.registry: Registry loaded in 3.484s"},
		{Kind: "out", Text: ""}, // blank emitted around the result
		{Kind: "out", Text: "190"},
		{Kind: "out", Text: ""},
	}
	got := scriptOutputLines(lines)
	if len(got) != 1 || got[0].Text != "190" {
		t.Fatalf("expected only the script result [190], got %v", got)
	}
}

func TestScriptOutputLinesKeepsMultiline(t *testing.T) {
	lines := []Line{
		{Kind: "info", Text: "2026-06-11 23:37:55,084 181 INFO db odoo.modules.loading: Modules loaded."},
		{Kind: "out", Text: "row 1"},
		{Kind: "out", Text: "row 2"},
	}
	got := scriptOutputLines(lines)
	if len(got) != 2 || got[0].Text != "row 1" || got[1].Text != "row 2" {
		t.Fatalf("expected both result rows, got %v", got)
	}
}

func TestResolveScriptArg(t *testing.T) {
	scriptsDir := t.TempDir()
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptsDir, "investigar.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(projectDir, "tools")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "fix.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("bare name resolves under scriptsDir", func(t *testing.T) {
		got, err := resolveScriptArg("investigar.py", scriptsDir, projectDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(scriptsDir, "investigar.py") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("path with separator resolves under projectDir", func(t *testing.T) {
		got, err := resolveScriptArg(filepath.Join("tools", "fix.py"), scriptsDir, projectDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(subDir, "fix.py") {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("non-.py rejected", func(t *testing.T) {
		if _, err := resolveScriptArg("data.csv", scriptsDir, projectDir); err == nil {
			t.Fatal("expected error for non-.py argument")
		}
	})

	t.Run("missing file rejected", func(t *testing.T) {
		if _, err := resolveScriptArg("ghost.py", scriptsDir, projectDir); err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}
