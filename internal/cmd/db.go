package cmd

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

type DBOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	StreamOut func(string)
	Log       DBLogger
}

// DBLogger receives a structured progress event from a db command so the
// REPL can render it as an Odoo-style line (echo.<cmd>.<step>). step is the
// sub-logger segment; db names the database the step acts on (empty → the
// session falls back to the active DB). Optional: a nil Log means the
// command runs without progress narration.
type DBLogger func(level, step, msg, db string, fields ...[2]string)

// log emits a progress event through opts.Log when set; no-op otherwise.
func (o DBOpts) log(level, step, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, step, msg, db, fields...)
	}
}

// restoreLineLogger returns the onLine callback fed to docker.Restore /
// RestoreSQL: it strips pg_restore's `pg_restore: ` prefix, drops blank
// lines, and emits each surviving line as a subdued DEBUG progress line
// under echo.db-restore.restore for target.
func restoreLineLogger(opts DBOpts, target string) func(string) {
	return func(line string) {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "pg_restore:"))
		if line == "" {
			return
		}
		opts.log("DEBUG", "restore", line, target)
	}
}

var (
	ErrNoBackups     = errors.New("no backups found in ./backups/")
	ErrActiveConns   = errors.New("active connections to the database — pass --force to terminate them and drop, or stop Odoo first (`down odoo`)")
	ErrDBExists      = errors.New("database already exists — use --force to replace")
	ErrNoFilestore   = errors.New("no filestore directory for this database")
	ErrNoTargetDB    = errors.New("no database given")
	ErrNoDBContainer = errors.New("no db container configured — run `init` first")
)

type dbFlags struct {
	force         bool
	withFilestore bool
	neutralize    bool
	asName        string
}

func parseDBArgs(args []string) (dbFlags, []string) {
	var f dbFlags
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--force":
			f.force = true
		case a == "--with-filestore":
			f.withFilestore = true
		case a == "--neutralize":
			f.neutralize = true
		case a == "--as":
			if i+1 < len(args) {
				f.asName = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--as="):
			f.asName = strings.TrimPrefix(a, "--as=")
		case strings.HasPrefix(a, "-"):
			// unknown flag — ignore
		default:
			positional = append(positional, a)
		}
	}
	return f, positional
}

func requireDBContainer(cfg *config.Config) error {
	if cfg.DBContainer == "" {
		return ErrNoDBContainer
	}
	return nil
}

func dbUser(opts DBOpts) string {
	return env.Load(opts.Root)["POSTGRES_USER"]
}

// DBList returns the non-system databases with size and creation date for
// Echo's styled `db-list` table (the REPL renders it and marks the active
// one). Replaces the old self-printing RunDBList.
func DBList(ctx context.Context, opts DBOpts) ([]docker.DatabaseInfo, error) {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return nil, err
	}
	return docker.ListDatabasesDetailed(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts))
}

// RunDBBackup dumps the target database to ./backups/. The target
// defaults to cfg.DBName; a positional arg overrides it. With
// --with-filestore, also packages ~/.local/share/Odoo/filestore/<db>
// into a .zip alongside the dump.
func RunDBBackup(ctx context.Context, opts DBOpts) error {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	flags, positional := parseDBArgs(opts.Args)

	target := opts.Cfg.DBName
	if len(positional) > 0 {
		target = positional[0]
	}
	if target == "" {
		return ErrNoTargetDB
	}

	if err := assertNoActiveConns(ctx, opts, target); err != nil {
		return err
	}

	backupsDir := filepath.Join(opts.Root, "backups")
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		return err
	}

	ts := time.Now().Format("20060102-150405")
	ext := "dump"
	if flags.withFilestore {
		ext = "zip"
	}
	outName := fmt.Sprintf("%s_%s.%s", target, ts, ext)
	outPath := filepath.Join(backupsDir, outName)

	if flags.withFilestore {
		if err := backupWithFilestore(ctx, opts, target, outPath); err != nil {
			return err
		}
	} else {
		if err := docker.Dump(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target, outPath); err != nil {
			return err
		}
	}

	maybeAppendGitignore(opts.Root, "backups/")

	if opts.StreamOut != nil {
		rel, _ := filepath.Rel(opts.Root, outPath)
		if rel == "" {
			rel = outPath
		}
		opts.StreamOut("→ " + rel)
	}
	return nil
}

