package odoolint

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// dataListRe captures the body of a manifest's data or demo list.
	// Both hold the files the loader reads; nothing else in the manifest
	// decides whether a file is loaded.
	dataListRe = regexp.MustCompile(`(?s)['"](?:data|demo)['"]\s*:\s*\[(.*?)\]`)

	// quotedRe pulls the individual entries out of such a list.
	quotedRe = regexp.MustCompile(`['"]([^'"]+)['"]`)
)

// ManifestEntry is one path listed in a manifest's data or demo list,
// together with the manifest line it sits on.
//
// The line is what makes a "listed but missing" finding actionable: the
// file does not exist, so there is nothing to navigate to except the
// manifest line the user has to edit.
type ManifestEntry struct {
	// Path is absolute, resolved against the module directory.
	Path string
	// Line is the 1-based line in __manifest__.py.
	Line int
}

// ManifestEntries returns the data and demo entries of a module's
// manifest.
//
// A regex rather than a Python parse: the lists are literal string
// sequences in every manifest Odoo accepts, and importing a Python
// interpreter to read them would be the larger risk.
func ManifestEntries(moduleDir string) []ManifestEntry {
	data, err := os.ReadFile(filepath.Join(moduleDir, "__manifest__.py"))
	if err != nil {
		return nil
	}
	var out []ManifestEntry
	for _, list := range dataListRe.FindAllSubmatchIndex(data, -1) {
		// Submatch 1 is the list body; offsets are absolute in data, so
		// entry line numbers come straight from them.
		bodyStart, bodyEnd := list[2], list[3]
		body := data[bodyStart:bodyEnd]
		for _, entry := range quotedRe.FindAllSubmatchIndex(body, -1) {
			rel := string(body[entry[2]:entry[3]])
			if rel == "" {
				continue
			}
			out = append(out, ManifestEntry{
				Path: absOrSelf(filepath.Join(moduleDir, rel)),
				Line: lineAt(data, bodyStart+entry[2]),
			})
		}
	}
	return out
}

// ManifestSet builds the severity set for a run: every path listed in the
// manifests of moduleDirs.
//
// This answers exactly one question, "would the loader read this file",
// because that is what separates a defect that aborts a deploy from one
// that merely waits to.
func ManifestSet(moduleDirs []string) map[string]bool {
	set := map[string]bool{}
	for _, dir := range moduleDirs {
		for _, e := range ManifestEntries(dir) {
			set[e.Path] = true
		}
	}
	return set
}

// CheckManifests cross-checks each module's manifest against disk, in both
// directions. It is the structural counterpart to the markup passes: same
// question ("what will the loader refuse"), different door.
func CheckManifests(moduleDirs []string) []Finding {
	var out []Finding
	for _, dir := range moduleDirs {
		entries := ManifestEntries(dir)
		out = append(out, missingEntries(dir, entries)...)
		out = append(out, unlistedDataFiles(dir, entries)...)
	}
	return out
}

// missingEntries reports manifest entries with no file on disk.
//
// Always KindErr, and this is the one place Unit 109's manifest-aware
// severity cannot apply: that model asks whether a manifest lists the file
// in order to decide if the loader will read it, and here the answer is
// yes by construction — the absence *is* the defect. The loader opens
// listed files in order and aborts on the first it cannot read, every
// time, so a warning would understate it.
func missingEntries(moduleDir string, entries []ManifestEntry) []Finding {
	manifest := filepath.Join(moduleDir, "__manifest__.py")
	var out []Finding
	for _, e := range entries {
		if _, err := os.Stat(e.Path); err == nil {
			continue
		}
		out = append(out, Finding{
			File:    manifest,
			Line:    e.Line,
			Kind:    KindErr,
			Rule:    RuleManifestMissing,
			Message: "listed in the manifest but not on disk: " + RelTo(moduleDir, e.Path),
		})
	}
	return out
}

// unlistedDataFiles reports data-shaped XML no manifest mentions.
//
// Always KindWarn, never more. An unlisted .xml is routine while a module
// is being written, and blocking a deploy on one would make the pre-flight
// unusable within a day. The check earns its place by explaining why a
// finding in such a file is inert, not by gating anything.
func unlistedDataFiles(moduleDir string, entries []ManifestEntry) []Finding {
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.Path] = true
	}

	var out []Finding
	_ = filepath.WalkDir(moduleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || unlistedSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".xml") {
			return nil
		}
		if listed[absOrSelf(path)] {
			return nil
		}
		// Only data-shaped files. An OWL <templates> file reaches Odoo
		// through the assets bundle, not through `data`, so flagging it
		// would be noise on every module that ships front-end code.
		if !isDataFile(path) {
			return nil
		}
		out = append(out, Finding{
			File:    path,
			Line:    1,
			Kind:    KindWarn,
			Rule:    RuleManifestUnlisted,
			Message: "no manifest lists this file; the loader never reads it",
		})
		return nil
	})
	return out
}

// unlistedSkipDirs are directories the data loader never reads by manifest
// listing, so an unlisted .xml inside them is expected rather than
// noteworthy. (static/ is also excluded by isDataFile, but skipping the
// tree is cheaper than reading every file in it.)
var unlistedSkipDirs = map[string]bool{
	"static":     true,
	"tests":      true,
	"migrations": true,
	"i18n":       true,
}

// ModuleDirs returns the module directories directly under each of roots
// — a directory is a module when it holds a __manifest__.py. Sorted and
// deduplicated.
func ModuleDirs(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || skipDirs[e.Name()] {
				continue
			}
			dir := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(dir, "__manifest__.py")); err != nil {
				continue
			}
			abs := absOrSelf(dir)
			if seen[abs] {
				continue
			}
			seen[abs] = true
			out = append(out, dir)
		}
		// A root that is itself a module (a single-module repo checked
		// out directly) counts too.
		if _, err := os.Stat(filepath.Join(root, "__manifest__.py")); err == nil {
			abs := absOrSelf(root)
			if !seen[abs] {
				seen[abs] = true
				out = append(out, root)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ModuleDirFor returns the module directory containing path, walking up
// until a __manifest__.py appears, or "" when path is not inside a module.
// It is how an explicit file argument still gets its module cross-checked.
func ModuleDirFor(path string) string {
	dir := path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "__manifest__.py")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// RelTo renders p relative to base when it is inside it, so messages read
// as module-relative paths rather than absolute noise.
func RelTo(base, p string) string {
	if rel, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}
