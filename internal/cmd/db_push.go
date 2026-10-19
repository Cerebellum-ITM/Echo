package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// DBPushOpts configures RunDBPush. Confirm asks before replacing a prod
// database on a TTY; nil uses the red huh confirm.
type DBPushOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	StreamOut func(string)
	Log       logFn
	Confirm   func(target, db string) error
}

func (o DBPushOpts) log(level, sub, msg, db string, fields ...[2]string) {
	ckptLog(o.Log, level, sub, msg, db, fields...)
}

// DBPushStep is one step of the run as reported in the summary.
type DBPushStep struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // ok | skipped | failed | planned
	Seconds int    `json:"seconds"`
}

// DBPushModule is one module of the restored database.
type DBPushModule struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Version string `json:"version"`
}

// DBPushResult is the db-push summary, printed as JSON under --json even
// when the run failed, so a caller learns where it stopped.
type DBPushResult struct {
	Target            string         `json:"target"`
	Host              string         `json:"host"`
	Stage             string         `json:"stage"`
	DB                string         `json:"db"`
	Dump              string         `json:"dump"`
	DumpBytes         int64          `json:"dump_bytes"`
	Filestore         string         `json:"filestore"`
	FilestoreBytes    int64          `json:"filestore_bytes"`
	Checkpoint        string         `json:"checkpoint"`
	PreviousFilestore string         `json:"previous_filestore"`
	Neutralized       bool           `json:"neutralized"`
	DryRun            bool           `json:"dry_run"`
	Steps             []DBPushStep   `json:"steps"`
	Modules           []DBPushModule `json:"modules"`
	OK                bool           `json:"ok"`
	JSON              bool           `json:"-"`
}

type dbPushArgs struct {
	dump         string
	to           string
	remote       bool
	filestore    bool
	tar          string
	as           string
	neutralize   bool
	noCheckpoint bool
	dryRun       bool
	force        bool
	jsonOut      bool
}

func dbPushUsage(format string, a ...any) error {
	return fmt.Errorf("%w: db-push: "+format, append([]any{ErrUsage}, a...)...)
}

func parseDBPushArgs(args []string) (dbPushArgs, error) {
	var p dbPushArgs
	var positional []string
	value := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) || strings.HasPrefix(args[*i+1], "-") {
			return "", dbPushUsage("%s needs a value", flag)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch {
		case a == "--to":
			p.to, err = value(&i, a)
		case strings.HasPrefix(a, "--to="):
			p.to = strings.TrimPrefix(a, "--to=")
		case a == "--remote":
			p.remote = true
		case a == "--filestore":
			p.filestore = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && strings.HasSuffix(args[i+1], ".tar") {
				i++
				p.tar = args[i]
			}
		case strings.HasPrefix(a, "--filestore="):
			p.filestore = true
			p.tar = strings.TrimPrefix(a, "--filestore=")
		case a == "--as":
			p.as, err = value(&i, a)
		case strings.HasPrefix(a, "--as="):
			p.as = strings.TrimPrefix(a, "--as=")
		case a == "--neutralize":
			p.neutralize = true
		case a == "--no-checkpoint":
			p.noCheckpoint = true
		case a == "--dry-run":
			p.dryRun = true
		case a == "--force":
			p.force = true
		case a == "--json":
			p.jsonOut = true
		case strings.HasPrefix(a, "-"):
			err = dbPushUsage("unknown flag %s", a)
		default:
			positional = append(positional, a)
		}
		if err != nil {
			return p, err
		}
	}
	switch {
	case len(positional) == 0:
		return p, dbPushUsage("pass the dump to load (a db-pull .dump file)")
	case len(positional) > 1:
		return p, dbPushUsage("takes one dump, got %s", strings.Join(positional, " "))
	case p.to != "" && p.remote:
		return p, dbPushUsage("pass --to <target> or --remote, not both")
	case p.to == "" && !p.remote:
		return p, dbPushUsage("pass --to <target> or --remote (db-push never guesses its target)")
	case p.as != "" && sanitizeDBName(p.as) != p.as:
		return p, dbPushUsage("--as %q is not a valid database name (try %q)", p.as, sanitizeDBName(p.as))
	}
	p.dump = positional[0]
	if p.filestore && p.tar == "" {
		p.tar = strings.TrimSuffix(p.dump, ".dump") + ".filestore.tar"
	}
	return p, nil
}