func backupWithFilestore(ctx context.Context, opts DBOpts, db, outPath string) error {
	tmpDump, err := os.CreateTemp("", "echo-dump-*.dump")
	if err != nil {
		return err
	}
	tmpDump.Close()
	defer os.Remove(tmpDump.Name())

	if err := docker.Dump(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), db, tmpDump.Name()); err != nil {
		return err
	}

	// Pull the filestore out of the Odoo container (that's where it
	// actually lives in a Dockerized Odoo).
	filestoreDir, cleanup, hasFilestore := pullContainerFilestore(ctx, opts, db)
	defer cleanup()
	if !hasFilestore && opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("⚠ filestore not found in container at %s/%s — packaging dump only", opts.Cfg.FilestorePath, db))
	}

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	if err := addFileToZip(zw, tmpDump.Name(), "dump.backup"); err != nil {
		return err
	}
	if hasFilestore {
		if err := addDirToZip(zw, filestoreDir, "filestore/"+db); err != nil {
			return err
		}
	}
	return nil
}

// pullContainerFilestore copies <FilestorePath>/<db> out of the Odoo
// container into a host temp dir and returns its path, a cleanup func,
// and whether a filestore was found. ok=false (with a no-op cleanup)
// when the container or filestore dir is missing — callers package the
// dump only.
func pullContainerFilestore(ctx context.Context, opts DBOpts, db string) (string, func(), bool) {
	noop := func() {}
	id, err := docker.ContainerID(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer)
	if err != nil {
		return "", noop, false
	}
	containerSrc := opts.Cfg.FilestorePath + "/" + db
	if err := docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, []string{"test", "-d", containerSrc}, func(string) {}); err != nil {
		return "", noop, false
	}
	tmp, err := os.MkdirTemp("", "echo-fs-*")
	if err != nil {
		return "", noop, false
	}
	cleanup := func() { os.RemoveAll(tmp) }
	if err := docker.CopyFromContainer(ctx, id, containerSrc, tmp); err != nil {
		cleanup()
		return "", noop, false
	}
	return filepath.Join(tmp, db), cleanup, true
}

// RunDBRestore opens a single-select picker over ./backups/*.{dump,zip},
// derives the target DB name from the filename (or --as), and restores.
// If the target exists and --force isn't set, returns ErrDBExists.
func RunDBRestore(ctx context.Context, opts DBOpts) error {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	flags, _ := parseDBArgs(opts.Args)

	files, err := listBackupFiles(opts.Root)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return ErrNoBackups
	}

	labels := make([]string, len(files))
	for i, f := range files {
		labels[i] = filepath.Base(f)
	}
	chosen, err := runSingleFuzzyPicker("Pick a backup to restore", labels, opts.Palette)
	if err != nil {
		return err
	}
	var picked string
	for _, f := range files {
		if filepath.Base(f) == chosen {
			picked = f
			break
		}
	}

	target := flags.asName
	if target == "" {
		target = dbNameFromBackup(filepath.Base(picked))
	}
	if target == "" {
		return ErrNoTargetDB
	}

	exists, err := docker.DatabaseExists(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target)
	if err != nil {
		return err
	}
	if exists {
		if !flags.force {
			return fmt.Errorf("%w (%q)", ErrDBExists, target)
		}
		opts.log("INFO", "restore", "dropping existing database", target)
		if err := docker.TerminateConnections(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target); err != nil {
			return err
		}
		if err := docker.DropDatabase(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target); err != nil {
			return err
		}
	}

	opts.log("INFO", "restore", "creating database", target)
	if err := docker.CreateDatabase(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target); err != nil {
		return err
	}

	note := ""
	if strings.HasSuffix(picked, ".zip") {
		n, err := restoreFromZip(ctx, opts, target, picked)
		if err != nil {
			return err
		}
		note = n
	} else {
		opts.log("INFO", "restore", "restoring data", target, [2]string{"file", filepath.Base(picked)})
		if err := docker.Restore(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target, picked, restoreLineLogger(opts, target)); err != nil {
			return err
		}
	}

	// --neutralize: turn the freshly restored copy into a safe,
	// non-production DB. Run last so a neutralize failure surfaces after
	// the data is already in place.
	if flags.neutralize {
		opts.log("INFO", "restore", "neutralizing", target)
		if err := neutralizeDB(ctx, opts, target); err != nil {
			return err
		}
		note += " (neutralized)"
	}

	if opts.StreamOut != nil {
		opts.StreamOut("→ " + target + note)
	}
	return nil
}

