// Package odoolint checks Odoo XML the way the server does, offline.
//
// It exists because two classes of mechanical failure abort a whole
// registry load — a `--` inside an XML comment and markup the data
// loader's grammar refuses — and both cost a deploy round trip to
// discover. Both are detectable locally in milliseconds.
//
// The package is a pure library: it takes paths and returns findings. It
// prints nothing and reads no config, because it has three consumers (the
// `lint` command, an editor/git hook, and `deploy`'s pre-flight) and none
// of them may own the logic.
//
// What a clean result means: "this does not break on markup". It does not
// mean "this loads". Semantic failure — an xpath with no anchor, a ref to
// a missing xml_id, a field that is not on the model — needs a real
// registry and is out of scope.
package odoolint

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind is a finding's severity.
//
// The split is not cosmetic: it is what keeps the linter from being
// stricter than the server. The data loader only ever reads files a
// manifest lists, so a defect anywhere else is information, not a
// failure — and a pre-flight that blocked on it would refuse deploys
// Odoo would have accepted.
type Kind string

const (
	// KindErr marks a defect the loader will hit: the file is listed in
	// a manifest's data/demo and the defect aborts the load.
	KindErr Kind = "err"
	// KindWarn marks a defect the loader will not hit on this run,
	// because no manifest lists the file.
	KindWarn Kind = "warn"
)

// Rule identifiers, stable enough to key on from a hook or CI.
const (
	// RuleSyntax is a well-formedness defect in the file itself.
	RuleSyntax = "xml-syntax"
	// RuleEmbedded is a well-formedness defect inside a sub-document
	// (a CDATA body or an entity-escaped arch), reported at the
	// containing file's line.
	RuleEmbedded = "xml-syntax-embedded"
	// RuleSchema is markup that parses but that Odoo's data-file
	// grammar refuses.
	RuleSchema = "odoo-schema"
)

// Finding is one defect, located in the file the user edits — never in a
// decoded fragment, even when that is where the parse failed.
type Finding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Kind    Kind   `json:"kind"`
	Rule    string `json:"rule"`
	Message string `json:"message"`

	// fromLibxml2 marks a finding produced by xmllint rather than by
	// encoding/xml. It only decides which of two reports of the same
	// defect survives dedupe — libxml2's, since that is the wording the
	// server prints.
	fromLibxml2 bool
}

// Options carries everything Check needs from the outside world, so the
// library itself stays free of config and process lookups.
type Options struct {
	// Grammar is the RelaxNG document to validate data files against.
	// Nil skips the schema pass.
	Grammar []byte

	// ManifestSet holds the absolute paths listed in some module's
	// manifest data/demo. Membership promotes a finding to KindErr.
	ManifestSet map[string]bool

	// Xmllint is the resolved xmllint binary. Empty means absent: the
	// Go pass still runs, the libxml2 passes are skipped.
	Xmllint string
}

// Result is the outcome of a run: the findings plus what was actually
// checked, so a caller can be honest about partial coverage.
type Result struct {
	Findings []Finding
	// Files is the number of .xml files examined.
	Files int
	// SkippedPasses names the passes that did not run (because
	// xmllint or the grammar was unavailable). A caller must surface
	// these: a degraded run that reports "clean" is a lie.
	SkippedPasses []string
}

// Errors returns the count of blocking findings.
func (r Result) Errors() int {
	n := 0
	for _, f := range r.Findings {
		if f.Kind == KindErr {
			n++
		}
	}
	return n
}

// Warnings returns the count of non-blocking findings.
func (r Result) Warnings() int { return len(r.Findings) - r.Errors() }

