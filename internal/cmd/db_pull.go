package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dbPullFlags is the parsed argument set for db-pull. neutralize is a
// tri-state: nil = auto (neutralize only when the source is prod), non-nil
// = the explicit --neutralize / --no-neutralize choice.
type dbPullFlags struct {
	asName     string
	neutralize *bool
	filestore  bool
	force      bool
	restore    bool
	from       string
	remote     bool
}

// parseDBPullArgs parses db-pull's flags. Remote switches (--from/--remote)
// are recognized via remoteFlagsIn; the rest are consumed here. Unknown
// flags are ignored (the db-command convention).
func parseDBPullArgs(args []string) dbPullFlags {
	var f dbPullFlags
	f.from, f.remote = remoteFlagsIn(args)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--as":
			if i+1 < len(args) {
				f.asName = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--as="):
			f.asName = strings.TrimPrefix(a, "--as=")
		case a == "--neutralize":
			v := true
			f.neutralize = &v
		case a == "--no-neutralize":
			v := false
			f.neutralize = &v
		case a == "--filestore":
			f.filestore = true
		case a == "--force":
			f.force = true
		case a == "--restore":
			f.restore = true
		case a == "--from":
			i++ // value consumed by remoteFlagsIn; skip it here
		}
	}
	return f
}

// sanitizeDBName lowercases s and collapses every character outside
// [a-z0-9_] to a single underscore, yielding a legal Postgres identifier
// stem (e.g. "muutrade-PROD" → "muutrade_prod").
func sanitizeDBName(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// RunDBPull dumps a remote target's database over SSH into the working
// directory's ./backups/. By default it stops there — a plain "make a
// backup on the remote and pull it down", which needs no local Docker
// stack and so runs projectless from a linked source directory. With
// --restore it also loads the dump into the local Postgres under a
// distinct name and (when the source is prod) neutralizes it; that path
// requires a local stack and self-guards via requireDBContainer. The
// remote side is always read-only (one pg_dump, plus an optional
// filestore tar) — no remote prod gate.
func RunDBPull(ctx context.Context, opts DBOpts) error {
	f := parseDBPullArgs(opts.Args)

	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, f.from, logFn)
	if err != nil {
		return err
	}

	remoteDB := rsc.prof.DBName
	if remoteDB == "" {
		return fmt.Errorf("remote profile has no db_name — cannot pull")
	}
	user := rsc.conn.User

	label := targetLabel(rsc)
	asName := f.asName
	if asName == "" {
		asName = sanitizeDBName(remoteDB + "_" + label)
	}

	// Decide neutralization: explicit flag wins; else auto — only a prod
	// source is neutralized by default.
	neutralize := strings.EqualFold(rsc.target.stage, "prod")
	if f.neutralize != nil {
		neutralize = *f.neutralize
	}

	opts.log("INFO", "", "pulling database", asName,
		[2]string{"target", label}, [2]string{"source", remoteDB},
		[2]string{"stage", rsc.target.stage})

	// --- dump: stream pg_dump's binary stdout straight into ./backups/ ---
	backupsDir := filepath.Join(opts.Root, "backups")
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		return err
	}
	ts := time.Now().Format("20060102-150405")
	outName := fmt.Sprintf("%s_%s_%s.dump", sanitizeDBName(remoteDB), sanitizeDBName(label), ts)
	outPath := filepath.Join(backupsDir, outName)

	dumpArgv := []string{"exec", "-T", rsc.prof.DBContainer, "pg_dump", "-Fc", "--no-owner"}
	if user != "" {
		dumpArgv = append(dumpArgv, "-U", user)
	}
	dumpArgv = append(dumpArgv, remoteDB)
	dumpCmd := remoteComposeCmd(rsc.remotePath, rsc.prof.ComposeCmd, dumpArgv...)

	opts.log("INFO", "dump", "streaming remote dump", asName, [2]string{"file", outName})
	if err := runSSHToFile(ctx, rsc.sshHost, dumpCmd, outPath, func(n int64) {
		opts.log("DEBUG", "dump", "pulled "+humanBytes(n), asName)
	}); err != nil {
		return fmt.Errorf("dump: %w", err)
	}
	maybeAppendGitignore(opts.Root, "backups/")

	size := int64(0)
	if fi, statErr := os.Stat(outPath); statErr == nil {
		size = fi.Size()
	}
	opts.log("INFO", "dump", "dump complete", asName, [2]string{"size", humanBytes(size)})

	// Download-only (default): keep the dump (and, with --filestore, the
	// raw filestore tar) in ./backups/ and stop. No local stack is touched,
	// so this half runs projectless from a linked source directory.
	if !f.restore {
		if f.filestore {
			fsName := fmt.Sprintf("%s_%s_%s.filestore.tar", sanitizeDBName(remoteDB), sanitizeDBName(label), ts)
			fsPath := filepath.Join(backupsDir, fsName)
			opts.log("INFO", "filestore", "streaming remote filestore", asName, [2]string{"file", fsName})
			if err := pullFilestoreArchive(ctx, opts, rsc, remoteDB, fsPath); err != nil {
				// Best-effort: the DB dump already succeeded; warn and continue.
				opts.log("WARNING", "filestore", "filestore not pulled: "+err.Error(), asName)
			}
		}
		rel, _ := filepath.Rel(opts.Root, outPath)
		if rel == "" {
			rel = outPath
		}
		opts.log("INFO", "", "pull complete", asName,
			[2]string{"file", rel}, [2]string{"size", humanBytes(size)})
		if opts.StreamOut != nil {
			opts.StreamOut("→ " + rel)
		}
		return nil
	}

	// --restore: load the dump into the local stack under the derived name.
	// This is the only half that needs a local Docker stack.
	if err := requireDBContainer(opts.Cfg); err != nil {
		return err
	}
	if err := restoreBackupFile(ctx, opts, outPath, asName, f.force, neutralize); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	// --- optional filestore ---
	if f.filestore {
		if err := pullFilestore(ctx, opts, rsc, remoteDB, asName); err != nil {
			// Best-effort: the DB pull already succeeded; warn and continue.
			opts.log("WARNING", "filestore", "filestore not pulled: "+err.Error(), asName)
		}
	}

	opts.log("INFO", "", "pull complete", asName,
		[2]string{"db", asName}, [2]string{"size", humanBytes(size)},
		[2]string{"neutralized", fmt.Sprintf("%t", neutralize)})
	if opts.StreamOut != nil {
		opts.StreamOut("→ db-use " + asName)
	}
	return nil
}

