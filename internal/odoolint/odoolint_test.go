package odoolint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates path under dir with content, making parents.
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// findingFor returns the first finding for file, or nil.
func findingFor(fs []Finding, file string) *Finding {
	for i := range fs {
		if fs[i].File == file {
			return &fs[i]
		}
	}
	return nil
}

func TestSyntaxFindingsPerRule(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string // substring expected in the message
		wantLn  int
	}{
		{
			name:    "double hyphen in comment",
			body:    "<odoo>\n<!-- tiene -- adentro -->\n</odoo>\n",
			wantSub: `invalid sequence "--" not allowed in comments`,
			wantLn:  2,
		},
		{
			name:    "mismatched tags",
			body:    "<odoo>\n<p>x</odoo>\n",
			wantSub: "closed by",
			wantLn:  2,
		},
		{
			name:    "stray < from inline JS",
			body:    "<templates>\n<t t-name=\"a\">\n<script>if (v<0){}</script>\n</t>\n</templates>\n",
			wantSub: "invalid XML name",
			wantLn:  3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := syntaxFindings("f.xml", []byte(tc.body))
			if len(got) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(got), got)
			}
			if !strings.Contains(got[0].Message, tc.wantSub) {
				t.Errorf("message = %q, want substring %q", got[0].Message, tc.wantSub)
			}
			if got[0].Line != tc.wantLn {
				t.Errorf("line = %d, want %d", got[0].Line, tc.wantLn)
			}
			if got[0].Rule != RuleSyntax {
				t.Errorf("rule = %q, want %q", got[0].Rule, RuleSyntax)
			}
		})
	}
}

func TestSyntaxFindingsValidFile(t *testing.T) {
	body := `<odoo>
  <record id="a" model="m"><field name="x">1</field></record>
</odoo>
`
	if got := syntaxFindings("f.xml", []byte(body)); len(got) != 0 {
		t.Fatalf("valid file produced findings: %+v", got)
	}
}

// A defect inside CDATA is invisible to the outer parse, and the finding
// must land on the containing file's line — not on the line within the
// fragment, which the user cannot navigate to.
func TestSyntaxFindingsCDATALineOffset(t *testing.T) {
	body := `<odoo>
<record id="t" model="mail.template">
<field name="arch"><![CDATA[
<div>
<!-- roto -- aqui -->
</div>
]]></field>
</record>
</odoo>
`
	got := syntaxFindings("f.xml", []byte(body))
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Rule != RuleEmbedded {
		t.Errorf("rule = %q, want %q", got[0].Rule, RuleEmbedded)
	}
	// The CDATA body starts on line 3 and the broken comment is on the
	// third line of the fragment (line 5 of the file).
	if got[0].Line != 5 {
		t.Errorf("line = %d, want 5 (file line, not fragment line)", got[0].Line)
	}
}

func TestSyntaxFindingsEscapedArch(t *testing.T) {
	body := `<odoo>
<record id="v" model="ir.ui.view">
<field name="arch_db">&lt;form&gt;&lt;!-- roto -- aqui --&gt;&lt;/form&gt;</field>
</record>
</odoo>
`
	got := syntaxFindings("f.xml", []byte(body))
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Line != 3 {
		t.Errorf("line = %d, want 3", got[0].Line)
	}
	if !strings.Contains(got[0].Message, "arch_db") {
		t.Errorf("message = %q, want the field named", got[0].Message)
	}
}

// body_html holds HTML, which Odoo parses with lxml.html. An HTML parser
// accepts unclosed tags and bare template directives that an XML parser
// rejects, so checking such a field as XML is a false-positive factory —
// it produced five on the first real repo this ran against.
func TestSyntaxFindingsSkipsHTMLFields(t *testing.T) {
	body := `<odoo>
<record id="t" model="mail.template">
<field name="body_html"><![CDATA[
<p>Hola
% if algo:
<br>
% endif
]]></field>
</record>
</odoo>
`
	if got := syntaxFindings("f.xml", []byte(body)); len(got) != 0 {
		t.Fatalf("HTML field reported as broken XML: %+v", got)
	}
}

// The same defect in arch is still caught, because the view loader really
// does parse it as XML.
func TestSyntaxFindingsChecksArchField(t *testing.T) {
	body := `<odoo>
<record id="v" model="ir.ui.view">
<field name="arch"><![CDATA[
<form><!-- roto -- aqui --></form>
]]></field>
</record>
</odoo>
`
	got := syntaxFindings("f.xml", []byte(body))
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Line != 4 {
		t.Errorf("line = %d, want 4", got[0].Line)
	}
}

