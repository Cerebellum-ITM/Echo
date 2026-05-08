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

func projectKey(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return fmt.Sprintf("%x", sum)
}