// restoreFromZip restores the dump inside a .zip and copies its
// filestore into the Odoo container. It returns a footer suffix
// describing the outcome (" (with filestore)" or "") so the caller can
// compose the final `→ <target>` line; ⚠ warnings are still printed
// here.
func restoreFromZip(ctx context.Context, opts DBOpts, target, zipPath string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "echo-restore-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	opts.log("INFO", "restore", "extracting archive", target, [2]string{"file", filepath.Base(zipPath)})
	if err := unzip(zipPath, tmpDir); err != nil {
		return "", err
	}

	onLine := restoreLineLogger(opts, target)

	// Pick the restore path by which dump the archive carries: Echo's own
	// backups ship a pg_dump custom-format `dump.backup` (pg_restore),
	// while a native Odoo backup ships a plain `dump.sql` (psql).
	switch {
	case fileExists(filepath.Join(tmpDir, "dump.backup")):
		opts.log("INFO", "restore", "restoring data", target, [2]string{"format", "custom"})
		if err := docker.Restore(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target, filepath.Join(tmpDir, "dump.backup"), onLine); err != nil {
			return "", err
		}
	case fileExists(filepath.Join(tmpDir, "dump.sql")):
		opts.log("INFO", "restore", "restoring data", target, [2]string{"format", "sql"})
		if err := docker.RestoreSQL(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target, filepath.Join(tmpDir, "dump.sql"), onLine); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("no dump.backup or dump.sql found in archive")
	}

	// Copy the filestore into the Odoo container under the new db name —
	// Odoo reads it from there, not from the host.
	srcFilestore, ok := findFilestoreInDir(tmpDir)
	if !ok {
		if opts.StreamOut != nil {
			opts.StreamOut("⚠ no filestore in archive — sql only")
		}
		return "", nil
	}
	opts.log("INFO", "restore", "copying filestore", target)
	if err := copyFilestoreToContainer(ctx, opts, target, srcFilestore); err != nil {
		return "", fmt.Errorf("filestore copy: %w", err)
	}
	return " (with filestore)", nil
}

// copyFilestoreToContainer copies the unzipped filestore directory into
// the Odoo container at <FilestorePath>/<target>/ via `docker cp`, then
// best-effort fixes ownership so Odoo can read existing and write new
// attachments (docker cp leaves files root-owned).
func copyFilestoreToContainer(ctx context.Context, opts DBOpts, target, srcDir string) error {
	id, err := docker.ContainerID(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer)
	if err != nil {
		return err
	}
	dst := opts.Cfg.FilestorePath + "/" + target
	if err := docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, []string{"mkdir", "-p", dst}, func(string) {}); err != nil {
		return err
	}
	if err := docker.CopyToContainer(ctx, id, srcDir+"/.", dst); err != nil {
		return err
	}
	chown := fmt.Sprintf(`chown -R "$(stat -c '%%u:%%g' '%s')" '%s'`, opts.Cfg.FilestorePath, dst)
	if err := docker.ExecAsRoot(ctx, id, "sh", "-c", chown); err != nil && opts.StreamOut != nil {
		opts.StreamOut("⚠ filestore copied but chown failed — Odoo can read it but may not write new attachments")
	}
	return nil
}

