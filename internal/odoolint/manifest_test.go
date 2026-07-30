package odoolint

import (
	"path/filepath"
	"strings"
	"testing"
)

// findingByRule returns the findings carrying rule.
func findingsByRule(fs []Finding, rule string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// A manifest entry with no file on disk aborts the load every time, so it
// is unconditionally blocking — and it is reported at the manifest line the
// user has to edit, because the file it names does not exist to navigate to.
func TestCheckManifestsMissingEntry(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mod_a/__manifest__.py", `{
    'name': 'A',
    'data': [
        'views/present.xml',
        'views/ghost.xml',
    ],
}`)
	writeFile(t, dir, "mod_a/views/present.xml", "<odoo/>")

	got := findingsByRule(CheckManifests([]string{filepath.Join(dir, "mod_a")}), RuleManifestMissing)
	if len(got) != 1 {
		t.Fatalf("got %d missing-entry findings, want 1: %+v", len(got), got)
	}
	if got[0].Kind != KindErr {
		t.Errorf("kind = %q, want %q (the loader aborts on it)", got[0].Kind, KindErr)
	}
	if filepath.Base(got[0].File) != "__manifest__.py" {
		t.Errorf("file = %q, want the manifest (the phantom path is not navigable)", got[0].File)
	}
	// 'views/ghost.xml' is on line 5 of the manifest above.
	if got[0].Line != 5 {
		t.Errorf("line = %d, want 5 (the manifest line of the entry)", got[0].Line)
	}
	if !strings.Contains(got[0].Message, "views/ghost.xml") {
		t.Errorf("message = %q, want the relative path named", got[0].Message)
	}
}

// Not only XML: a missing security CSV aborts the load the same way.
func TestCheckManifestsMissingNonXML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mod_a/__manifest__.py",
		"{'name': 'A', 'data': ['security/ir.model.access.csv']}")

	got := findingsByRule(CheckManifests([]string{filepath.Join(dir, "mod_a")}), RuleManifestMissing)
	if len(got) != 1 {
		t.Fatalf("a missing .csv entry was not caught: %+v", got)
	}
	if got[0].Kind != KindErr {
		t.Errorf("kind = %q, want %q", got[0].Kind, KindErr)
	}
}

func TestCheckManifestsUnlistedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mod_a/__manifest__.py",
		"{'name': 'A', 'data': ['views/listed.xml']}")
	writeFile(t, dir, "mod_a/views/listed.xml", "<odoo/>")
	writeFile(t, dir, "mod_a/views/orphan.xml", "<odoo><record id=\"a\" model=\"m\"/></odoo>")

	got := findingsByRule(CheckManifests([]string{filepath.Join(dir, "mod_a")}), RuleManifestUnlisted)
	if len(got) != 1 {
		t.Fatalf("got %d unlisted findings, want 1: %+v", len(got), got)
	}
	if filepath.Base(got[0].File) != "orphan.xml" {
		t.Errorf("file = %q, want orphan.xml", got[0].File)
	}
	// Never more than a warning: an unlisted .xml is routine while a module
	// is being written, and blocking a deploy on one would make the
	// pre-flight unusable within a day.
	if got[0].Kind != KindWarn {
		t.Errorf("kind = %q, want %q", got[0].Kind, KindWarn)
	}
}

// The unlisted check has to stay quiet enough to leave on: front-end
// templates and the non-data directories must not be flagged.
func TestCheckManifestsUnlistedIsQuiet(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mod_a/__manifest__.py", "{'name': 'A', 'data': []}")
	// An OWL templates file reaches Odoo through the assets bundle, not
	// through `data` — and not only from static/.
	writeFile(t, dir, "mod_a/views/owl.xml", "<templates><t t-name=\"a\"/></templates>")
	writeFile(t, dir, "mod_a/static/src/xml/t.xml", "<odoo/>")
	writeFile(t, dir, "mod_a/tests/fixture.xml", "<odoo/>")
	writeFile(t, dir, "mod_a/migrations/17.0.1.0/pre.xml", "<odoo/>")
	writeFile(t, dir, "mod_a/i18n/es_MX.xml", "<odoo/>")

	got := findingsByRule(CheckManifests([]string{filepath.Join(dir, "mod_a")}), RuleManifestUnlisted)
	if len(got) != 0 {
		t.Fatalf("the unlisted check is too loud: %+v", got)
	}
}

func TestCheckManifestsConsistentModuleIsSilent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mod_a/__manifest__.py",
		"{'name': 'A', 'data': ['views/v.xml'], 'demo': ['demo/d.xml']}")
	writeFile(t, dir, "mod_a/views/v.xml", "<odoo/>")
	writeFile(t, dir, "mod_a/demo/d.xml", "<odoo/>")

	if got := CheckManifests([]string{filepath.Join(dir, "mod_a")}); len(got) != 0 {
		t.Fatalf("a consistent module produced findings: %+v", got)
	}
}

func TestManifestEntriesLineNumbers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mod_a/__manifest__.py", `{
    'name': 'A',
    'data': [
        'a.xml',
        'b.xml',
    ],
    'demo': ['c.xml'],
}`)
	got := ManifestEntries(filepath.Join(dir, "mod_a"))
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	wantLines := map[string]int{"a.xml": 4, "b.xml": 5, "c.xml": 7}
	for _, e := range got {
		base := filepath.Base(e.Path)
		if want := wantLines[base]; e.Line != want {
			t.Errorf("%s line = %d, want %d", base, e.Line, want)
		}
	}
}

// Both structural rules carry their own severity; the manifest-aware pass
// must not rewrite either — for opposite reasons.
func TestApplySeverityLeavesManifestRulesAlone(t *testing.T) {
	in := []Finding{
		{File: "mod/__manifest__.py", Line: 3, Rule: RuleManifestMissing, Kind: KindErr},
		{File: "mod/views/orphan.xml", Line: 1, Rule: RuleManifestUnlisted, Kind: KindWarn},
	}
	// An empty severity set would normally force everything to warn.
	got := applySeverity(in, map[string]bool{})
	if got[0].Kind != KindErr {
		t.Errorf("manifest-missing = %q, want %q even with nothing listed", got[0].Kind, KindErr)
	}
	if got[1].Kind != KindWarn {
		t.Errorf("manifest-unlisted = %q, want %q", got[1].Kind, KindWarn)
	}

	// And a fully-listing set must not escalate the unlisted rule either.
	set := map[string]bool{absOrSelf("mod/views/orphan.xml"): true}
	if got := applySeverity(in, set); got[1].Kind != KindWarn {
		t.Errorf("manifest-unlisted escalated to %q; it must never block", got[1].Kind)
	}
}

func TestModuleDirFor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "addons/mod_a/__manifest__.py", "{'name': 'A'}")
	f := writeFile(t, dir, "addons/mod_a/views/v.xml", "<odoo/>")
	outside := writeFile(t, dir, "loose.xml", "<odoo/>")

	if got := ModuleDirFor(f); filepath.Base(got) != "mod_a" {
		t.Errorf("ModuleDirFor(file in module) = %q, want mod_a", got)
	}
	if got := ModuleDirFor(outside); got != "" {
		t.Errorf("ModuleDirFor(file outside any module) = %q, want empty", got)
	}
}

// Check must surface the structural findings alongside the markup ones, in
// the same sorted list.
func TestCheckIncludesManifestFindings(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "mod_a")
	writeFile(t, dir, "mod_a/__manifest__.py",
		"{'name': 'A', 'data': ['views/ghost.xml']}")
	writeFile(t, dir, "mod_a/views/orphan.xml", "<odoo><record id=\"a\" model=\"m\"/></odoo>")

	res, err := Check([]string{mod}, Options{ManifestDirs: []string{mod}})
	if err != nil {
		t.Fatal(err)
	}
	if len(findingsByRule(res.Findings, RuleManifestMissing)) != 1 {
		t.Errorf("missing-entry finding absent from Check: %+v", res.Findings)
	}
	if len(findingsByRule(res.Findings, RuleManifestUnlisted)) != 1 {
		t.Errorf("unlisted finding absent from Check: %+v", res.Findings)
	}
	if res.Errors() != 1 {
		t.Errorf("Errors() = %d, want 1 (the missing entry blocks)", res.Errors())
	}
	if res.Warnings() != 1 {
		t.Errorf("Warnings() = %d, want 1 (the orphan does not)", res.Warnings())
	}
}

// Without ManifestDirs the structural pass does not run at all, so a caller
// that only wants markup checks gets exactly that.
func TestCheckSkipsManifestPassWhenUnset(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "mod_a")
	writeFile(t, dir, "mod_a/__manifest__.py",
		"{'name': 'A', 'data': ['views/ghost.xml']}")

	res, err := Check([]string{mod}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("structural findings appeared without ManifestDirs: %+v", res.Findings)
	}
}