// checkDBPushFiles validates the local inputs and returns their sizes.
func checkDBPushFiles(p dbPushArgs) (dumpBytes, tarBytes int64, err error) {
	f, err := os.Open(p.dump)
	if err != nil {
		return 0, 0, dbPushUsage("dump %s: %v", p.dump, err)
	}
	defer f.Close()
	magic := make([]byte, 5)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "PGDMP" {
		return 0, 0, dbPushUsage("%s is not a pg_dump custom-format file", p.dump)
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	dumpBytes = fi.Size()
	if p.filestore {
		ti, err := os.Stat(p.tar)
		if err != nil {
			return 0, 0, dbPushUsage("filestore archive %s not found", p.tar)
		}
		tarBytes = ti.Size()
	}
	return dumpBytes, tarBytes, nil
}

// Test seams: the upload transport and the run timestamp.
var (
	dbPushUpload = runSSHStreamReader
	dbPushNow    = time.Now
)

const dbPushStepCount = 12

// RunDBPush replaces a remote target's database (and optionally its
// filestore) with a local db-pull dump: preflight, operation lock, upload,
// stop Odoo, checkpoint, restore, filestore swap, neutralize, registry
// check, start Odoo, report. The upload directory and the lock are removed
// whatever happens. Writes on the server only outside --dry-run.
func RunDBPush(ctx context.Context, opts DBPushOpts) (DBPushResult, error) {
	p, err := parseDBPushArgs(opts.Args)
	res := DBPushResult{JSON: p.jsonOut, Steps: []DBPushStep{}, Modules: []DBPushModule{}}
	if err != nil {
		return res, err
	}
	dumpBytes, tarBytes, err := checkDBPushFiles(p)
	if err != nil {
		return res, err
	}
	res.Dump, res.DumpBytes = filepath.Base(p.dump), dumpBytes
	if p.filestore {
		res.Filestore, res.FilestoreBytes = filepath.Base(p.tar), tarBytes
	}

	from := p.to
	if p.remote {
		if opts.Cfg.ConnectSSHHost == "" || opts.Cfg.ConnectRemotePath == "" {
			return res, dbPushUsage("--remote needs this directory's link binding (see link)")
		}
	} else if !hasConnectTarget(opts.Cfg, p.to) {
		return res, fmt.Errorf("unknown connect target: %s", p.to)
	}
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, opts.Log)
	if err != nil {
		return res, err
	}
	return runDBPushFlow(ctx, opts, p, rsc, res)
}

func hasConnectTarget(cfg *config.Config, name string) bool {
	for _, t := range cfg.ConnectTargets {
		if t.Name == name {
			return true
		}
	}
	return false
}

type dbPushRun struct {
	opts   DBPushOpts
	p      dbPushArgs
	rsc    remoteShellContext
	res    *DBPushResult
	db     string
	ts     string
	upload string // upload dir, relative to remote_path
	step   int
}

func (r *dbPushRun) log(level, sub, msg string, fields ...[2]string) {
	r.opts.log(level, sub, msg, r.db, fields...)
}

func (r *dbPushRun) stream(line string) {
	if r.opts.StreamOut != nil {
		r.opts.StreamOut(line)
	}
}

// do runs one numbered step and records its outcome.
func (r *dbPushRun) do(name string, run func() error) error {
	r.step++
	r.log("INFO", "step", fmt.Sprintf("step %d/%d %s", r.step, dbPushStepCount, name))
	start := time.Now()
	err := run()
	status := "ok"
	if err != nil {
		status = "failed"
	}
	r.res.Steps = append(r.res.Steps, DBPushStep{Name: name, Status: status, Seconds: int(time.Since(start).Seconds())})
	return err
}

func (r *dbPushRun) skip(name, why string) {
	r.step++
	r.log("INFO", "step", fmt.Sprintf("step %d/%d %s skipped", r.step, dbPushStepCount, name), [2]string{"reason", why})
	r.res.Steps = append(r.res.Steps, DBPushStep{Name: name, Status: "skipped"})
}