// RunDBDrop drops a database with a red huh.Confirm by default.
// --force skips the confirmation.
func RunDBDrop(ctx context.Context, opts DBOpts) error {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	flags, positional := parseDBArgs(opts.Args)

	target := ""
	if len(positional) > 0 {
		target = positional[0]
	} else {
		// Pick interactively when no name was given.
		names, err := docker.ListDatabases(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts))
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return errors.New("no databases to drop")
		}
		picked, err := runSingleFuzzyPicker("Pick a database to drop", names, opts.Palette)
		if err != nil {
			return err
		}
		target = picked
	}
	if target == "" {
		return ErrNoTargetDB
	}

	// --force also clears whatever is holding the DB open; without it the
	// guard still protects a live DB from an accidental drop.
	if flags.force {
		if err := docker.TerminateConnections(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target); err != nil {
			return err
		}
	} else {
		if err := assertNoActiveConns(ctx, opts, target); err != nil {
			return err
		}
		if err := confirmDrop(opts.Palette, target); err != nil {
			return err
		}
	}

	if err := docker.DropDatabase(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target); err != nil {
		return err
	}
	if opts.StreamOut != nil {
		opts.StreamOut("→ " + target)
	}
	return nil
}

func confirmDrop(palette theme.Palette, name string) error {
	if err := requireTTY("pass --force to drop without a prompt"); err != nil {
		return err
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(name)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  About to drop database " + red).
			Description("This cannot be undone.").
			Affirmative("Drop").
			Negative("Cancel").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}

// RunDBNeutralize runs `odoo neutralize` against the target DB, applying
// Odoo's per-module neutralization SQL (disables mail/fetchmail servers,
// crons, payment providers, the environment ribbon, …). Target defaults
// to cfg.DBName; a positional arg overrides it; if neither is set, a
// picker is shown. Confirms (red) when the target is the active DB or
// stage=prod — neutralizing those wipes real config — unless --force is
// passed.
func RunDBNeutralize(ctx context.Context, opts DBOpts) error {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	flags, positional := parseDBArgs(opts.Args)

	target := opts.Cfg.DBName
	if len(positional) > 0 {
		target = positional[0]
	}
	if target == "" {
		names, err := docker.ListDatabases(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts))
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return errors.New("no databases to neutralize")
		}
		picked, err := runSingleFuzzyPicker("Pick a database to neutralize", names, opts.Palette)
		if err != nil {
			return err
		}
		target = picked
	}
	if target == "" {
		return ErrNoTargetDB
	}

	// Guard the dangerous targets: neutralizing the active DB or a prod
	// stage destroys real mail/payment configuration.
	if !flags.force && (target == opts.Cfg.DBName || strings.EqualFold(opts.Cfg.Stage, "prod")) {
		if err := confirmNeutralize(opts.Palette, target); err != nil {
			return err
		}
	}

	if err := neutralizeDB(ctx, opts, target); err != nil {
		return err
	}
	if opts.StreamOut != nil {
		opts.StreamOut("→ " + target + " (neutralized)")
	}
	return nil
}

// neutralizeDB runs `odoo neutralize -d <target>` inside the Odoo
// container, streaming its output. Shared by RunDBNeutralize (after its
// guard) and RunDBRestore (--neutralize). No active-connection guard:
// neutralization runs fine while Odoo is up.
func neutralizeDB(ctx context.Context, opts DBOpts, target string) error {
	envVars := env.Load(opts.Root)
	conn := odoo.Conn{
		DB:       target,
		Host:     opts.Cfg.DBContainer,
		Port:     envVars["POSTGRES_PORT"],
		User:     envVars["POSTGRES_USER"],
		Password: envVars["POSTGRES_PASSWORD"],
	}
	if conn.Port == "" {
		conn.Port = "5432"
	}
	stream := func(string) {}
	if opts.StreamOut != nil {
		stream = opts.StreamOut
	}
	return docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, odoo.Neutralize(conn), stream)
}

