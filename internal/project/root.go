package project

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrNoRoot = errors.New("no docker-compose.yml found in cwd or any parent")

// FindRoot walks up from cwd looking for a directory containing
// docker-compose.yml or docker-compose.yaml. Returns ErrNoRoot if
// the filesystem root is reached without finding one.
func FindRoot(cwd string) (string, error) {
	dir := cwd
	for {
		for _, name := range []string{"docker-compose.yml", "docker-compose.yaml"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoRoot
		}
		dir = parent
	}
}
