package config

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// LastUpdate is the last `update` target for one (project, database),
// reused by `update --last` and the empty-picker repeat. Modules is the
// resolved module list; All is true when the last run was `update --all`
// (then Modules is empty). Level is the --log-level in effect at that run
// (may be empty). One entry per database within a project's file.
type LastUpdate struct {
	Modules []string  `toml:"modules"`
	All     bool      `toml:"all"`
	Level   string    `toml:"level"`
	SavedAt time.Time `toml:"saved_at"`
}

// lastUpdatesFile is the on-disk shape of a per-project recall file:
// `~/.config/echo/last-updates/<key>.toml`, a table of dbName → LastUpdate.
type lastUpdatesFile struct {
	Updates map[string]LastUpdate `toml:"updates"`
}

// lastUpdatesPath returns the recall file path for the given project key
// (the per-project config filename stem, see ProjectKey).
func lastUpdatesPath(projectKey string) (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "last-updates", projectKey+".toml"), nil
}

// LoadLastUpdate returns the saved target for (projectKey, db) and whether
// one exists. A missing or unparseable file, or an absent db key, yields
// (zero, false) and no error: the recall is a best-effort optimization,
// never a hard dependency.
func LoadLastUpdate(projectKey, db string) (LastUpdate, bool) {
	path, err := lastUpdatesPath(projectKey)
	if err != nil {
		return LastUpdate{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LastUpdate{}, false
	}
	var f lastUpdatesFile
	if _, err := toml.Decode(string(data), &f); err != nil || f.Updates == nil {
		return LastUpdate{}, false
	}
	u, ok := f.Updates[db]
	return u, ok
}

// SaveLastUpdate upserts the record for db into the project's recall file,
// preserving the other databases' entries, and writes it atomically.
func SaveLastUpdate(projectKey, db string, u LastUpdate) error {
	path, err := lastUpdatesPath(projectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	updates := loadLastUpdates(projectKey)
	updates[db] = u

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(lastUpdatesFile{Updates: updates}); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}

// loadLastUpdates reads the full db → LastUpdate table for a project,
// returning a non-nil (possibly empty) map so callers can upsert safely.
func loadLastUpdates(projectKey string) map[string]LastUpdate {
	path, err := lastUpdatesPath(projectKey)
	if err != nil {
		return map[string]LastUpdate{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]LastUpdate{}
	}
	var f lastUpdatesFile
	if _, err := toml.Decode(string(data), &f); err != nil || f.Updates == nil {
		return map[string]LastUpdate{}
	}
	return f.Updates
}