func runDBPushFlow(ctx context.Context, opts DBPushOpts, p dbPushArgs, rsc remoteShellContext, res DBPushResult) (DBPushResult, error) {
	res.DryRun, res.JSON = p.dryRun, p.jsonOut
	r := &dbPushRun{opts: opts, p: p, rsc: rsc, res: &res}
	r.db = p.as
	if r.db == "" {
		r.db = rsc.prof.DBName
	}
	r.ts = dbPushNow().Format("20060102-150405")
	r.upload = "backups/db-push-" + r.ts
	res.Target, res.Host, res.Stage, res.DB = targetLabel(rsc), rsc.sshHost, rsc.target.rawStage, r.db
	if res.Stage == "" {
		res.Stage = rsc.target.stage
	}

	if rsc.reverb != nil || rsc.prof.Reverb != nil {
		return res, errors.New("db-push does not support Reverb environments")
	}
	if r.db == "" {
		return res, dbPushUsage("the target profile has no db_name; pass --as <db>")
	}
	isProd := strings.EqualFold(rsc.target.stage, "prod")
	if !p.dryRun {
		if isProd && !p.force {
			return res, dbPushUsage("refusing to replace the database of a prod target; pass --force")
		}
		if isProd && stdinIsTTY() {
			confirm := opts.Confirm
			if confirm == nil {
				confirm = func(target, db string) error { return confirmDBPushProd(opts.Palette, target, db) }
			}
			if err := confirm(res.Target, r.db); err != nil {
				return res, err
			}
		}
	}

	pol := resolveCheckpointPolicy(rsc.prof, opts.Cfg)
	var plan dbPushPlan
	err := r.do("preflight", func() error {
		var perr error
		plan, perr = r.preflight(ctx, pol.method)
		return perr
	})
	if p.dryRun {
		r.printPlan(plan, pol.method, isProd, err)
		res.OK = err == nil
		return res, err
	}
	if err != nil {
		return res, err
	}

	if err := r.do("lock", func() error {
		_, lerr := acquireOpLock(ctx, rsc, "db-push", p.force, opts.Log)
		return lerr
	}); err != nil {
		return res, err
	}

	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	defer r.cleanup()

	if err := r.do("upload", func() error { return r.uploadAll(ctx) }); err != nil {
		return res, err
	}
	if err := r.do("stop", func() error {
		return ckptRunSSHStream(ctx, rsc.sshHost, remoteStopApp(rsc), nil, r.stream)
	}); err != nil {
		return res, err
	}

	if p.noCheckpoint || !plan.dbExists {
		why := "--no-checkpoint"
		if !plan.dbExists {
			why = "the database does not exist yet"
		}
		r.skip("checkpoint", why)
	} else if err := r.do("checkpoint", func() error { return r.checkpoint(ctx, pol) }); err != nil {
		r.startOdoo(context.Background())
		return res, err
	}

	leftStopped := func(err error) (DBPushResult, error) {
		r.log("ERROR", "", "the target's Odoo is left stopped")
		if res.Checkpoint != "" {
			r.log("INFO", "", "restore the previous DB: echo_cli deploy --rollback --from "+res.Target+" --force")
		}
		return res, err
	}

	if err := r.do("restore", func() error { return r.restore(ctx) }); err != nil {
		return leftStopped(err)
	}
	if p.filestore {
		if err := r.do("filestore", func() error { return r.swapFilestore(ctx) }); err != nil {
			return leftStopped(err)
		}
	} else {
		r.skip("filestore", "no --filestore")
	}
	if p.neutralize {
		if err := r.do("neutralize", func() error {
			conn := rsc.conn
			conn.DB = r.db
			return ckptRunSSHStream(ctx, rsc.sshHost, oneOffOdooCmd(rsc, false, "", odoo.Neutralize(conn)), nil, r.stream)
		}); err != nil {
			return leftStopped(err)
		}
		res.Neutralized = true
	} else {
		r.skip("neutralize", "no --neutralize")
	}
	if err := r.do("registry", func() error { return r.registryCheck(ctx) }); err != nil {
		return leftStopped(err)
	}
	if err := r.do("start", func() error { return r.startOdoo(ctx) }); err != nil {
		return leftStopped(err)
	}
	if err := r.do("report", func() error { return r.report(ctx) }); err != nil {
		r.log("WARNING", "report", "module versions not read", [2]string{"err", err.Error()})
	}
	res.OK = true
	r.log("INFO", "", "database replaced", [2]string{"target", res.Target}, [2]string{"dump", res.Dump})
	return res, nil
}

type dbPushPlan struct {
	dbExists bool
	dbBytes  int64
	free     int64
	need     int64
	holder   *OpLock
	held     bool
}