// CDATA holding something that is not markup (SQL, prose) must not be
// parsed as XML, or every report template becomes a false positive.
func TestSyntaxFindingsIgnoresNonMarkupCDATA(t *testing.T) {
	body := `<odoo>
<field name="query"><![CDATA[
SELECT a FROM b WHERE c < 3 AND d -- comentario
]]></field>
</odoo>
`
	if got := syntaxFindings("f.xml", []byte(body)); len(got) != 0 {
		t.Fatalf("non-markup CDATA produced findings: %+v", got)
	}
}

// The severity split is what keeps the linter from being stricter than
// the server: identical defect, different consequence.
func TestApplySeverityManifestAware(t *testing.T) {
	listed := absOrSelf("mod/views/listed.xml")
	unlisted := absOrSelf("mod/views/dead.xml")
	set := map[string]bool{listed: true}

	in := []Finding{
		{File: "mod/views/listed.xml", Line: 2, Rule: RuleSyntax},
		{File: "mod/views/dead.xml", Line: 2, Rule: RuleSyntax},
	}
	got := applySeverity(in, set)

	if got[0].Kind != KindErr {
		t.Errorf("manifest-listed finding = %q, want %q", got[0].Kind, KindErr)
	}
	if got[1].Kind != KindWarn {
		t.Errorf("unlisted finding = %q, want %q", got[1].Kind, KindWarn)
	}
	_ = unlisted
}

func TestDedupePrefersLibxml2Message(t *testing.T) {
	in := []Finding{
		{File: "f.xml", Line: 2, Rule: RuleSyntax, Message: "go wording"},
		{File: "f.xml", Line: 2, Rule: RuleSyntax, Message: "libxml2 wording", fromLibxml2: true},
	}
	got := dedupe(in)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}
	if got[0].Message != "libxml2 wording" {
		t.Errorf("message = %q, want the libxml2 wording (what the server prints)", got[0].Message)
	}
}

func TestGrammarSelection(t *testing.T) {
	for _, major := range []string{"17", "18", "19", "18.0", "saas-19.2"} {
		data, used, exact := Grammar(major)
		if len(data) == 0 {
			t.Fatalf("Grammar(%q) returned no data", major)
		}
		if !exact {
			t.Errorf("Grammar(%q) fell back to %q, want an exact match", major, used)
		}
	}

	data, used, exact := Grammar("21")
	if exact {
		t.Error("Grammar(\"21\") reported an exact match for an unknown major")
	}
	if used != "19" {
		t.Errorf("fallback major = %q, want the newest embedded (19)", used)
	}
	if len(data) == 0 {
		t.Fatal("fallback returned no data")
	}
}

// A corrupt or truncated vendored grammar would make the schema pass
// silently useless, so pin both parseability and content.
func TestEmbeddedGrammarIntact(t *testing.T) {
	data, _, _ := Grammar("19")

	var probe struct{}
	if err := xml.Unmarshal(data, &probe); err != nil {
		t.Fatalf("embedded grammar does not parse as XML: %v", err)
	}

	const want = "eca952160a2bc1c79201a34dbbc511ad5b776bc76e1e64fa89b22dfc1811a90e"
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("grammar sha256 = %s, want %s (Odoo 17.0/18.0/19.0 import_xml.rng)", got, want)
	}

	// A grammar with includes could not be embedded as one file.
	if strings.Contains(string(data), "externalRef") || strings.Contains(string(data), "rng:include") {
		t.Error("grammar has include/externalRef directives; it is no longer self-contained")
	}
}

func TestRootElementAndDataFileClassification(t *testing.T) {
	dir := t.TempDir()
	odooRoot := writeFile(t, dir, "mod/views/v.xml", "<odoo><record id=\"a\" model=\"m\"/></odoo>")
	openerpRoot := writeFile(t, dir, "mod/views/legacy.xml", "<openerp><data/></openerp>")
	staticFile := writeFile(t, dir, "mod/static/src/xml/t.xml", "<templates><t t-name=\"a\"/></templates>")
	owlRoot := writeFile(t, dir, "mod/other/t.xml", "<templates><t t-name=\"a\"/></templates>")

	if !isDataFile(odooRoot) {
		t.Error("an <odoo> root outside static/ must get the schema pass")
	}
	// The grammar defines an <openerp><data> root, so a pre-v10 file is
	// validated normally rather than skipped.
	if !isDataFile(openerpRoot) {
		t.Error("an <openerp> root must get the schema pass, not be skipped")
	}
	if isDataFile(staticFile) {
		t.Error("files under static/ are never read by the data loader")
	}
	if isDataFile(owlRoot) {
		t.Error("a <templates> root is not a data file")
	}
}

