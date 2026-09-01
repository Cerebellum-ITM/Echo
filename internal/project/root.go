package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// GitRoot returns the top level of the git repository containing dir.
// Empty when dir is not in a repository, or git is unavailable.
func GitRoot(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SameDir reports whether two paths name the same directory, resolving
// symlinks so a repo reached through /tmp is not mistaken for a different
// project than the same repo reached through /private/tmp.
func SameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	return erra == nil && errb == nil && ra == rb
}
