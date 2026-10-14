package config

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// SideloadTarget records the last synced content hash of every addon
// sideloaded to one remote target from a given local source. It is the
// change-detection ledger behind `sideload`: on a re-run an addon whose
// tree hash still matches its stored value is skipped (no transfer, no
// update). SavedAt is the last time the entry changed.
type SideloadTarget struct {
	// Hashes maps an addon name to the hex SHA-256 of its file tree at the
	// time of the last successful sync to this target.
	Hashes  map[string]string `toml:"hashes"`
	SavedAt time.Time         `toml:"saved_at"`
}

// sideloadHistoryFile is the on-disk shape of a per-project sideload
// history: `~/.config/echo/sideload-history/<projectKey>.toml`, a table of
// targetKey → SideloadTarget. The targetKey is shared with deploy history
// (DeployTargetKey) so "this host+path" means the same destination for
// both commands.
type sideloadHistoryFile struct {
	Targets map[string]SideloadTarget `toml:"targets"`
}

// sideloadHistoryPath returns the history file path for a project key.
func sideloadHistoryPath(projectKey string) (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sideload-history", projectKey+".toml"), nil
}

// LoadSideloadHashes returns the addon→hash map last recorded for
// (projectKey, targetKey). A missing or unparseable file, or an absent
// target, yields an empty (non-nil) map and no error: change detection is a
// best-effort optimization, never a hard dependency — a lost ledger just
// re-syncs everything once.
func LoadSideloadHashes(projectKey, targetKey string) map[string]string {
	t, ok := loadSideloadHistory(projectKey).Targets[targetKey]
	if !ok || t.Hashes == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(t.Hashes))
	for k, v := range t.Hashes {
		out[k] = v
	}
	return out
}

// RecordSideloadHashes merges hashes into the stored ledger for
// (projectKey, targetKey): entries present in hashes overwrite (or add)
// their addon's recorded hash; addons absent from hashes keep their prior
// value, so sideloading addon B never forgets addon A. An empty map is a
// no-op. Best-effort write, mirroring MarkDeployed: the error is returned
// but callers treat persistence as non-fatal.
func RecordSideloadHashes(projectKey, targetKey string, hashes map[string]string) error {
	if len(hashes) == 0 {
		return nil
	}
	path, err := sideloadHistoryPath(projectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	f := loadSideloadHistory(projectKey)
	if f.Targets == nil {
		f.Targets = map[string]SideloadTarget{}
	}
	t := f.Targets[targetKey]
	if t.Hashes == nil {
		t.Hashes = map[string]string{}
	}
	for k, v := range hashes {
		t.Hashes[k] = v
	}
	t.SavedAt = time.Now()
	f.Targets[targetKey] = t

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}

// loadSideloadHistory reads the full targetKey → SideloadTarget table for a
// project, returning a non-nil (possibly empty) struct so callers can
// upsert safely.
func loadSideloadHistory(projectKey string) sideloadHistoryFile {
	empty := sideloadHistoryFile{Targets: map[string]SideloadTarget{}}
	path, err := sideloadHistoryPath(projectKey)
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var f sideloadHistoryFile
	if _, err := toml.Decode(string(data), &f); err != nil || f.Targets == nil {
		return empty
	}
	return f
}
