package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

// logFn is the Odoo-style progress logger passed around the checkpoint
// helpers (nil-safe at every call site via ckptLog).
type logFn = func(level, sub, msg, db string, fields ...[2]string)

// CheckpointInfo is the metric summary of a created checkpoint, surfaced in
// the deploy result and the `--json` output.
type CheckpointInfo struct {
	Name    string `json:"name"`
	Method  string `json:"method"`
	Size    string `json:"size"`
	TookSec int    `json:"took_sec"`
}

// Package-level seams so tests can script the SSH transport in place of the
// real ssh binary. ckptRunSSH buffers (short scalars / DDL); ckptRunSSHStream
// streams (long dump/restore) through the same callback deploy uses.
var (
	ckptRunSSH       = runSSH
	ckptRunSSHStream = runSSHStream
)

// checkpointDir is the remote project-relative directory the "dump" method
// keeps its dumps in (created on demand), mirroring the local ./backups/.
const checkpointDir = "backups/checkpoints"

// remoteStopApp stops ONLY the Odoo app service, leaving the Postgres
// container running. The checkpoint and rollback commands (psql, pg_dump,
// pg_restore) execute inside the DB container, so a full `compose stop` — which
// takes the DB container down too — makes them fail with "service db is not
// running". Stopping just the app clears the source DB's sessions while keeping
// it queryable for the copy/restore.
func remoteStopApp(rsc remoteShellContext) string {
	return remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "stop", rsc.target.odooContainer)
}

// ckptLog invokes log only when it is set.
func ckptLog(log logFn, level, sub, msg, db string, fields ...[2]string) {
	if log != nil {
		log(level, sub, msg, db, fields...)
	}
}

// pgUserFor is the Postgres role for the remote target's psql/pg_dump
// invocations, defaulting to "odoo" (the same fallback remoteModuleStates
// uses) when the remote `.env` didn't yield a POSTGRES_USER.
func pgUserFor(rsc remoteShellContext) string {
	if rsc.conn.User != "" {
		return rsc.conn.User
	}
	return "odoo"
}

// sqlLit doubles single quotes so s is safe inside a psql string literal.
func sqlLit(s string) string { return strings.ReplaceAll(s, "'", "''") }

