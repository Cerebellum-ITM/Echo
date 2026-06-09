package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Dump runs `pg_dump -Fc -U <user> <db>` inside dbContainer and writes
// the binary stdout to outPath. Stderr is captured and surfaced in the
// returned error.
func Dump(ctx context.Context, composeCmd, dir, dbContainer, user, db, outPath string) error {
	if user == "" {
		user = "postgres"
	}
	args := append(SplitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"pg_dump", "-U", user, "-Fc", db,
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var stderr strings.Builder
	cmd.Stdout = f
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(outPath)
		return fmt.Errorf("pg_dump: %s: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Restore pipes the contents of inPath into `pg_restore -d <db>
// --no-owner --role=<user>` inside dbContainer.
func Restore(ctx context.Context, composeCmd, dir, dbContainer, user, db, inPath string) error {
	if user == "" {
		user = "postgres"
	}
	args := append(SplitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"pg_restore", "-U", user, "-d", db, "--no-owner", "--role="+user,
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir

	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var stderr strings.Builder
	cmd.Stdin = f
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %s: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// RestoreSQL loads a plain-SQL dump (e.g. an Odoo backup's dump.sql) into
// db by piping it to `psql` inside dbContainer. Used for archives that
// carry SQL text rather than a pg_dump custom-format file. psql exits
// non-zero only on fatal/connection errors (no ON_ERROR_STOP), matching a
// manual `psql < dump.sql` against a freshly-created, --no-owner dump.
func RestoreSQL(ctx context.Context, composeCmd, dir, dbContainer, user, db, inPath string) error {
	if user == "" {
		user = "postgres"
	}
	args := append(SplitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"psql", "-q", "-U", user, "-d", db,
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir

	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var stderr strings.Builder
	cmd.Stdin = f
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql restore: %s: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

