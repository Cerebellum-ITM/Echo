package config

import (
	"os"
	"path/filepath"
)

// projectStateFiles are the per-project state files, all named
// <key>.toml. cmd-logs is a directory keyed the same way and is handled
// alongside them.
var projectStateFiles = []string{
	"projects", "deploy-history", "last-updates", "last-sequences", "checkpoints",
}

// MigrateProjectKey moves a project's state from the key of oldRoot to the
// key of newRoot, so a binding made in a subdirectory survives the move to
// resolving the repository root. It refuses to overwrite: nothing happens
// unless the destination project file is absent and the source is present,
// which also makes it idempotent.
//
// Reports whether anything moved.
func MigrateProjectKey(oldRoot, newRoot string) (bool, error) {
	root, err := configRoot()
	if err != nil {
		return false, err
	}
	oldKey, newKey := ProjectKey(oldRoot), ProjectKey(newRoot)
	if oldKey == newKey {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(root, "projects", oldKey+".toml")); err != nil {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(root, "projects", newKey+".toml")); err == nil {
		return false, nil
	}

	for _, dir := range projectStateFiles {
		src := filepath.Join(root, dir, oldKey+".toml")
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, filepath.Join(root, dir, newKey+".toml")); err != nil {
			return false, err
		}
	}
	logs := filepath.Join(root, "cmd-logs", oldKey)
	if _, err := os.Stat(logs); err == nil {
		if err := os.Rename(logs, filepath.Join(root, "cmd-logs", newKey)); err != nil {
			return false, err
		}
	}
	return true, nil
}

// FindProjectState returns the deepest directory in the chain from start
// up to stop (inclusive) that already holds project state, or "" when none
// does. It is the source side of MigrateProjectKey: the subdirectory
// someone ran `link` from before the root resolved to the repository.
func FindProjectState(start, stop string) string {
	root, err := configRoot()
	if err != nil {
		return ""
	}
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(root, "projects", ProjectKey(dir)+".toml")); err == nil {
			return dir
		}
		if dir == stop {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