// remotePsqlScalar runs a single-value query against db in the remote
// Postgres container and returns the trimmed scalar.
func remotePsqlScalar(ctx context.Context, rsc remoteShellContext, db, query string) (string, error) {
	argv := odoo.Cmd{"psql", "-U", pgUserFor(rsc), "-d", db, "-At", "-c", query}
	out, err := ckptRunSSH(ctx, rsc.sshHost, remoteDBCmd(rsc.remotePath, rsc.target, argv), nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// remotePsqlExec runs a DDL/utility statement against db in the remote
// Postgres container, stopping on the first SQL error.
func remotePsqlExec(ctx context.Context, rsc remoteShellContext, db, stmt string) error {
	argv := odoo.Cmd{"psql", "-U", pgUserFor(rsc), "-d", db, "-v", "ON_ERROR_STOP=1", "-c", stmt}
	_, err := ckptRunSSH(ctx, rsc.sshHost, remoteDBCmd(rsc.remotePath, rsc.target, argv), nil)
	return err
}

// remotePGVersionNum returns the remote server's server_version_num (e.g.
// 150004 for PG 15.4), or 0 when it can't be read.
func remotePGVersionNum(ctx context.Context, rsc remoteShellContext) int {
	out, err := remotePsqlScalar(ctx, rsc, "postgres", "SHOW server_version_num")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// remoteDBSize returns the on-disk size of db in bytes (pg_database_size).
func remoteDBSize(ctx context.Context, rsc remoteShellContext, db string) (int64, error) {
	out, err := remotePsqlScalar(ctx, rsc, "postgres",
		"SELECT pg_database_size('"+sqlLit(db)+"')")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// remoteDataDir returns the remote cluster's data_directory path.
func remoteDataDir(ctx context.Context, rsc remoteShellContext) (string, error) {
	return remotePsqlScalar(ctx, rsc, "postgres", "SHOW data_directory")
}

// remoteDiskFreeBytes returns the available bytes on the filesystem holding
// path, via `df -Pk` inside the remote Postgres container (POSIX output:
// the "Available" column is field 4, in KiB).
func remoteDiskFreeBytes(ctx context.Context, rsc remoteShellContext, path string) (int64, error) {
	argv := odoo.Cmd{"df", "-Pk", path}
	out, err := ckptRunSSH(ctx, rsc.sshHost, remoteDBCmd(rsc.remotePath, rsc.target, argv), nil)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, fmt.Errorf("unexpected df row: %q", lines[len(lines)-1])
	}
	kib, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0, err
	}
	return kib * 1024, nil
}

// remoteListDatabases lists every non-template database on the remote server.
func remoteListDatabases(ctx context.Context, rsc remoteShellContext) ([]string, error) {
	out, err := remotePsqlScalar(ctx, rsc, "postgres",
		"SELECT datname FROM pg_database WHERE NOT datistemplate")
	if err != nil {
		return nil, err
	}
	var dbs []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && l != "postgres" {
			dbs = append(dbs, l)
		}
	}
	return dbs, nil
}

// remoteTerminateConns force-closes every other backend connected to db, so
// it can be dropped, renamed, or used as a CREATE DATABASE template.
func remoteTerminateConns(ctx context.Context, rsc remoteShellContext, db string) error {
	q := "SELECT pg_terminate_backend(pid) FROM pg_stat_activity " +
		"WHERE datname = '" + sqlLit(db) + "' AND pid <> pg_backend_pid()"
	return remotePsqlExec(ctx, rsc, "postgres", q)
}

// remoteCreateFromTemplate copies src into a new database dst via CREATE
// DATABASE … TEMPLATE. fileCopy adds STRATEGY FILE_COPY (fast for big DBs) —
// pass it only on PG 15+, whose default WAL_LOG strategy is slower.
func remoteCreateFromTemplate(ctx context.Context, rsc remoteShellContext, src, dst string, fileCopy bool) error {
	stmt := fmt.Sprintf(`CREATE DATABASE "%s" TEMPLATE "%s" OWNER "%s"`, dst, src, pgUserFor(rsc))
	if fileCopy {
		stmt += " STRATEGY FILE_COPY"
	}
	return remotePsqlExec(ctx, rsc, "postgres", stmt)
}

// remoteRenameDB renames database from to to.
func remoteRenameDB(ctx context.Context, rsc remoteShellContext, from, to string) error {
	return remotePsqlExec(ctx, rsc, "postgres",
		fmt.Sprintf(`ALTER DATABASE "%s" RENAME TO "%s"`, from, to))
}

// remoteSetAllowConns toggles a database's ALLOW_CONNECTIONS. Setting it false
// on a checkpoint copy hides it from Odoo's database selector (Odoo lists only
// databases with connections allowed) and blocks anyone connecting to it; the
// rollback flips it back to true on the restored database so Odoo can use it.
func remoteSetAllowConns(ctx context.Context, rsc remoteShellContext, db string, allow bool) error {
	v := "false"
	if allow {
		v = "true"
	}
	return remotePsqlExec(ctx, rsc, "postgres",
		fmt.Sprintf(`ALTER DATABASE "%s" WITH ALLOW_CONNECTIONS %s`, db, v))
}

// remoteDropDB drops database db (a no-op when it doesn't exist).
func remoteDropDB(ctx context.Context, rsc remoteShellContext, db string) error {
	return remotePsqlExec(ctx, rsc, "postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, db))
}

// remoteCreateDB creates an empty database db owned by the target role.
func remoteCreateDB(ctx context.Context, rsc remoteShellContext, db string) error {
	return remotePsqlExec(ctx, rsc, "postgres",
		fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, db, pgUserFor(rsc)))
}

// remoteDumpToFile runs pg_dump -Fc for db inside the remote Postgres
// container, redirecting the archive into <remotePath>/<relPath> on the
// server (the dir is created on demand). The dump never leaves the server.
func remoteDumpToFile(ctx context.Context, rsc remoteShellContext, db, relPath string, stream func(string)) error {
	dir := relPath[:strings.LastIndex(relPath, "/")]
	inner := rsc.target.composeCmd + " exec -T " + shellQuote(rsc.target.dbContainer) +
		" pg_dump -Fc -U " + shellQuote(pgUserFor(rsc)) + " " + shellQuote(db)
	full := "cd " + shellQuote(rsc.remotePath) +
		" && mkdir -p " + shellQuote(dir) +
		" && " + inner + " > " + shellQuote(relPath)
	return ckptRunSSHStream(ctx, rsc.sshHost, full, nil, stream)
}

// remoteRestoreDump pipes the server-side dump at relPath into pg_restore in
// the remote Postgres container, loading it into the (freshly created) db.
func remoteRestoreDump(ctx context.Context, rsc remoteShellContext, db, relPath string, stream func(string)) error {
	user := pgUserFor(rsc)
	inner := rsc.target.composeCmd + " exec -T " + shellQuote(rsc.target.dbContainer) +
		" pg_restore --no-owner --role=" + shellQuote(user) +
		" -U " + shellQuote(user) + " -d " + shellQuote(db)
	full := "cd " + shellQuote(rsc.remotePath) + " && " + inner + " < " + shellQuote(relPath)
	return ckptRunSSHStream(ctx, rsc.sshHost, full, nil, stream)
}

// remoteFileSize returns the byte size of the server-side file at relPath
// (0 when it's missing).
func remoteFileSize(ctx context.Context, rsc remoteShellContext, relPath string) (int64, error) {
	full := "cd " + shellQuote(rsc.remotePath) + " && wc -c < " + shellQuote(relPath) + " 2>/dev/null || echo 0"
	out, err := ckptRunSSH(ctx, rsc.sshHost, full, nil)
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n, nil
}

// remoteFileExists reports whether the server-side file at relPath is present.
func remoteFileExists(ctx context.Context, rsc remoteShellContext, relPath string) bool {
	full := "cd " + shellQuote(rsc.remotePath) + " && { test -f " + shellQuote(relPath) + " && echo yes; } || echo no"
	out, err := ckptRunSSH(ctx, rsc.sshHost, full, nil)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "yes"
}

// remoteRemoveFile deletes the server-side file at relPath.
func remoteRemoveFile(ctx context.Context, rsc remoteShellContext, relPath string) error {
	full := "cd " + shellQuote(rsc.remotePath) + " && rm -f " + shellQuote(relPath)
	_, err := ckptRunSSH(ctx, rsc.sshHost, full, nil)
	return err
}

// ckptDBName builds the checkpoint DATABASE name for db at timestamp ts,
// truncating the db portion so the result stays within Postgres' 63-byte
// identifier limit.
func ckptDBName(db, ts string) string {
	const suffix = "__ckpt_"
	const maxIdent = 63
	room := maxIdent - len(suffix) - len(ts)
	if room < 1 {
		room = 1
	}
	if len(db) > room {
		db = db[:room]
	}
	return db + suffix + ts
}

// createCheckpoint takes a checkpoint of the remote target's database by the
// given method ("db" template copy or "dump" pg_dump), returning the stored
// entry and its metric summary. The caller runs it with the containers
// already stopped for the "db" method (a template copy needs no connections).
func createCheckpoint(ctx context.Context, rsc remoteShellContext, method string, shas []string, stream func(string), log logFn) (config.CheckpointEntry, CheckpointInfo, error) {
	db := rsc.prof.DBName
	now := time.Now()
	ts := now.Format("20060102_150405")

	if method == "dump" {
		name := sanitizeDBName(db) + "_" + ts + ".dump"
		relPath := checkpointDir + "/" + name
		ckptLog(log, "INFO", "checkpoint", "creating dump checkpoint", db, [2]string{"file", name})
		start := time.Now()
		if err := remoteDumpToFile(ctx, rsc, db, relPath, stream); err != nil {
			return config.CheckpointEntry{}, CheckpointInfo{}, fmt.Errorf("checkpoint dump: %w", err)
		}
		size, _ := remoteFileSize(ctx, rsc, relPath)
		took := int(time.Since(start).Seconds())
		entry := config.CheckpointEntry{Name: name, Method: "dump", DB: db, CreatedAt: now, DeploySHAs: shas, DumpPath: relPath}
		info := CheckpointInfo{Name: name, Method: "dump", Size: humanBytes(size), TookSec: took}
		ckptLog(log, "INFO", "checkpoint", "created", db,
			[2]string{"method", "dump"}, [2]string{"size", humanBytes(size)}, [2]string{"took", strconv.Itoa(took) + "s"})
		return entry, info, nil
	}

	// "db" — server-side template copy.
	name := ckptDBName(db, ts)
	size, _ := remoteDBSize(ctx, rsc, db)
	ckptLog(log, "INFO", "checkpoint", "creating database checkpoint", db, [2]string{"copy", name})
	start := time.Now()
	if err := remoteTerminateConns(ctx, rsc, db); err != nil {
		return config.CheckpointEntry{}, CheckpointInfo{}, fmt.Errorf("checkpoint terminate connections: %w", err)
	}
	fileCopy := remotePGVersionNum(ctx, rsc) >= 150000
	if err := remoteCreateFromTemplate(ctx, rsc, db, name, fileCopy); err != nil {
		return config.CheckpointEntry{}, CheckpointInfo{}, fmt.Errorf("checkpoint template copy: %w", err)
	}
	// Hide the copy from Odoo's DB selector (and block accidental logins) by
	// disabling connections. Best-effort — the checkpoint is a valid restore
	// point either way; a failure just leaves it visible.
	if err := remoteSetAllowConns(ctx, rsc, name, false); err != nil {
		ckptLog(log, "WARNING", "checkpoint", "checkpoint left visible to Odoo (could not disable connections)", db,
			[2]string{"name", name}, [2]string{"err", err.Error()})
	}
	took := int(time.Since(start).Seconds())
	entry := config.CheckpointEntry{Name: name, Method: "db", DB: db, CreatedAt: now, DeploySHAs: shas}
	info := CheckpointInfo{Name: name, Method: "db", Size: humanBytes(size), TookSec: took}
	ckptLog(log, "INFO", "checkpoint", "created", db,
		[2]string{"method", "db"}, [2]string{"size", humanBytes(size)}, [2]string{"took", strconv.Itoa(took) + "s"})
	return entry, info, nil
}

// restoreCheckpoint restores the target's database from entry. It reports
// whether the checkpoint object was consumed (the "db" method renames the
// copy over the live DB; the "dump" method leaves the file intact). The
// caller runs it with the containers stopped.
func restoreCheckpoint(ctx context.Context, rsc remoteShellContext, entry config.CheckpointEntry, stream func(string), log logFn) (consumed bool, err error) {
	db := entry.DB
	ckptLog(log, "INFO", "rollback", "restoring checkpoint", db,
		[2]string{"method", entry.Method}, [2]string{"name", entry.Name})
	start := time.Now()

	if entry.Method == "dump" {
		if err := remoteTerminateConns(ctx, rsc, db); err != nil {
			return false, err
		}
		if err := remoteDropDB(ctx, rsc, db); err != nil {
			return false, err
		}
		if err := remoteCreateDB(ctx, rsc, db); err != nil {
			return false, err
		}
		if err := remoteRestoreDump(ctx, rsc, db, entry.DumpPath, stream); err != nil {
			return false, err
		}
		ckptLog(log, "INFO", "rollback", "database restored", db,
			[2]string{"took", strconv.Itoa(int(time.Since(start).Seconds())) + "s"})
		return false, nil
	}

	// "db" — drop the (broken) live DB and rename the checkpoint over it.
	if err := remoteTerminateConns(ctx, rsc, db); err != nil {
		return false, err
	}
	if err := remoteTerminateConns(ctx, rsc, entry.Name); err != nil {
		return false, err
	}
	if err := remoteDropDB(ctx, rsc, db); err != nil {
		return false, err
	}
	if err := remoteRenameDB(ctx, rsc, entry.Name, db); err != nil {
		return false, err
	}
	// The checkpoint had connections disabled (to hide it from Odoo); the
	// restored database inherits that, so re-enable them or Odoo can't connect.
	if err := remoteSetAllowConns(ctx, rsc, db, true); err != nil {
		ckptLog(log, "ERROR", "rollback", "database restored but connections still disabled — run: ALTER DATABASE \""+db+"\" WITH ALLOW_CONNECTIONS true", db,
			[2]string{"err", err.Error()})
	}
	ckptLog(log, "INFO", "rollback", "database restored", db,
		[2]string{"took", strconv.Itoa(int(time.Since(start).Seconds())) + "s"})
	return true, nil
}

// destroyCheckpointObject removes a checkpoint's remote artifact (its copy DB
// or its dump file), used by retention pruning and `checkpoint rm`.
func destroyCheckpointObject(ctx context.Context, rsc remoteShellContext, e config.CheckpointEntry) error {
	if e.Method == "dump" {
		return remoteRemoveFile(ctx, rsc, e.DumpPath)
	}
	return remoteDropDB(ctx, rsc, e.Name)
}

// checkpointPreflight aborts before any container stop when the DB is too big
// to checkpoint safely: the "db" method needs ~1.2× the DB size free, the
// "dump" method ~0.5×. A best-effort probe — if the size or free space can't
// be read, it warns and lets the deploy proceed rather than blocking on a
// measurement gap.
func checkpointPreflight(ctx context.Context, rsc remoteShellContext, method string, log logFn) error {
	db := rsc.prof.DBName
	size, err := remoteDBSize(ctx, rsc, db)
	if err != nil {
		ckptLog(log, "WARNING", "checkpoint", "disk preflight skipped", db, [2]string{"reason", err.Error()})
		return nil
	}
	dataDir, err := remoteDataDir(ctx, rsc)
	if err != nil {
		ckptLog(log, "WARNING", "checkpoint", "disk preflight skipped", db, [2]string{"reason", err.Error()})
		return nil
	}
	free, err := remoteDiskFreeBytes(ctx, rsc, dataDir)
	if err != nil {
		ckptLog(log, "WARNING", "checkpoint", "disk preflight skipped", db, [2]string{"reason", err.Error()})
		return nil
	}
	factor := 1.2
	if method == "dump" {
		factor = 0.5
	}
	need := int64(float64(size) * factor)
	if free < need {
		return fmt.Errorf("%w: not enough disk for a %s checkpoint — db is %s, need ~%s free, %s available (retry with --no-checkpoint, or free space with `checkpoint rm`)",
			ErrUsage, method, humanBytes(size), humanBytes(need), humanBytes(free))
	}
	ckptLog(log, "INFO", "checkpoint", "disk preflight ok", db,
		[2]string{"db_size", humanBytes(size)}, [2]string{"free", humanBytes(free)})
	return nil
}

// pruneCheckpoints enforces the retention keep count for a target: it keeps
// the newest keep checkpoints and destroys the rest (remote object + metadata
// entry). Best-effort — a prune failure warns and moves on.
func pruneCheckpoints(ctx context.Context, rsc remoteShellContext, projectKey, targetKey string, keep int, log logFn) {
	if keep < 1 {
		keep = 1
	}
	entries := config.LoadCheckpoints(projectKey, targetKey) // newest first
	if len(entries) <= keep {
		return
	}
	for _, e := range entries[keep:] {
		if err := destroyCheckpointObject(ctx, rsc, e); err != nil {
			ckptLog(log, "WARNING", "checkpoint", "prune failed", rsc.prof.DBName,
				[2]string{"name", e.Name}, [2]string{"err", err.Error()})
			continue
		}
		_ = config.RemoveCheckpoint(projectKey, targetKey, e.Name)
		ckptLog(log, "INFO", "checkpoint", "pruned old checkpoint", rsc.prof.DBName, [2]string{"name", e.Name})
	}
}
