package docker

import (
	"context"
	"os/exec"
	"strings"
)

var systemDBs = map[string]bool{
	"postgres":  true,
	"template0": true,
	"template1": true,
}

// ListDatabases runs `psql -lqt` inside dbContainer and returns the
// non-system database names. user is the PostgreSQL role (typically
// from POSTGRES_USER in the project's .env).
func ListDatabases(ctx context.Context, composeCmd, dir, dbContainer, user string) ([]string, error) {
	if user == "" {
		user = "postgres"
	}
	args := append(splitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"psql", "-U", user, "-lqt",
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var dbs []string
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "|", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" || systemDBs[name] {
			continue
		}
		dbs = append(dbs, name)
	}
	return dbs, nil
}
