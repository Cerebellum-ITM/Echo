package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
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

// DatabaseInfo describes one row returned by ListDatabasesDetailed.
type DatabaseInfo struct {
	Name      string
	SizeBytes int64
	SizeHuman string
	CreatedAt string // YYYY-MM-DD or "—" when unavailable
}

// ListDatabasesDetailed returns size and creation date for each
// non-system database. Creation date comes from pg_stat_file on the
// database's base directory; if the cluster doesn't grant access to
// pg_stat_file, CreatedAt is left as "—".
func ListDatabasesDetailed(ctx context.Context, composeCmd, dir, dbContainer, user string) ([]DatabaseInfo, error) {
	if user == "" {
		user = "postgres"
	}
	query := `SELECT d.datname,
                 pg_database_size(d.datname)::text,
                 pg_size_pretty(pg_database_size(d.datname)),
                 COALESCE(to_char((pg_stat_file('base/'||d.oid||'/PG_VERSION')).modification, 'YYYY-MM-DD'), '—')
          FROM pg_database d
          WHERE NOT d.datistemplate AND d.datname <> 'postgres'
          ORDER BY d.datname;`
	args := append(splitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"psql", "-U", user, "-d", "postgres", "-At", "-F", "|", "-c", query,
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Fallback for postgres versions / roles that can't read
		// pg_stat_file: drop the creation-date column.
		return listDatabasesSizeOnly(ctx, composeCmd, dir, dbContainer, user)
	}
	return parseDBInfo(string(out)), nil
}

func listDatabasesSizeOnly(ctx context.Context, composeCmd, dir, dbContainer, user string) ([]DatabaseInfo, error) {
	query := `SELECT datname,
                 pg_database_size(datname)::text,
                 pg_size_pretty(pg_database_size(datname))
          FROM pg_database
          WHERE NOT datistemplate AND datname <> 'postgres'
          ORDER BY datname;`
	args := append(splitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"psql", "-U", user, "-d", "postgres", "-At", "-F", "|", "-c", query,
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var infos []DatabaseInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 3 || parts[0] == "" {
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		infos = append(infos, DatabaseInfo{
			Name:      parts[0],
			SizeBytes: size,
			SizeHuman: parts[2],
			CreatedAt: "—",
		})
	}
	return infos, nil
}

func parseDBInfo(raw string) []DatabaseInfo {
	var infos []DatabaseInfo
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 4 || parts[0] == "" {
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		infos = append(infos, DatabaseInfo{
			Name:      parts[0],
			SizeBytes: size,
			SizeHuman: parts[2],
			CreatedAt: parts[3],
		})
	}
	return infos
}

// ActiveConnections returns the number of sessions connected to db
// other than the caller's own backend.
func ActiveConnections(ctx context.Context, composeCmd, dir, dbContainer, user, db string) (int, error) {
	if user == "" {
		user = "postgres"
	}
	query := `SELECT count(*) FROM pg_stat_activity WHERE datname = '` + escapeIdent(db) + `' AND pid <> pg_backend_pid();`
	out, err := psqlScalar(ctx, composeCmd, dir, dbContainer, user, "postgres", query)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse active connections: %w", err)
	}
	return n, nil
}

// DatabaseExists reports whether a database with the given name exists.
func DatabaseExists(ctx context.Context, composeCmd, dir, dbContainer, user, db string) (bool, error) {
	if user == "" {
		user = "postgres"
	}
	query := `SELECT 1 FROM pg_database WHERE datname = '` + escapeIdent(db) + `';`
	out, err := psqlScalar(ctx, composeCmd, dir, dbContainer, user, "postgres", query)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

// CreateDatabase issues CREATE DATABASE "<db>" OWNER "<user>".
func CreateDatabase(ctx context.Context, composeCmd, dir, dbContainer, user, db string) error {
	if user == "" {
		user = "postgres"
	}
	stmt := fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s";`, db, user)
	return psqlExec(ctx, composeCmd, dir, dbContainer, user, "postgres", stmt)
}

// DropDatabase issues DROP DATABASE "<db>".
func DropDatabase(ctx context.Context, composeCmd, dir, dbContainer, user, db string) error {
	if user == "" {
		user = "postgres"
	}
	stmt := fmt.Sprintf(`DROP DATABASE "%s";`, db)
	return psqlExec(ctx, composeCmd, dir, dbContainer, user, "postgres", stmt)
}

func psqlScalar(ctx context.Context, composeCmd, dir, dbContainer, user, db, query string) (string, error) {
	args := append(splitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"psql", "-U", user, "-d", db, "-At", "-c", query,
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func psqlExec(ctx context.Context, composeCmd, dir, dbContainer, user, db, stmt string) error {
	args := append(splitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"psql", "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1", "-c", stmt,
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// escapeIdent escapes single quotes for use in psql literal contexts.
func escapeIdent(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
