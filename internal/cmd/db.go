package cmd

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/theme"
)

type DBOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	StreamOut func(string)
}

var (
	ErrNoBackups      = errors.New("no backups found in ./backups/")
	ErrActiveConns    = errors.New("active connections to the database — stop Odoo first (`down odoo`)")
	ErrDBExists       = errors.New("database already exists — use --force to replace")
	ErrNoFilestore    = errors.New("no filestore directory for this database")
	ErrNoTargetDB     = errors.New("no database given")
	ErrNoDBContainer  = errors.New("no db container configured — run `init` first")
)

type dbFlags struct {
	force         bool
	withFilestore bool
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

// RunDBList prints non-system databases with size, creation date, and
// marks the active one (cfg.DBName) with a ● bullet.
func RunDBList(ctx context.Context, opts DBOpts) error {
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	infos, err := docker.ListDatabasesDetailed(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts))
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		if opts.StreamOut != nil {
			opts.StreamOut("(no databases)")
		}
		return nil
	}

	nameWidth := 12
	for _, i := range infos {
		if len(i.Name) > nameWidth {
			nameWidth = len(i.Name)
		}
	}
	sizeWidth := 10
	for _, i := range infos {
		if len(i.SizeHuman) > sizeWidth {
			sizeWidth = len(i.SizeHuman)
		}
	}

	active := opts.Cfg.DBName
	bulletActive := lipgloss.NewStyle().Foreground(opts.Palette.Success).Render("●")

	for _, i := range infos {
		mark := "  "
		if i.Name == active {
			mark = bulletActive + " "
		}
		line := fmt.Sprintf("%s%-*s  %-*s  %s",
			mark,
			nameWidth, i.Name,
			sizeWidth, i.SizeHuman,
			i.CreatedAt,
		)
		opts.StreamOut(line)
	}
	return nil
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

	filestoreDir := odooFilestorePath(db)
	hasFilestore := dirExists(filestoreDir)
	if !hasFilestore && opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("⚠ filestore not found at %s — packaging dump only", filestoreDir))
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
		if err := assertNoActiveConns(ctx, opts, target); err != nil {
			return err
		}
		if err := docker.DropDatabase(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target); err != nil {
			return err
		}
	}

	if err := docker.CreateDatabase(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target); err != nil {
		return err
	}

	if strings.HasSuffix(picked, ".zip") {
		return restoreFromZip(ctx, opts, target, picked)
	}
	if err := docker.Restore(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target, picked); err != nil {
		return err
	}
	if opts.StreamOut != nil {
		opts.StreamOut("→ " + target)
	}
	return nil
}

func restoreFromZip(ctx context.Context, opts DBOpts, target, zipPath string) error {
	tmpDir, err := os.MkdirTemp("", "echo-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if err := unzip(zipPath, tmpDir); err != nil {
		return err
	}

	dumpFile := filepath.Join(tmpDir, "dump.backup")
	if _, err := os.Stat(dumpFile); err != nil {
		return fmt.Errorf("dump.backup not found in archive: %w", err)
	}
	if err := docker.Restore(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, dbUser(opts), target, dumpFile); err != nil {
		return err
	}

	// Find a filestore/<somename>/ entry and copy it to the host
	// filestore path under the new target name.
	srcFilestore, ok := findFilestoreInDir(tmpDir)
	if !ok {
		if opts.StreamOut != nil {
			opts.StreamOut("⚠ no filestore in archive — sql only")
		}
		return nil
	}
	dst := odooFilestorePath(target)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := copyDir(srcFilestore, dst); err != nil {
		return fmt.Errorf("filestore copy: %w", err)
	}
	if opts.StreamOut != nil {
		opts.StreamOut("→ " + target + " (with filestore)")
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

	if err := assertNoActiveConns(ctx, opts, target); err != nil {
		return err
	}

	if !flags.force {
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
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(name)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  About to drop database "+red).
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

// dbNameFromBackup parses `<db>_<YYYYMMDD-HHMMSS>.{dump,zip}` and
// returns the db prefix. Falls back to the basename without extension.
func dbNameFromBackup(name string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".dump"), ".zip")
	idx := strings.LastIndex(base, "_")
	if idx <= 0 {
		return base
	}
	suffix := base[idx+1:]
	// timestamp = 8 digits + '-' + 6 digits = 15 chars
	if len(suffix) == 15 && suffix[8] == '-' {
		return base[:idx]
	}
	return base
}

func odooFilestorePath(db string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "Odoo", "filestore", db)
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
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

func findFilestoreInDir(root string) (string, bool) {
	fs := filepath.Join(root, "filestore")
	entries, err := os.ReadDir(fs)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(fs, e.Name()), true
		}
	}
	return "", false
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
