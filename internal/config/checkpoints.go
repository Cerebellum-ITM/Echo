package config

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// CheckpointEntry is one recorded database checkpoint for a remote deploy
// target. Name is the remote object: the checkpoint DATABASE name for the
// "db" method, or the dump file's basename for the "dump" method. DumpPath
// is the dump file's path relative to the remote project root (dump method
// only). DeploySHAs are the commit SHAs the checkpoint protected, so a
// `deploy --rollback` can un-mark them from the deploy history.
type CheckpointEntry struct {
	Name       string    `toml:"name"`
	Method     string    `toml:"method"`
	DB         string    `toml:"db"`
	CreatedAt  time.Time `toml:"created_at"`
	DeploySHAs []string  `toml:"deploy_shas"`
	DumpPath   string    `toml:"dump_path"`
	// CodeSHA is the remote deploy branch's HEAD captured BEFORE the deploy
	// advanced it (git-deploy targets only, Unit 102). When set, a rollback
	// restores the code to this hash alongside the DB; empty on non-git
	// targets and pre-Unit-102 entries (DB-only restore, unchanged).
	CodeSHA string `toml:"code_sha"`
}

// checkpointTarget is the set of checkpoints recorded for one remote target.
type checkpointTarget struct {
	Entries []CheckpointEntry `toml:"entries"`
}

// checkpointStoreFile is the on-disk shape of a per-project checkpoint store:
// `~/.config/echo/checkpoints/<projectKey>.toml`, a table of targetKey →
// checkpointTarget. It mirrors deploy-history's layout (same DeployTargetKey).
type checkpointStoreFile struct {
	Targets map[string]checkpointTarget `toml:"targets"`
}

// checkpointStorePath returns the checkpoint store path for a project key.
func checkpointStorePath(projectKey string) (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "checkpoints", projectKey+".toml"), nil
}

// LoadCheckpoints returns the checkpoints recorded for (projectKey,
// targetKey), newest first. A missing or unparseable file, or an absent
// target, yields nil and no error: the store is a best-effort convenience.
func LoadCheckpoints(projectKey, targetKey string) []CheckpointEntry {
	t := loadCheckpointStore(projectKey).Targets[targetKey]
	if len(t.Entries) == 0 {
		return nil
	}
	out := append([]CheckpointEntry(nil), t.Entries...)
	sortCheckpointsNewestFirst(out)
	return out
}

// SaveCheckpoints replaces the checkpoint list for (projectKey, targetKey)
// with entries (an empty slice clears the target). Best-effort atomic write.
func SaveCheckpoints(projectKey, targetKey string, entries []CheckpointEntry) error {
	path, err := checkpointStorePath(projectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f := loadCheckpointStore(projectKey)
	if f.Targets == nil {
		f.Targets = map[string]checkpointTarget{}
	}
	if len(entries) == 0 {
		delete(f.Targets, targetKey)
	} else {
		f.Targets[targetKey] = checkpointTarget{Entries: entries}
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}

// AddCheckpoint appends entry to (projectKey, targetKey), newest first.
func AddCheckpoint(projectKey, targetKey string, entry CheckpointEntry) error {
	entries := LoadCheckpoints(projectKey, targetKey)
	entries = append([]CheckpointEntry{entry}, entries...)
	return SaveCheckpoints(projectKey, targetKey, entries)
}

// RemoveCheckpoint drops the entry named name from (projectKey, targetKey).
// A missing name is a no-op.
func RemoveCheckpoint(projectKey, targetKey, name string) error {
	entries := LoadCheckpoints(projectKey, targetKey)
	kept := entries[:0]
	for _, e := range entries {
		if e.Name != name {
			kept = append(kept, e)
		}
	}
	return SaveCheckpoints(projectKey, targetKey, kept)
}

// loadCheckpointStore reads the full targetKey → checkpointTarget table for a
// project, returning a non-nil (possibly empty) struct so callers can upsert.
func loadCheckpointStore(projectKey string) checkpointStoreFile {
	empty := checkpointStoreFile{Targets: map[string]checkpointTarget{}}
	path, err := checkpointStorePath(projectKey)
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var f checkpointStoreFile
	if _, err := toml.Decode(string(data), &f); err != nil || f.Targets == nil {
		return empty
	}
	return f
}

// sortCheckpointsNewestFirst orders entries by CreatedAt descending (a stable
// insertion sort — the lists are tiny, bounded by the retention keep count).
func sortCheckpointsNewestFirst(entries []CheckpointEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].CreatedAt.After(entries[j-1].CreatedAt); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}
