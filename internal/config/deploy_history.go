package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// deployHistoryCap bounds how many commit SHAs are remembered per target,
// keeping the recall file from growing without limit. The most recent
// SHAs are kept when the cap is exceeded.
const deployHistoryCap = 1000

// DeployTarget is the set of commit SHAs already deployed to one remote
// target from a given local repo. SavedAt is the last time it changed.
type DeployTarget struct {
	SHAs    []string  `toml:"shas"`
	SavedAt time.Time `toml:"saved_at"`
}

// deployHistoryFile is the on-disk shape of a per-project deploy history:
// `~/.config/echo/deploy-history/<projectKey>.toml`, a table of
// targetKey → DeployTarget.
type deployHistoryFile struct {
	Targets map[string]DeployTarget `toml:"targets"`
}

// deployHistoryPath returns the history file path for a project key (the
// per-project filename stem, see ProjectKey).
func deployHistoryPath(projectKey string) (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "deploy-history", projectKey+".toml"), nil
}

// DeployTargetKey derives a stable map key for a remote target from its SSH
// host and remote path, so "deployed" is scoped per destination (a commit
// shipped to staging isn't shipped to prod).
func DeployTargetKey(sshHost, remotePath string) string {
	sum := sha256.Sum256([]byte(sshHost + "\x1f" + remotePath))
	return fmt.Sprintf("%x", sum)
}

// LoadDeployedSHAs returns the set of commit SHAs already deployed to
// (projectKey, targetKey). A missing or unparseable file, or an absent
// target, yields an empty map and no error: the tint is a best-effort
// optimization, never a hard dependency.
func LoadDeployedSHAs(projectKey, targetKey string) map[string]bool {
	t, ok := loadDeployHistory(projectKey).Targets[targetKey]
	if !ok {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(t.SHAs))
	for _, s := range t.SHAs {
		set[s] = true
	}
	return set
}

// MarkDeployed records shas as deployed to (projectKey, targetKey),
// merging into any existing set (dedup, prior preserved) and capping the
// result at deployHistoryCap most-recent SHAs. Newly deployed SHAs sort to
// the end so the cap drops the oldest. A best-effort write: errors are
// returned but callers treat persistence as non-fatal.
func MarkDeployed(projectKey, targetKey string, shas []string) error {
	if len(shas) == 0 {
		return nil
	}
	path, err := deployHistoryPath(projectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	f := loadDeployHistory(projectKey)
	if f.Targets == nil {
		f.Targets = map[string]DeployTarget{}
	}
	t := f.Targets[targetKey]

	seen := make(map[string]bool, len(t.SHAs))
	merged := make([]string, 0, len(t.SHAs)+len(shas))
	for _, s := range t.SHAs {
		if !seen[s] {
			seen[s] = true
			merged = append(merged, s)
		}
	}
	for _, s := range shas {
		if !seen[s] {
			seen[s] = true
			merged = append(merged, s)
		}
	}
	if len(merged) > deployHistoryCap {
		merged = merged[len(merged)-deployHistoryCap:]
	}

	t.SHAs = merged
	t.SavedAt = time.Now()
	f.Targets[targetKey] = t

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}

// loadDeployHistory reads the full targetKey → DeployTarget table for a
// project, returning a non-nil (possibly empty) struct so callers can
// upsert safely.
func loadDeployHistory(projectKey string) deployHistoryFile {
	empty := deployHistoryFile{Targets: map[string]DeployTarget{}}
	path, err := deployHistoryPath(projectKey)
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var f deployHistoryFile
	if _, err := toml.Decode(string(data), &f); err != nil || f.Targets == nil {
		return empty
	}
	return f
}
