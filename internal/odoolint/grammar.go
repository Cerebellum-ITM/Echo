package odoolint

import (
	"embed"
	"sort"
	"strings"
)

// grammarFS holds Odoo's own data-file grammar, vendored from the server
// source. It is the file odoo/tools/convert.py loads and validates every
// manifest-listed XML file against, so validating with it locally
// reproduces the loader's verdict rather than approximating it.
//
// It is embedded rather than read from a local Odoo checkout for two
// reasons that matter more than correctness-in-principle: a checkout
// dependency degrades to well-formedness-only on every machine that never
// downloaded Odoo — exactly the machine where nobody notices the gap —
// and fetching it from the target instance would make an offline check,
// runnable from a save hook, depend on the network.
//
//go:embed grammar/*.rng
var grammarFS embed.FS

// grammarByMajor maps an Odoo major to its grammar file.
//
// All three entries point at the same file because the grammar is
// byte-identical across 17.0, 18.0 and 19.0 (11,407 bytes, sha256
// eca95216…). The map, not a directory per major, is the extension
// point: the day a major diverges, a second file drops in and one entry
// changes. Three copies of one file would be version support in
// appearance only.
var grammarByMajor = map[string]string{
	"17": "grammar/import_xml.rng",
	"18": "grammar/import_xml.rng",
	"19": "grammar/import_xml.rng",
}

// Grammar returns the embedded grammar for an Odoo major.
//
// used names the major whose grammar was actually loaded and exact
// reports whether it matched the request. An unrecognised major falls
// back to the newest embedded grammar — the caller is expected to warn,
// because "validated against a neighbouring major's grammar" is a
// caveat, not a wrong answer.
func Grammar(major string) (data []byte, used string, exact bool) {
	major = normalizeMajor(major)
	if name, ok := grammarByMajor[major]; ok {
		if b, err := grammarFS.ReadFile(name); err == nil {
			return b, major, true
		}
	}
	newest := newestMajor()
	b, err := grammarFS.ReadFile(grammarByMajor[newest])
	if err != nil {
		return nil, "", false
	}
	return b, newest, false
}

// normalizeMajor reduces a configured Odoo version ("18", "18.0",
// "saas-18.2") to the major key the grammar map uses.
func normalizeMajor(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "saas-")
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}
	return v
}

// SupportedMajors lists the majors with an embedded grammar, ascending.
func SupportedMajors() []string {
	out := make([]string, 0, len(grammarByMajor))
	for m := range grammarByMajor {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// newestMajor returns the highest major with an embedded grammar.
func newestMajor() string {
	majors := SupportedMajors()
	return majors[len(majors)-1]
}