func confirmNeutralize(palette theme.Palette, name string) error {
	if err := requireTTY("pass --force to neutralize without a prompt"); err != nil {
		return err
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(name)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  About to neutralize database " + red).
			Description("Disables mail servers, crons, and payment providers. Don't run this on production.").
			Affirmative("Neutralize").
			Negative("Cancel").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}

// Admin-reset constants: Odoo's admin user is id 2 (id 1 is the system
// superuser), and we reset both its login and password to "admin".
const (
	adminUserID   = 2
	adminLogin    = "admin"
	adminPassword = "admin"
)

// RunDBAdmin resets the login and password of user id 2 (Odoo's admin
// user) to admin/admin so you can sign into the back office without
// knowing the current credentials. Target defaults to cfg.DBName; a
// positional arg overrides it; if neither resolves, a picker is shown.
// The password is stored in plain text and Odoo re-hashes it on the next
// successful login. Confirms (red) when stage=prod — resetting prod admin
// to a known password is a security hole — unless --force is passed.
func RunDBAdmin(ctx context.Context, opts DBOpts) error {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	flags, positional := parseDBArgs(opts.Args)

	target := opts.Cfg.DBName
	if len(positional) > 0 {
		target = positional[0]
	}
	if target == "" {
		names, err := docker.ListDatabases(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts))
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return errors.New("no databases available")
		}
		picked, err := runSingleFuzzyPicker("Pick a database to reset admin on", names, opts.Palette)
		if err != nil {
			return err
		}
		target = picked
	}
	if target == "" {
		return ErrNoTargetDB
	}

	// Guard prod: a known admin/admin on production is a security hole.
	if !flags.force && strings.EqualFold(opts.Cfg.Stage, "prod") {
		if err := confirmAdminReset(opts.Palette, target); err != nil {
			return err
		}
	}

	found, err := docker.ResetUserCredentials(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target, adminUserID, adminLogin, adminPassword)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no user with id %d in %q", adminUserID, target)
	}
	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("→ %s  %s / %s (uid %d)", target, adminLogin, adminPassword, adminUserID))
	}
	return nil
}

func confirmAdminReset(palette theme.Palette, name string) error {
	if err := requireTTY("pass --force to reset admin without a prompt"); err != nil {
		return err
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(name)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  About to reset admin on " + red).
			Description("Sets the admin login and password to admin/admin. Don't run this on production.").
			Affirmative("Reset").
			Negative("Cancel").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelled
	}
	return nil
}

// RunDBUse switches the project's active database (cfg.DBName) — the one
// db-list marks with ●, and the implicit target of update/shell/psql/
// modstate/db-admin/…. With no positional arg it opens a picker over the
// database list; with a name it switches to that DB after verifying it
// exists. The change is persisted to the project config (db_name) so it
// survives restarts; because the session holds the same *config.Config,
// the prompt picks up the new DB on the next render. Switching to the DB
// already active is a reported no-op.
func RunDBUse(ctx context.Context, opts DBOpts) error {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	_, positional := parseDBArgs(opts.Args)

	names, err := docker.ListDatabases(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("no databases available")
	}

	target := ""
	if len(positional) > 0 {
		target = positional[0]
		found := false
		for _, n := range names {
			if n == target {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no database named %q", target)
		}
	} else {
		picked, err := runSingleFuzzyPicker("Pick the active database", names, opts.Palette)
		if err != nil {
			return err
		}
		target = picked
	}
	if target == "" {
		return ErrNoTargetDB
	}

	if target == opts.Cfg.DBName {
		if opts.StreamOut != nil {
			opts.StreamOut("→ " + target + " (already active)")
		}
		return nil
	}

	opts.Cfg.DBName = target
	if err := config.SaveProject(opts.Cfg); err != nil {
		return err
	}
	if opts.StreamOut != nil {
		opts.StreamOut("→ " + target)
	}
	return nil
}

func assertNoActiveConns(ctx context.Context, opts DBOpts, db string) error {
	n, err := docker.ActiveConnections(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), db)
	if err != nil {
		// Active-conns query failure shouldn't block (DB may not exist
		// yet, etc.). Treat as zero and let the actual op surface the
		// real error.
		return nil
	}
	if n > 0 {
		return fmt.Errorf("%w (%d open on %q)", ErrActiveConns, n, db)
	}
	return nil
}

