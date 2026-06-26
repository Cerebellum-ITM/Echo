package config

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// LastSequence is the last sequence executed for a project, reused by
// `sequence --last`. Steps holds the fully-composed recipe lines (with any
// remote flag already baked in), so a repeat re-runs them verbatim without
// re-resolving anything. Remote/From record how it was launched, only for
// the running-line label. One record per project.
type LastSequence struct {
	Steps   []string  `toml:"steps"`
	Remote  bool      `toml:"remote"`
	From    string    `toml:"from"`
	SavedAt time.Time `toml:"saved_at"`
}

// lastSequencePath returns the recall file path for the given project key:
// `~/.config/echo/last-sequences/<key>.toml`.
func lastSequencePath(projectKey string) (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "last-sequences", projectKey+".toml"), nil
}

// LoadLastSequence returns the saved sequence for projectKey and whether one
// exists. A missing or unparseable file yields (zero, false) and no error:
// the recall is a best-effort convenience, never a hard dependency.
func LoadLastSequence(projectKey string) (LastSequence, bool) {
	path, err := lastSequencePath(projectKey)
	if err != nil {
		return LastSequence{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LastSequence{}, false
	}
	var s LastSequence
	if _, err := toml.Decode(string(data), &s); err != nil || len(s.Steps) == 0 {
		return LastSequence{}, false
	}
	return s, true
}

// SaveLastSequence writes the project's last-sequence record atomically.
func SaveLastSequence(projectKey string, s LastSequence) error {
	path, err := lastSequencePath(projectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(s); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}