func (r *dbPushRun) preflight(ctx context.Context, method string) (dbPushPlan, error) {
	var plan dbPushPlan
	rsc := r.rsc
	if _, err := remotePsqlScalar(ctx, rsc, "postgres", "SELECT 1"); err != nil {
		return plan, fmt.Errorf("the database service of %s is not reachable: %w", r.res.Target, err)
	}
	dbs, err := remoteListDatabases(ctx, rsc)
	if err != nil {
		return plan, err
	}
	for _, d := range dbs {
		if d == r.db {
			plan.dbExists = true
		}
	}
	if !plan.dbExists {
		r.log("INFO", "preflight", "the database does not exist yet; no checkpoint will be taken")
	} else if plan.dbBytes, err = remoteDBSize(ctx, rsc, r.db); err != nil {
		return plan, err
	}
	plan.need = r.res.DumpBytes + 2*r.res.FilestoreBytes
	if plan.dbExists && !r.p.noCheckpoint {
		plan.need += checkpointNeed(plan.dbBytes, method)
	}
	if plan.free, err = remoteHostDiskFreeBytes(ctx, rsc, rsc.remotePath); err != nil {
		return plan, err
	}
	if plan.holder, plan.held, err = readOpLock(ctx, rsc); err != nil {
		return plan, err
	}
	if plan.free < plan.need {
		return plan, fmt.Errorf("not enough disk on %s: need ~%s free, %s available", r.res.Target,
			humanBytes(plan.need), humanBytes(plan.free))
	}
	r.log("INFO", "preflight", "preflight ok", [2]string{"free", humanBytes(plan.free)}, [2]string{"need", humanBytes(plan.need)})
	return plan, nil
}

func (r *dbPushRun) printPlan(plan dbPushPlan, method string, isProd bool, preflightErr error) {
	gate := "allowed"
	switch {
	case isProd && !r.p.force:
		gate = "refused: prod target without --force"
	case isProd:
		gate = "prod: --force, confirm on a TTY"
	}
	dbState := "new"
	if plan.dbExists {
		dbState = "existing, " + humanBytes(plan.dbBytes)
	}
	ckpt := "none"
	if plan.dbExists && !r.p.noCheckpoint {
		if method == "dump" {
			ckpt = "dump " + sanitizeDBName(r.db) + "_<ts>.dump"
		} else {
			ckpt = "db " + ckptDBName(r.db, "<ts>")
		}
	}
	lock := "free"
	if plan.held {
		lock = errOpLockHeld{plan.holder}.Error()
	}
	fs := "unchanged"
	if r.p.filestore {
		fs = r.res.Filestore + " (" + humanBytes(r.res.FilestoreBytes) + "), previous kept as " + r.db + ".pre-push-" + r.ts
	}
	rows := [][2]string{
		{"target", r.res.Target + " (" + r.res.Host + ":" + r.rsc.remotePath + ")"},
		{"stage", r.res.Stage + " · gate " + gate},
		{"database", r.db + " (" + dbState + ")"},
		{"dump", r.res.Dump + " (" + humanBytes(r.res.DumpBytes) + ")"},
		{"filestore", fs},
		{"upload dir", r.upload},
		{"checkpoint", ckpt},
		{"lock", lock},
		{"disk", humanBytes(plan.free) + " free, ~" + humanBytes(plan.need) + " needed"},
		{"neutralize", strconv.FormatBool(r.p.neutralize)},
	}
	for _, row := range rows {
		r.stream(fmt.Sprintf("  %-11s %s", row[0], row[1]))
	}
	steps := []string{"lock", "upload", "stop Odoo", "checkpoint", "restore", "filestore", "neutralize",
		"registry check", "start Odoo", "report", "cleanup"}
	for i, s := range steps {
		r.stream(fmt.Sprintf("  %2d. %s", i+2, s))
	}
	for _, s := range []string{"lock", "upload", "stop", "checkpoint", "restore", "filestore", "neutralize", "registry", "start", "report", "cleanup"} {
		r.res.Steps = append(r.res.Steps, DBPushStep{Name: s, Status: "planned"})
	}
	if preflightErr == nil {
		r.log("INFO", "", "dry run: nothing was changed on the server")
	}
}

