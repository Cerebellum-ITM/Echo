package odoolint

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var (
	// dataListRe captures the body of a manifest's data or demo list.
	// Both hold the files the loader reads; nothing else in the manifest
	// decides whether a file is loaded.
	dataListRe = regexp.MustCompile(`(?s)['"](?:data|demo)['"]\s*:\s*\[(.*?)\]`)

	// quotedRe pulls the individual entries out of such a list.
	quotedRe = regexp.MustCompile(`['"]([^'"]+)['"]`)
)

// ManifestFiles returns the paths listed in a module's manifest data and
// demo lists, resolved against moduleDir and absolute.
//
// A regex rather than a Python parse: the lists are literal string
// sequences in every manifest Odoo accepts, and importing a Python
// interpreter to read them would be the larger risk.
func ManifestFiles(moduleDir string) []string {
	data, err := os.ReadFile(filepath.Join(moduleDir, "__manifest__.py"))
	if err != nil {
		return nil
	}
	var out []string
	for _, list := range dataListRe.FindAllSubmatch(data, -1) {
		for _, entry := range quotedRe.FindAllSubmatch(list[1], -1) {
			rel := string(entry[1])
			if rel == "" {
				continue
			}
			out = append(out, absOrSelf(filepath.Join(moduleDir, rel)))
		}
	}
	return out
}

// ManifestSet builds the severity set for a run: every path listed in the
// manifests of moduleDirs.
//
// This is not a manifest cross-check — it does not care whether a listed
// file exists. It answers exactly one question, "would the loader read
// this file", because that is what separates a defect that aborts a
// deploy from one that merely waits to.
func ManifestSet(moduleDirs []string) map[string]bool {
	set := map[string]bool{}
	for _, dir := range moduleDirs {
		for _, p := range ManifestFiles(dir) {
			set[p] = true
		}
	}
	return set
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
