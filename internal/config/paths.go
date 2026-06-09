package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

const configDir = ".config/echo"

func configRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDir), nil
}

// RunLogsDir is the directory where `echo run --log` writes recipe
// transcripts: ~/.config/echo/run-logs. The caller is responsible for
// MkdirAll before writing.
func RunLogsDir() (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "run-logs"), nil
}

func projectKey(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return fmt.Sprintf("%x", sum)
}

// ProjectKey is the exported form of projectKey: the per-project config
// filename stem (`<key>.toml`) derived from a project's absolute path.
// The `connect` command uses it to locate a remote host's Echo profile
// over SSH by hashing the configured remote project path.
func ProjectKey(absPath string) string {
	return projectKey(absPath)
}
