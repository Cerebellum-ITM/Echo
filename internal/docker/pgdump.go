package docker

import (
	"bufio"
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
// --no-owner --role=<user>` inside dbContainer. When onLine is non-nil,
// pg_restore runs with --verbose and every stderr line is forwarded to it
// live (so the caller can show progress during the long restore);
// error/fatal lines are still collected for the failure message.
func Restore(ctx context.Context, composeCmd, dir, dbContainer, user, db, inPath string, onLine func(string)) error {
	if user == "" {
		user = "postgres"
	}
	args := append(SplitCompose(composeCmd),
		"exec", "-T", dbContainer,
		"pg_restore", "-U", user, "-d", db, "--no-owner", "--role="+user,
	)
	if onLine != nil {
		args = append(args, "--verbose")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir

	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdin = f

	detail, werr := streamStderr(cmd, onLine)
	if werr != nil {
		return fmt.Errorf("pg_restore: %s", joinErrDetail(werr, detail))
	}
	return nil
}

// RestoreSQL loads a plain-SQL dump (e.g. an Odoo backup's dump.sql) into
// db by piping it to `psql` inside dbContainer. Used for archives that
// carry SQL text rather than a pg_dump custom-format file. psql exits
// non-zero only on fatal/connection errors (no ON_ERROR_STOP), matching a
// manual `psql < dump.sql` against a freshly-created, --no-owner dump.
func RestoreSQL(ctx context.Context, composeCmd, dir, dbContainer, user, db, inPath string, onLine func(string)) error {
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
	cmd.Stdin = f

	detail, werr := streamStderr(cmd, onLine)
	if werr != nil {
		return fmt.Errorf("psql restore: %s", joinErrDetail(werr, detail))
	}
	return nil
}

// streamStderr starts cmd, scans its stderr line by line forwarding each
// to onLine (when non-nil) for live progress, and collects the lines that
// look like errors (error/fatal) into the returned detail string for the
// failure message. It returns cmd.Wait()'s error.
func streamStderr(cmd *exec.Cmd, onLine func(string)) (string, error) {
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var errLines []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if onLine != nil {
			onLine(line)
		}
		low := strings.ToLower(line)
		if strings.Contains(low, "error") || strings.Contains(low, "fatal") {
			errLines = append(errLines, strings.TrimSpace(line))
		}
	}
	return strings.Join(errLines, "; "), cmd.Wait()
}

// joinErrDetail composes a restore failure message: the wait error, plus
// the collected error-line detail when there is any.
func joinErrDetail(werr error, detail string) string {
	if detail == "" {
		return werr.Error()
	}
	return werr.Error() + ": " + detail
}