func TestCollectWalksAndSkips(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mod/views/a.xml", "<odoo/>")
	writeFile(t, dir, "mod/static/src/b.xml", "<templates/>")
	writeFile(t, dir, "mod/models/c.py", "x = 1")
	writeFile(t, dir, ".git/objects/d.xml", "<odoo/>")
	writeFile(t, dir, "node_modules/pkg/e.xml", "<odoo/>")
	writeFile(t, dir, "mod/__pycache__/f.xml", "<odoo/>")

	got, err := collect([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("collected %d files, want 2 (a.xml + static b.xml): %v", len(got), got)
	}
	for _, f := range got {
		if strings.Contains(f, ".git") || strings.Contains(f, "node_modules") ||
			strings.Contains(f, "__pycache__") {
			t.Errorf("collected a file from a skipped directory: %s", f)
		}
	}
}

// An explicit file argument is taken as given — that is what a save hook
// passes, and it may well sit outside the addons paths.
func TestCollectExplicitFile(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, dir, "scratch/one.xml", "<odoo/>")
	got, err := collect([]string{f})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != f {
		t.Fatalf("collect(%q) = %v, want just the file", f, got)
	}
}

func TestCollectMissingPathErrors(t *testing.T) {
	if _, err := collect([]string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("collect on a missing path returned no error")
	}
}

func TestManifestSetAndModuleDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "addons/mod_a/__manifest__.py", `{
    'name': 'A',
    'data': [
        'views/listed.xml',
        'security/ir.model.access.csv',
    ],
    'demo': ['demo/demo.xml'],
}`)
	writeFile(t, dir, "addons/mod_a/views/listed.xml", "<odoo/>")
	writeFile(t, dir, "addons/mod_a/views/dead.xml", "<odoo/>")
	writeFile(t, dir, "addons/not_a_module/x.xml", "<odoo/>")

	dirs := ModuleDirs([]string{filepath.Join(dir, "addons")})
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "mod_a" {
		t.Fatalf("ModuleDirs = %v, want just mod_a", dirs)
	}

	set := ManifestSet(dirs)
	listed := absOrSelf(filepath.Join(dir, "addons/mod_a/views/listed.xml"))
	demo := absOrSelf(filepath.Join(dir, "addons/mod_a/demo/demo.xml"))
	dead := absOrSelf(filepath.Join(dir, "addons/mod_a/views/dead.xml"))

	if !set[listed] {
		t.Error("a file in the data list is not in the manifest set")
	}
	if !set[demo] {
		t.Error("a file in the demo list is not in the manifest set")
	}
	if set[dead] {
		t.Error("a file no list mentions is in the manifest set")
	}
}

// Without xmllint the Go pass must still report, the skipped passes must
// be named, and a file whose only defect is schema-level must come back
// clean rather than falsely flagged.
func TestCheckWithoutXmllint(t *testing.T) {
	dir := t.TempDir()
	broken := writeFile(t, dir, "mod/views/broken.xml", "<odoo>\n<!-- a -- b -->\n</odoo>\n")
	schemaOnly := writeFile(t, dir, "mod/views/schema.xml",
		"<odoo>\n<template id=\"x\" t-translation=\"off\"><t t-esc=\"1\"/></template>\n</odoo>\n")
	grammar, _, _ := Grammar("18")

	res, err := Check([]string{dir}, Options{Grammar: grammar, Xmllint: ""})
	if err != nil {
		t.Fatal(err)
	}
	if findingFor(res.Findings, broken) == nil {
		t.Error("the Go pass finding did not survive a missing xmllint")
	}
	if f := findingFor(res.Findings, schemaOnly); f != nil {
		t.Errorf("reported a schema defect without a validator: %+v", f)
	}
	if len(res.SkippedPasses) != 2 {
		t.Errorf("SkippedPasses = %v, want both libxml2 passes named", res.SkippedPasses)
	}
}