// Check runs every available pass over paths and returns the findings,
// sorted by file then line so output is stable across runs.
//
// A path may be a directory (walked for .xml files) or a single file
// (checked as-is, whatever its extension — a hook passes what the user
// just saved).
func Check(paths []string, opt Options) (Result, error) {
	files, err := collect(paths)
	if err != nil {
		return Result{}, err
	}
	res := Result{Files: len(files)}

	// Pass 1: encoding/xml, on every file. This is the floor — compiled
	// in, so it cannot be skipped on any platform Echo cross-compiles
	// for, and it alone covers the failures that have actually cost
	// deploys.
	var wellFormed []string
	for _, f := range files {
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			continue // unreadable is not a markup defect
		}
		fs := syntaxFindings(f, data)
		res.Findings = append(res.Findings, fs...)
		if len(fs) == 0 {
			wellFormed = append(wellFormed, f)
		}
	}

	// Passes 2 and 3 both need xmllint. Without it the Go pass stands
	// alone and the caller is told exactly what it is missing.
	if opt.Xmllint == "" {
		res.SkippedPasses = append(res.SkippedPasses, "libxml2-syntax")
		if opt.Grammar != nil {
			res.SkippedPasses = append(res.SkippedPasses, "odoo-schema")
		}
		res.Findings = applySeverity(res.Findings, opt.ManifestSet)
		sortFindings(res.Findings)
		return res, nil
	}

	// Only files that survived the Go pass are worth handing to
	// libxml2: a file that already failed would just report the same
	// defect twice, in two dialects.
	schema, syntaxOnly := splitBySchemaEligibility(wellFormed, opt.Grammar != nil)

	if opt.Grammar == nil {
		res.SkippedPasses = append(res.SkippedPasses, "odoo-schema")
	}

	xf, xerr := runXmllint(opt.Xmllint, opt.Grammar, schema, syntaxOnly)
	if xerr != nil {
		return Result{}, xerr
	}
	res.Findings = append(res.Findings, xf...)

	res.Findings = dedupe(res.Findings)
	res.Findings = applySeverity(res.Findings, opt.ManifestSet)
	sortFindings(res.Findings)
	return res, nil
}

// splitBySchemaEligibility partitions files into the ones the data
// loader reads — and that therefore get the grammar — and the rest,
// which only get well-formedness.
//
// The loader reads files a manifest lists, and those always have an
// <odoo> or <openerp> root. Anything under static/ is served to a
// browser, never parsed by convert.py.
func splitBySchemaEligibility(files []string, haveGrammar bool) (schema, syntaxOnly []string) {
	for _, f := range files {
		if haveGrammar && isDataFile(f) {
			schema = append(schema, f)
			continue
		}
		syntaxOnly = append(syntaxOnly, f)
	}
	return schema, syntaxOnly
}

// isDataFile reports whether f looks like a file the Odoo data loader
// reads: an <odoo>/<openerp> root, outside static/.
func isDataFile(f string) bool {
	if underStatic(f) {
		return false
	}
	data, err := os.ReadFile(f)
	if err != nil {
		return false
	}
	switch rootElement(data) {
	case "odoo", "openerp":
		return true
	}
	return false
}

// underStatic reports whether path lies inside a static/ directory.
func underStatic(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == "static" {
			return true
		}
	}
	return false
}

// applySeverity assigns each finding its Kind. A finding is blocking
// only when the loader would actually reach the file — that is, when a
// manifest lists it.
func applySeverity(findings []Finding, manifest map[string]bool) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		f.Kind = KindWarn
		if manifest[absOrSelf(f.File)] {
			f.Kind = KindErr
		}
		out = append(out, f)
	}
	return out
}

// absOrSelf returns the absolute form of path, or path unchanged when it
// cannot be resolved.
func absOrSelf(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// dedupe collapses findings the Go pass and libxml2 both reported. The
// libxml2 message wins, because it is the one the server will print.
func dedupe(findings []Finding) []Finding {
	type key struct {
		file string
		line int
		rule string
	}
	idx := map[key]int{}
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		k := key{f.File, f.Line, f.Rule}
		if at, ok := idx[k]; ok {
			if f.fromLibxml2 {
				out[at] = f
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, f)
	}
	return out
}

// sortFindings orders findings by file, then line, then rule, so two
// runs over an unchanged tree produce identical output.
func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].File != f[j].File {
			return f[i].File < f[j].File
		}
		if f[i].Line != f[j].Line {
			return f[i].Line < f[j].Line
		}
		return f[i].Rule < f[j].Rule
	})
}

// skipDirs are directories never worth walking: version control,
// bytecode caches, and vendored JS.
var skipDirs = map[string]bool{
	".git":         true,
	"__pycache__":  true,
	"node_modules": true,
}

// collect expands paths into the list of files to check. Directories are
// walked for .xml; an explicit file is taken as given, so a hook can
// pass whatever the user just saved.
func collect(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(p)
			continue
		}
		werr := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // an unreadable subtree is not a markup defect
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".xml") {
				add(path)
			}
			return nil
		})
		if werr != nil {
			return nil, werr
		}
	}
	sort.Strings(out)
	return out, nil
}