// remoteFilestoreTarCmd builds the SSH command that tars the remote Odoo
// container's filestore for remoteDB to stdout. Shared by the download-only
// (pullFilestoreArchive) and restore (pullFilestore) paths.
func remoteFilestoreTarCmd(rsc remoteShellContext, opts DBOpts, remoteDB string) string {
	parent := opts.Cfg.FilestorePath // same in-container path convention both sides
	return remoteComposeCmd(rsc.remotePath, rsc.prof.ComposeCmd,
		"exec", "-T", rsc.prof.OdooContainer, "tar", "-cf", "-", "-C", parent, remoteDB)
}

// pullFilestoreArchive streams the remote filestore tar straight to outPath
// without extracting it — the download-only counterpart of pullFilestore,
// used when db-pull runs without --restore (no local container to copy into).
func pullFilestoreArchive(ctx context.Context, opts DBOpts, rsc remoteShellContext, remoteDB, outPath string) error {
	return runSSHToFile(ctx, rsc.sshHost, remoteFilestoreTarCmd(rsc, opts, remoteDB), outPath, nil)
}

// pullFilestore tars the remote Odoo container's filestore for remoteDB,
// streams it into a temp file, extracts it locally, and copies it into the
// local Odoo container under asName — reusing the restore path's
// container-copy machinery.
func pullFilestore(ctx context.Context, opts DBOpts, rsc remoteShellContext, remoteDB, asName string) error {
	tarCmd := remoteFilestoreTarCmd(rsc, opts, remoteDB)

	tmpTar, err := os.CreateTemp("", "echo-fs-*.tar")
	if err != nil {
		return err
	}
	tmpPath := tmpTar.Name()
	tmpTar.Close()
	defer os.Remove(tmpPath)

	opts.log("INFO", "filestore", "pulling filestore", asName)
	if err := runSSHToFile(ctx, rsc.sshHost, tarCmd, tmpPath, nil); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "echo-fs-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	tf, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	if err := extractTarReader(tf, tmpDir); err != nil {
		tf.Close()
		return err
	}
	tf.Close()

	srcDir := filepath.Join(tmpDir, remoteDB)
	if _, statErr := os.Stat(srcDir); statErr != nil {
		return fmt.Errorf("remote filestore empty or missing")
	}
	opts.log("INFO", "filestore", "copying filestore into container", asName)
	return copyFilestoreToContainer(ctx, opts, asName, srcDir)
}

// humanBytes renders a byte count in a compact human unit (B/KB/MB/GB).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