// The end-to-end path, with the real xmllint when the host has one. It
// pins the two failures that motivated the unit plus one Go cannot see.
func TestCheckWithXmllint(t *testing.T) {
	bin := FindXmllint()
	if bin == "" {
		t.Skip("xmllint not installed")
	}

	dir := t.TempDir()
	writeFile(t, dir, "addons/mod_a/__manifest__.py", `{
    'name': 'A',
    'data': ['views/schema.xml', 'views/dup.xml', 'views/comment.xml'],
}`)
	comment := writeFile(t, dir, "addons/mod_a/views/comment.xml",
		"<odoo>\n<!-- tiene -- adentro -->\n</odoo>\n")
	schemaOnly := writeFile(t, dir, "addons/mod_a/views/schema.xml",
		"<odoo>\n<template id=\"x\" t-translation=\"off\"><t t-esc=\"1\"/></template>\n</odoo>\n")
	dup := writeFile(t, dir, "addons/mod_a/views/dup.xml",
		"<odoo>\n<record id=\"a\" id=\"b\" model=\"m\"/>\n</odoo>\n")
	dead := writeFile(t, dir, "addons/mod_a/views/dead.xml",
		"<odoo>\n<!-- otro -- roto -->\n</odoo>\n")
	ok := writeFile(t, dir, "addons/mod_a/views/ok.xml",
		"<odoo>\n<record id=\"a\" model=\"m\"><field name=\"x\">1</field></record>\n</odoo>\n")

	grammar, _, _ := Grammar("18")
	res, err := Check([]string{dir}, Options{
		Grammar:     grammar,
		Xmllint:     bin,
		ManifestSet: ManifestSet(ModuleDirs([]string{filepath.Join(dir, "addons")})),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SkippedPasses) != 0 {
		t.Errorf("SkippedPasses = %v, want none with xmllint present", res.SkippedPasses)
	}

	if f := findingFor(res.Findings, comment); f == nil || f.Kind != KindErr {
		t.Errorf("`--` in a listed file = %+v, want a KindErr finding", f)
	}
	if f := findingFor(res.Findings, schemaOnly); f == nil {
		t.Error("t-translation on <template> was not caught by the schema pass")
	} else if f.Rule != RuleSchema || f.Kind != KindErr {
		t.Errorf("schema finding = %+v, want rule %q kind %q", f, RuleSchema, KindErr)
	} else if !strings.Contains(f.Message, "extra content") {
		t.Errorf("schema message = %q, want the server's wording", f.Message)
	}
	// Go accepts a duplicate attribute; libxml2 (and so the server)
	// does not. This is the pass-inversion regression test.
	if f := findingFor(res.Findings, dup); f == nil {
		t.Error("duplicate attribute not caught — the libxml2 pass is not wired")
	}
	// Nothing lists dead.xml, so the loader never reads it.
	if f := findingFor(res.Findings, dead); f == nil {
		t.Error("no finding for the unlisted broken file")
	} else if f.Kind != KindWarn {
		t.Errorf("unlisted file finding = %q, want %q (the loader never reads it)", f.Kind, KindWarn)
	}
	if f := findingFor(res.Findings, ok); f != nil {
		t.Errorf("valid file reported: %+v", f)
	}
}

func TestParseXmllintDiagnostics(t *testing.T) {
	stderr := `dup.xml:2: parser error : Attribute name redefined
<record id="a" id="b" model="m"/>
                     ^
tpl.xml:2: element template: Relax-NG validity error : Element odoo has extra content: template
tpl.xml fails to validate
`
	got := parseXmllint(stderr)
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(got), got)
	}
	if got[0].File != "dup.xml" || got[0].Line != 2 || got[0].Rule != RuleSyntax {
		t.Errorf("first finding = %+v", got[0])
	}
	if got[0].Message != "Attribute name redefined" {
		t.Errorf("first message = %q, want the bare wording", got[0].Message)
	}
	if got[1].Rule != RuleSchema {
		t.Errorf("Relax-NG diagnostic classified as %q, want %q", got[1].Rule, RuleSchema)
	}
	if got[1].Message != "Element odoo has extra content: template" {
		t.Errorf("second message = %q", got[1].Message)
	}
	for _, f := range got {
		if !f.fromLibxml2 {
			t.Errorf("finding not marked as libxml2-sourced: %+v", f)
		}
	}
}

func TestResultCounts(t *testing.T) {
	r := Result{Findings: []Finding{
		{Kind: KindErr}, {Kind: KindWarn}, {Kind: KindErr},
	}}
	if r.Errors() != 2 {
		t.Errorf("Errors() = %d, want 2", r.Errors())
	}
	if r.Warnings() != 1 {
		t.Errorf("Warnings() = %d, want 1", r.Warnings())
	}
}