func listBackupFiles(root string) ([]string, error) {
	dir := filepath.Join(root, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".dump") || strings.HasSuffix(name, ".zip") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Slice(files, func(i, j int) bool {
		fi, _ := os.Stat(files[i])
		fj, _ := os.Stat(files[j])
		return fi.ModTime().After(fj.ModTime())
	})
	return files, nil
}

// odooBackupTS matches the timestamp Odoo's database manager appends to a
// backup download: `_YYYY-MM-DD_HH-MM-SS` at the end of the name.
var odooBackupTS = regexp.MustCompile(`_\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}$`)

// dbNameFromBackup returns the db prefix from a backup filename, stripping
// either Echo's `<db>_YYYYMMDD-HHMMSS` suffix or Odoo's
// `<db>_YYYY-MM-DD_HH-MM-SS` suffix. Falls back to the basename without
// extension.
func dbNameFromBackup(name string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".dump"), ".zip")
	if loc := odooBackupTS.FindStringIndex(base); loc != nil {
		return base[:loc[0]]
	}
	idx := strings.LastIndex(base, "_")
	if idx <= 0 {
		return base
	}
	suffix := base[idx+1:]
	// Echo timestamp = 8 digits + '-' + 6 digits = 15 chars
	if len(suffix) == 15 && suffix[8] == '-' {
		return base[:idx]
	}
	return base
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// isHexPrefix reports whether name is a 2-character lowercase-hex
// directory name — the shape Odoo uses to shard a filestore
// (`filestore/<XX>/<sha>`).
func isHexPrefix(name string) bool {
	if len(name) != 2 {
		return false
	}
	for _, c := range name {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func maybeAppendGitignore(root, line string) {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		// No .gitignore → don't create one.
		return
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return
		}
	}
	suffix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		suffix = "\n"
	}
	_ = os.WriteFile(path, append(data, []byte(suffix+line+"\n")...), 0o644)
}

func addFileToZip(zw *zip.Writer, srcPath, zipPath string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := zw.Create(zipPath)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func addDirToZip(zw *zip.Writer, srcDir, zipPrefix string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		return addFileToZip(zw, path, filepath.ToSlash(filepath.Join(zipPrefix, rel)))
	})
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		out := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(out, filepath.Clean(dst)+string(os.PathSeparator)) && out != filepath.Clean(dst) {
			return fmt.Errorf("zip slip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, f.Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(w, rc); err != nil {
			w.Close()
			rc.Close()
			return err
		}
		w.Close()
		rc.Close()
	}
	return nil
}

// findFilestoreInDir returns the directory that directly holds the
// 2-char filestore prefix dirs, handling both layouts: an Odoo backup
// shards them straight under `filestore/<XX>/…`, while an Echo backup
// nests them under the db name (`filestore/<db>/<XX>/…`).
func findFilestoreInDir(root string) (string, bool) {
	fs := filepath.Join(root, "filestore")
	entries, err := os.ReadDir(fs)
	if err != nil {
		return "", false
	}
	dirs, prefixDirs := 0, 0
	firstDir := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirs++
		if firstDir == "" {
			firstDir = e.Name()
		}
		if isHexPrefix(e.Name()) {
			prefixDirs++
		}
	}
	if dirs == 0 {
		return "", false
	}
	if prefixDirs == dirs {
		return fs, true // Odoo: prefix dirs directly under filestore/
	}
	return filepath.Join(fs, firstDir), true // Echo: filestore/<db>/<XX>/…
}
