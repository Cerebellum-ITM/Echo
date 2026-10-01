// Package depcheck finds the Python methods, fields and XML ids a new version
// of an Odoo module no longer defines, and maps a remote grep for their uses
// back to the modules that still reference them. It works by regex, not by
// parsing: no class awareness, no signatures, no JS.
package depcheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind is what a symbol is in the module that defines it.
type Kind string

const (
	Method Kind = "method"
	Field  Kind = "field"
	XMLID  Kind = "xmlid"
)

// Symbol is one name a module defines. An XMLID's Name is the module-local
// id, without the module prefix.
type Symbol struct {
	Kind Kind
	Name string
}

// Use is the word a module staying on the server would contain to depend on
// the symbol: the bare name, or <module>.<id> for an XML id.
func (s Symbol) Use(module string) string {
	if s.Kind == XMLID {
		return module + "." + s.Name
	}
	return s.Name
}

// Set is the symbols one module tree defines.
type Set map[Symbol]bool

// ormNames are methods modules override all the time and other modules call
// on the base model anyway, so dropping an override never breaks a caller.
var ormNames = map[string]bool{
	"create": true, "write": true, "unlink": true, "copy": true, "read": true,
	"search": true, "search_read": true, "search_count": true, "browse": true,
	"exists": true, "name_get": true, "name_search": true, "default_get": true,
	"fields_get": true, "onchange": true, "init": true, "_auto_init": true,
	"_compute_display_name": true, "_name_search": true, "_search": true,
	"action_archive": true, "action_unarchive": true, "toggle_active": true,
}

var (
	classRe  = regexp.MustCompile(`^class\s+\w+`)
	methodRe = regexp.MustCompile(`^def\s+(\w+)\s*\(`)
	fieldRe  = regexp.MustCompile(`^(\w+)\s*=\s*fields\.\w+\(`)
	xmlIDRe  = regexp.MustCompile(`<(?:record|template|menuitem|act_window|report)\b[^>]*\bid\s*=\s*["']([^"']+)["']`)
)

// Symbols walks a module directory and returns the methods and fields its
// classes define and the XML ids its data files declare. tests/ and
// migrations/ are skipped: nothing outside them can depend on what they hold.
func Symbols(root string) (Set, error) {
	set := Set{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (d.Name() == "tests" || d.Name() == "migrations") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(p)
		if ext != ".py" && ext != ".xml" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if ext == ".py" {
			pythonSymbols(string(data), set)
		} else {
			xmlSymbols(string(data), set)
		}
		return nil
	})
	return set, err
}

// pythonSymbols adds the defs and field assignments made at a class's body
// indentation; nested functions and module-level code are not members.
func pythonSymbols(src string, set Set) {
	inClass, bodyIndent := false, ""
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		if indent == "" {
			inClass, bodyIndent = classRe.MatchString(line), ""
			continue
		}
		if !inClass {
			continue
		}
		if bodyIndent == "" {
			bodyIndent = indent
		}
		if indent != bodyIndent {
			continue
		}
		if m := methodRe.FindStringSubmatch(trimmed); m != nil {
			set[Symbol{Method, m[1]}] = true
		} else if m := fieldRe.FindStringSubmatch(trimmed); m != nil {
			set[Symbol{Field, m[1]}] = true
		}
	}
}

// xmlSymbols adds the ids of the records a data file declares. A dotted id
// overrides another module's record, so it is not this module's to remove.
func xmlSymbols(src string, set Set) {
	for _, m := range xmlIDRe.FindAllStringSubmatch(src, -1) {
		if !strings.Contains(m[1], ".") {
			set[Symbol{XMLID, m[1]}] = true
		}
	}
}

// Removed lists, sorted, the symbols old defines and new does not, minus the
// ORM overrides, dunder names, and methods or fields keep still defines (a
// name moved to another module shipped in the same run). An XML id moved to
// another module changes its qualified name, so keep never covers one.
func Removed(old, new, keep Set) []Symbol {
	var out []Symbol
	for s := range old {
		if new[s] {
			continue
		}
		if s.Kind != XMLID && (keep[s] || ormNames[s.Name] || isDunder(s.Name)) {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func isDunder(name string) bool {
	return len(name) > 4 && strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")
}

// GrepPattern is the extended regex, for `grep -wE`, that matches a use of any
// of the removed symbols of module.
func GrepPattern(removed []Symbol, module string) string {
	alts := make([]string, len(removed))
	for i, s := range removed {
		alts[i] = regexp.QuoteMeta(s.Use(module))
	}
	if len(alts) == 1 {
		return alts[0]
	}
	return "(" + strings.Join(alts, "|") + ")"
}

// Defines reports whether a source line is the definition of the method or
// field s, which makes the module holding it a provider, not a user.
func Defines(s Symbol, line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	switch s.Kind {
	case Method:
		m := methodRe.FindStringSubmatch(trimmed)
		return m != nil && m[1] == s.Name
	case Field:
		m := fieldRe.FindStringSubmatch(trimmed)
		return m != nil && m[1] == s.Name
	}
	return false
}

// Ref is one line of a module that matched the grep. File is relative to the
// directory holding the module, so it starts with the module's name.
type Ref struct {
	Module string
	File   string
	Line   int
	Text   string
}

// ParseGrep maps `grep -rnH` output (path:line:text) back to the module each
// path belongs to. roots maps every searched module directory, as given to
// grep, to the module's name; lines outside them are dropped.
func ParseGrep(out string, roots map[string]string) []Ref {
	var refs []Ref
	for _, line := range strings.Split(out, "\n") {
		for dir, module := range roots {
			rest, ok := strings.CutPrefix(line, dir+"/")
			if !ok {
				continue
			}
			file, rest, ok1 := strings.Cut(rest, ":")
			num, text, ok2 := strings.Cut(rest, ":")
			n, err := strconv.Atoi(num)
			if ok1 && ok2 && err == nil {
				refs = append(refs, Ref{Module: module, File: module + "/" + file, Line: n, Text: text})
			}
			break
		}
	}
	return refs
}