func (r *dbPushRun) uploadAll(ctx context.Context) error {
	files := [][2]string{{r.p.dump, r.res.Dump}}
	sizes := []int64{r.res.DumpBytes}
	if r.p.filestore {
		files = append(files, [2]string{r.p.tar, r.res.Filestore})
		sizes = append(sizes, r.res.FilestoreBytes)
	}
	for i, f := range files {
		if err := r.uploadFile(ctx, f[0], f[1], sizes[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *dbPushRun) uploadFile(ctx context.Context, local, name string, size int64) error {
	in, err := os.Open(local)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := shellQuote(r.upload)
	part := shellQuote(r.upload + "/" + name + ".part")
	script := "cd " + shellQuote(r.rsc.remotePath) + " && mkdir -p " + dir +
		" && cat > " + part + " && mv " + part + " " + shellQuote(r.upload+"/"+name)
	progress := newProgressReporter(func(msg string, fields ...[2]string) {
		r.log("INFO", "upload", msg, fields...)
	}, "uploading "+name, size)
	progress.Start()
	err = dbPushUpload(ctx, r.rsc.sshHost, script, io.TeeReader(in, countingWriter{io.Discard, progress}), r.stream)
	progress.Stop()
	if err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	got, err := remoteFileSize(ctx, r.rsc, r.upload+"/"+name)
	if err != nil {
		return err
	}
	if got != size {
		return fmt.Errorf("upload %s: the server has %d bytes, expected %d", name, got, size)
	}
	r.log("INFO", "upload", "uploaded", [2]string{"file", name}, [2]string{"size", humanBytes(size)})
	return nil
}

func (r *dbPushRun) checkpoint(ctx context.Context, pol checkpointPolicy) error {
	entry, info, err := createCheckpoint(ctx, r.rsc, r.db, pol.method, nil, r.stream, r.opts.Log)
	if err != nil {
		return err
	}
	projectKey := config.ProjectKey(r.opts.Root)
	targetKey := config.DeployTargetKey(r.rsc.sshHost, r.rsc.remotePath)
	if err := config.AddCheckpoint(projectKey, targetKey, entry); err == nil {
		pruneCheckpoints(ctx, r.rsc, projectKey, targetKey, pol.keep, r.opts.Log)
	}
	r.res.Checkpoint = info.Name
	return nil
}

func (r *dbPushRun) restore(ctx context.Context) error {
	rsc := r.rsc
	if err := remoteTerminateConns(ctx, rsc, r.db); err != nil {
		return err
	}
	if err := remoteDropDB(ctx, rsc, r.db); err != nil {
		return err
	}
	if err := remoteCreateDB(ctx, rsc, r.db); err != nil {
		return err
	}
	return remoteRestoreDump(ctx, rsc, r.db, r.upload+"/"+r.res.Dump, r.stream)
}

// dbPushFilestoreScript runs as root in a one-off Odoo container: it keeps
// the current filestore of the database under a .pre-push name, extracts
// the archive from stdin into a fresh directory (the archive's top folder is
// the source database's name) and gives it the owner of the filestore root.
const dbPushFilestoreScript = `set -e
fs="$1"; db="$2"; ts="$3"
if [ -e "$fs/$db" ]; then mv "$fs/$db" "$fs/$db.pre-push-$ts"; echo "previous:$db.pre-push-$ts"; fi
mkdir -p "$fs/$db"
tar -xf - -C "$fs/$db" --strip-components=1
chown -R "$(stat -c %u:%g "$fs")" "$fs/$db"`

func (r *dbPushRun) swapFilestore(ctx context.Context) error {
	rsc := r.rsc
	full := oneOffOdooCmd(rsc, true, r.upload+"/"+r.res.Filestore,
		odoo.Cmd{"-c", dbPushFilestoreScript, "sh", rsc.prof.FilestorePath, r.db, r.ts})
	return ckptRunSSHStream(ctx, rsc.sshHost, full, nil, func(line string) {
		if prev, ok := strings.CutPrefix(strings.TrimSpace(line), "previous:"); ok {
			r.res.PreviousFilestore = prev
			r.log("INFO", "filestore", "previous filestore kept", [2]string{"dir", rsc.prof.FilestorePath + "/" + prev})
			return
		}
		r.stream(line)
	})
}

func (r *dbPushRun) registryCheck(ctx context.Context) error {
	conn := r.rsc.conn
	conn.DB = r.db
	argv := append(odoo.Cmd{"odoo", "--stop-after-init", "--no-http"}, conn.Flags()...)
	scanner := &runFailureScanner{inner: r.stream}
	if err := ckptRunSSHStream(ctx, r.rsc.sshHost, oneOffOdooCmd(r.rsc, false, "", argv), nil, scanner.scan); err != nil {
		return fmt.Errorf("registry check: %w", err)
	}
	if scanner.hits > 0 {
		return fmt.Errorf("registry check: Odoo reported %d failure line(s) loading %s", scanner.hits, r.db)
	}
	return nil
}

func (r *dbPushRun) startOdoo(ctx context.Context) error {
	return ckptRunSSHStream(ctx, r.rsc.sshHost,
		remoteComposeCmd(r.rsc.remotePath, r.rsc.target.composeCmd, "up", "-d", r.rsc.target.odooContainer), nil, r.stream)
}

func (r *dbPushRun) report(ctx context.Context) error {
	q := "SELECT name, state, COALESCE(latest_version, ''), COALESCE(author, '') FROM ir_module_module ORDER BY name"
	out, err := remotePsqlScalar(ctx, r.rsc, r.db, q)
	if err != nil {
		return err
	}
	var installed, custom []DBPushModule
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 || parts[0] == "" {
			continue
		}
		m := DBPushModule{Name: parts[0], State: parts[1], Version: parts[2]}
		r.res.Modules = append(r.res.Modules, m)
		if m.State != "installed" {
			continue
		}
		installed = append(installed, m)
		if m.Name == "base" {
			r.log("INFO", "report", "base", [2]string{"version", m.Version})
		} else if !strings.Contains(parts[3], "Odoo S.A.") {
			custom = append(custom, m)
		}
	}
	sort.Slice(custom, func(i, j int) bool { return custom[i].Name < custom[j].Name })
	for i, m := range custom {
		if i == 5 {
			break
		}
		r.log("INFO", "report", m.Name, [2]string{"version", m.Version})
	}
	r.log("INFO", "report", "installed modules", [2]string{"count", strconv.Itoa(len(installed))})
	return nil
}

// cleanup removes the upload directory and releases the operation lock.
// It runs on a fresh context so Ctrl+C or SIGTERM still cleans up.
func (r *dbPushRun) cleanup() {
	r.step++
	r.log("INFO", "step", fmt.Sprintf("step %d/%d cleanup", dbPushStepCount, dbPushStepCount))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	status := "ok"
	if _, err := ckptRunSSH(ctx, r.rsc.sshHost, "cd "+shellQuote(r.rsc.remotePath)+" && rm -rf "+shellQuote(r.upload), nil); err != nil {
		status = "failed"
		r.log("WARNING", "cleanup", "upload directory not removed", [2]string{"dir", r.upload}, [2]string{"err", err.Error()})
	}
	if err := releaseOpLock(ctx, r.rsc); err != nil {
		status = "failed"
		r.log("WARNING", "cleanup", "operation lock not released", [2]string{"lock", opLockDir}, [2]string{"err", err.Error()})
	}
	r.res.Steps = append(r.res.Steps, DBPushStep{Name: "cleanup", Status: status})
}

// oneOffOdooCmd runs argv in a throwaway container of the Odoo service
// (`compose run --rm --no-deps -T`): it mounts the service's volumes and
// reaches the database while the Odoo service itself is stopped. asRoot
// replaces the entrypoint with sh and runs as uid 0; stdinFile, relative to
// remote_path, is piped into the container when set.
func oneOffOdooCmd(rsc remoteShellContext, asRoot bool, stdinFile string, argv odoo.Cmd) string {
	args := []string{"run", "--rm", "--no-deps", "-T"}
	if asRoot {
		args = append(args, "--user", "0", "--entrypoint", "sh")
	}
	args = append(append(args, rsc.target.odooContainer), argv...)
	var b strings.Builder
	b.WriteString("cd " + shellQuote(rsc.remotePath) + " && ")
	if stdinFile != "" {
		b.WriteString("cat " + shellQuote(stdinFile) + " | ")
	}
	b.WriteString(rsc.target.composeCmd)
	for _, a := range args {
		b.WriteString(" " + shellQuote(a))
	}
	return b.String()
}

func confirmDBPushProd(palette theme.Palette, target, db string) error {
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Replace the prod database " + red + " on " + target).
			Description("Its current data is replaced by the dump; a checkpoint is the only way back.").
			Affirmative("Replace").
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
