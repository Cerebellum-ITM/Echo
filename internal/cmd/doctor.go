package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// DoctorOpts configures a `doctor` run.
type DoctorOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log emits one Odoo-style line per check under `echo.doctor[.<id>]`.
	Log func(level, sub, msg, db string, fields ...[2]string)
}

func (o DoctorOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

const (
	doctorOK      = "ok"
	doctorWarn    = "warn"
	doctorFailed  = "failed"
	doctorSkipped = "skipped"
)

// DoctorCheck is the outcome of one readiness check. Reason says why a check
// was skipped; Fields never carry `.env` values or the Reverb token.
type DoctorCheck struct {
	ID      string
	Status  string
	Reason  string
	Message string
	Fields  [][2]string
}

// MarshalJSON renders Fields as a string object, the shape `doctor --json`
// documents.
func (c DoctorCheck) MarshalJSON() ([]byte, error) {
	fields := make(map[string]string, len(c.Fields))
	for _, f := range c.Fields {
		fields[f[0]] = f[1]
	}
	return json.Marshal(struct {
		ID      string            `json:"id"`
		Status  string            `json:"status"`
		Reason  string            `json:"reason,omitempty"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	}{c.ID, c.Status, c.Reason, c.Message, fields})
}

// warn downgrades an ok check, or appends one more issue to a warned one.
func (c *DoctorCheck) warn(msg string, fields ...[2]string) {
	if c.Status == doctorOK {
		c.Status, c.Message = doctorWarn, msg
	} else {
		c.Message += "; " + msg
	}
	c.Fields = append(c.Fields, fields...)
}

// DoctorTarget identifies the checked target. Stage is the normalized stage
// the gates read; empty with DB when the server profile is unusable.
type DoctorTarget struct {
	Name          string `json:"name"`
	Host          string `json:"host"`
	Path          string `json:"path"`
	Stage         string `json:"stage"`
	StageDeclared bool   `json:"stage_declared"`
	DB            string `json:"db"`
}

type DoctorCounts struct {
	OK      int `json:"ok"`
	Warn    int `json:"warn"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type DoctorResult struct {
	Target DoctorTarget  `json:"target"`
	Checks []DoctorCheck `json:"checks"`
	Counts DoctorCounts  `json:"counts"`
}

// doctorRunSSH carries the three read-only batches; a seam so tests can
// script or rewrite them.
var doctorRunSSH = runSSH

const doctorBatchTimeout = 30 * time.Second

type doctorArgs struct {
	from    string
	remote  bool
	jsonOut bool
}

// parseDoctorArgs accepts only the target selection and --json; anything else
// is a usage error raised before any SSH.
func parseDoctorArgs(args []string) (doctorArgs, error) {
	var out doctorArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return out, fmt.Errorf("%w: --from needs a target name", ErrUsage)
			}
			out.from = args[i+1]
			i++
		case strings.HasPrefix(a, "--from="):
			out.from = strings.TrimPrefix(a, "--from=")
		case a == "--remote":
			out.remote = true
		case a == "--json":
			out.jsonOut = true
		case a == "-E", a == "--env", strings.HasPrefix(a, "-E="), strings.HasPrefix(a, "--env="):
			return out, fmt.Errorf("%w: doctor does not take %s — register the environment as a connect target and pass --from", ErrUsage, a)
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			return out, fmt.Errorf("%w: unexpected argument %q — name the target with --from", ErrUsage, a)
		}
	}
	return out, nil
}

// doctorTarget is what the batches are built from: the resolved target, its
// git-deploy topology, this repository's root commit ("" outside a
// repository) and the other connect targets on the same ssh_host.
type doctorTarget struct {
	cfg        *config.Config
	name       string
	host       string
	path       string
	git        gitDeployConfig
	rootCommit string
	peers      []config.ConnectTarget
}

func (d doctorTarget) absGitDir() string { return absGitDir(d.path, d.git.path) }

// RunDoctor reports whether a target is ready for deploy, push and
// checkpoints. It runs every check even after one fails and changes nothing
// on either side: three buffered SSH reads (host, destinations, database),
// each a script that marks its sections and always exits 0, so a non-zero
// ssh exit means transport. Failed checks are counted in the result, not
// returned as an error; the error is for usage and target resolution.
func RunDoctor(ctx context.Context, opts DoctorOpts) (DoctorResult, error) {
	p, err := parseDoctorArgs(opts.Args)
	if err != nil {
		return DoctorResult{}, err
	}
	host, remotePath, name, err := resolveRemoteTarget(opts.Cfg, opts.Palette, p.from, opts.Log)
	if err != nil {
		return DoctorResult{}, err
	}
	if name == "" {
		name = linkTargetName(opts.Cfg)
	}
	d := doctorTarget{
		cfg:   opts.Cfg,
		name:  name,
		host:  host,
		path:  remotePath,
		git:   resolveGitDeploy(opts.Cfg, name, host, remotePath),
		peers: sameHostTargets(opts.Cfg.ConnectTargets, host, remotePath),
	}
	if out, gerr := gitOutput(ctx, opts.Root, "rev-list", "--max-parents=0", "HEAD"); gerr == nil {
		d.rootCommit = firstLine(string(out))
	}
	display := statusProjectName(opts.Cfg, true, remotePath, name)
	res := DoctorResult{Target: DoctorTarget{Name: display, Host: host, Path: remotePath}}
	opts.log("INFO", "", "target", "",
		[2]string{"target", display}, [2]string{"host", host}, [2]string{"path", remotePath})

	db := ""
	emit := func(c DoctorCheck) {
		res.Checks = append(res.Checks, c)
		fields := append([][2]string{{"status", c.Status}}, c.Fields...)
		if c.Reason != "" {
			fields = append(fields, [2]string{"reason", c.Reason})
		}
		opts.log(doctorLevel(c.Status), c.ID, c.Message, db, fields...)
	}
	skipRest := func(reason string, ids ...string) {
		for _, id := range ids {
			emit(DoctorCheck{ID: id, Status: doctorSkipped, Reason: reason, Message: "not checked"})
		}
	}
	afterSSH := []string{"profile", "rsync", "git", "disk", "lock", "dest"}

	if _, lerr := lookPath("ssh"); lerr != nil {
		emit(DoctorCheck{ID: "ssh", Status: doctorFailed, Message: "ssh not found on PATH"})
		skipRest("unreachable", afterSSH...)
		return finishDoctor(opts, res), nil
	}
	hostOut, err := d.runBatch(ctx, doctorHostScript(d))
	if err != nil {
		emit(DoctorCheck{ID: "ssh", Status: doctorFailed, Message: "host unreachable",
			Fields: [][2]string{{"host", host}, {"error", lastNonEmptyLine([]byte(err.Error()))}}})
		skipRest("unreachable", afterSSH...)
		return finishDoctor(opts, res), nil
	}
	hostSec := splitDoctorSections(string(hostOut))
	emit(evalSSH(host))

	prof, target, profCheck := evalProfile(hostSec, config.ProjectKey(remotePath), display)
	usable := profCheck.Status != doctorFailed
	if usable {
		db = target.dbName
		res.Target.Stage, res.Target.StageDeclared, res.Target.DB = target.stage, target.stageDeclared, target.dbName
	}
	emit(profCheck)

	_, localRsync := lookPath("rsync")
	emit(evalRsync(localRsync, hostSec, resolveDeployPush(deployArgs{}, prof, opts.Cfg)))
	emit(evalGit(d.git, d.absGitDir(), d.rootCommit, hostSec))

	switch {
	case !usable:
		emit(DoctorCheck{ID: "disk", Status: doctorSkipped, Reason: "profile", Message: "not checked"})
	default:
		if renv, _ := reverbEnvFromProfile(opts.Cfg, prof, remotePath); renv != nil {
			emit(DoctorCheck{ID: "disk", Status: doctorSkipped, Reason: "reverb_snapshots",
				Message: "checkpoints are Reverb snapshots taken on the server"})
			break
		}
		pol := resolveCheckpointPolicy(prof, opts.Cfg)
		enabled, method := resolveCheckpointMode(deployArgs{}, pol, target.stage)
		user := doctorPGUser(hostSec)
		var dbSec doctorSections
		dbErr := withDBExecFallback(target, func(mode string) error {
			out, e := d.runBatch(ctx, dbExecCmd(remotePath, target, mode, odoo.Cmd{"sh", "-c", doctorDBScript(user, target.dbName)}))
			dbSec = splitDoctorSections(string(out))
			return e
		})
		emit(evalDisk(enabled, method, measureCheckpointDisk(method, hostSec, dbSec, dbErr)))
	}

	emit(evalLock(hostSec))

	if !usable {
		emit(DoctorCheck{ID: "dest", Status: doctorSkipped, Reason: "profile", Message: "not checked"})
		return finishDoctor(opts, res), nil
	}
	own := plannedPushDest(prof, opts.Cfg, remotePath)
	peers := make([]peerDest, 0, len(d.peers))
	for i, t := range d.peers {
		// A peer whose profile is missing or broken still resolves a
		// destination: the local [push] path, else the default candidates.
		peerProf, _ := config.ParseRemoteProfile([]byte(hostSec["global"].out), []byte(hostSec[peerSection(i)].out))
		peers = append(peers, peerDest{
			name:  t.Name,
			stage: remoteConnectTarget(peerProf).stage,
			dest:  plannedPushDest(peerProf, opts.Cfg, t.RemotePath),
		})
	}
	probes := probePaths(own, peers)
	realPaths := map[string]string{}
	if len(probes) > 0 {
		out, perr := d.runBatch(ctx, doctorDestScript(probes))
		if perr != nil {
			emit(DoctorCheck{ID: "dest", Status: doctorWarn, Message: "could not probe the push destination",
				Fields: [][2]string{{"error", lastNonEmptyLine([]byte(perr.Error()))}}})
			return finishDoctor(opts, res), nil
		}
		realPaths = destRealPaths(probes, splitDoctorSections(string(out)))
	}
	destCheck, ownReal := evalDest(own, realPaths)
	emit(destCheck)
	for _, c := range evalDestShared(ownReal, target.stage, peers, realPaths) {
		emit(c)
	}
	return finishDoctor(opts, res), nil
}

func (d doctorTarget) runBatch(ctx context.Context, script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, doctorBatchTimeout)
	defer cancel()
	return doctorRunSSH(ctx, d.host, script, nil)
}

// finishDoctor counts the checks and logs the summary line.
func finishDoctor(opts DoctorOpts, res DoctorResult) DoctorResult {
	for _, c := range res.Checks {
		switch c.Status {
		case doctorOK:
			res.Counts.OK++
		case doctorWarn:
			res.Counts.Warn++
		case doctorFailed:
			res.Counts.Failed++
		default:
			res.Counts.Skipped++
		}
	}
	opts.log("INFO", "", "doctor summary", res.Target.DB,
		[2]string{"target", res.Target.Name},
		[2]string{"ok", strconv.Itoa(res.Counts.OK)},
		[2]string{"warn", strconv.Itoa(res.Counts.Warn)},
		[2]string{"failed", strconv.Itoa(res.Counts.Failed)},
		[2]string{"skipped", strconv.Itoa(res.Counts.Skipped)})
	return res
}

func doctorLevel(status string) string {
	switch status {
	case doctorWarn:
		return "WARNING"
	case doctorFailed:
		return "ERROR"
	}
	return "INFO"
}

// sameHostTargets lists the connect targets on host other than the one at
// remotePath: the only ones that can share its push destination.
func sameHostTargets(targets []config.ConnectTarget, host, remotePath string) []config.ConnectTarget {
	var out []config.ConnectTarget
	for _, t := range targets {
		if t.SSHHost == host && t.RemotePath != remotePath && t.RemotePath != "" {
			out = append(out, t)
		}
	}
	return out
}

const doctorMarker = "@@echo-doctor "

// doctorSection appends cmd to a batch script, followed by a marker line with
// its exit status. The marker starts on a fresh line so output without a
// trailing newline cannot swallow it.
func doctorSection(b *strings.Builder, name, cmd string) {
	b.WriteString("(" + cmd + ") 2>/dev/null; printf '\\n" + doctorMarker + "%s %s\\n' " + name + " \"$?\"; ")
}

func peerSection(i int) string { return "peer." + strconv.Itoa(i) }

// doctorHostScript is batch 1: both server profile files and the peers'
// project profiles, rsync, git, the lock and whether the repository tracks
// it, `.env` (parsed in memory only) and the host's free space under
// remote_path. Read-only: cat, command -v, git queries, df.
func doctorHostScript(d doctorTarget) string {
	var b strings.Builder
	doctorSection(&b, "global", "cat ~/.config/echo/"+config.GlobalFileName)
	doctorSection(&b, "project", "cat "+remoteProjectProfilePath(d.path))
	for i, t := range d.peers {
		doctorSection(&b, peerSection(i), "cat "+remoteProjectProfilePath(t.RemotePath))
	}
	doctorSection(&b, "rsync", "command -v rsync")
	doctorSection(&b, "git", "git --version 2>&1")
	if d.git.enabled {
		dir := d.absGitDir()
		doctorSection(&b, "git.worktree", remoteGitCmd(dir, "rev-parse", "--is-inside-work-tree"))
		if d.rootCommit != "" {
			doctorSection(&b, "git.root", remoteGitCmd(dir, "cat-file", "-e", d.rootCommit))
		}
		doctorSection(&b, "git.branch", remoteGitCmd(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+d.git.branch))
		doctorSection(&b, "git.status", remoteGitCmd(dir, "status", "--porcelain"))
	}
	doctorSection(&b, "lock", lockReadScript(d.path))
	doctorSection(&b, "lock.tracked", "cd "+shellQuote(d.path)+" && git ls-files -- "+lockDirName)
	doctorSection(&b, "env", "cat "+shellQuote(d.path+"/.env"))
	doctorSection(&b, "hostdf", "df -Pk "+shellQuote(d.path))
	return b.String()
}

// remoteProjectProfilePath is where a server keeps the Echo profile of the
// project at remotePath; the ~ stays unquoted so the remote shell expands it.
func remoteProjectProfilePath(remotePath string) string {
	return "~/.config/echo/projects/" + config.ProjectKey(remotePath) + ".toml"
}

// doctorDestScript is batch 2: for each path, its resolved real path when it
// is an existing directory, and a non-zero status when it is not.
func doctorDestScript(paths []string) string {
	var b strings.Builder
	for i, p := range paths {
		q := shellQuote(p)
		doctorSection(&b, "dir."+strconv.Itoa(i), "test -d "+q+" && readlink -f "+q)
	}
	return b.String()
}

// doctorDBScript is batch 3, run with `sh -c` inside the DB container: the
// database size, the cluster's data directory and its free space.
func doctorDBScript(user, db string) string {
	psql := "psql -U " + shellQuote(user) + " -d postgres -At -c "
	var b strings.Builder
	doctorSection(&b, "size", psql+shellQuote("SELECT pg_database_size('"+sqlLit(db)+"')"))
	b.WriteString("d=$(" + psql + shellQuote("SHOW data_directory") + " 2>/dev/null); printf '%s\\n" + doctorMarker + "datadir %s\\n' \"$d\" \"$?\"; ")
	doctorSection(&b, "datadf", "df -Pk \"$d\"")
	return b.String()
}

type doctorOutput struct {
	out string
	rc  int
}

type doctorSections map[string]doctorOutput

func (s doctorSections) ok(name string) bool {
	sec, found := s[name]
	return found && sec.rc == 0
}

// splitDoctorSections cuts a batch's stdout at its marker lines. A section
// whose marker never arrived is absent from the map.
func splitDoctorSections(out string) doctorSections {
	sections := doctorSections{}
	rest := out
	for {
		i := strings.Index(rest, "\n"+doctorMarker)
		if i < 0 {
			return sections
		}
		body := rest[:i]
		line := rest[i+1+len(doctorMarker):]
		end := strings.IndexByte(line, '\n')
		if end < 0 {
			end = len(line)
		}
		if f := strings.Fields(line[:end]); len(f) == 2 {
			rc, err := strconv.Atoi(f[1])
			if err != nil {
				rc = -1
			}
			sections[f[0]] = doctorOutput{out: body, rc: rc}
		}
		if end < len(line) {
			end++
		}
		rest = line[end:]
	}
}

// doctorPGUser is the Postgres role from the server's .env, read for
// POSTGRES_USER only; the rest of the file never leaves this function.
func doctorPGUser(s doctorSections) string {
	if s.ok("env") {
		if u := env.Parse(strings.NewReader(s["env"].out))["POSTGRES_USER"]; u != "" {
			return u
		}
	}
	return "odoo"
}

func evalSSH(host string) DoctorCheck {
	c := DoctorCheck{ID: "ssh", Status: doctorOK, Message: "host reachable", Fields: [][2]string{{"host", host}}}
	if strings.Contains(host, "@") {
		c.warn("ssh_host is a literal user@host — it matches no Host block of ~/.ssh/config, so its port, key and ProxyJump are not applied")
	}
	return c
}

// evalProfile parses the server profile the way every remote verb does. It
// fails when the project profile is missing, does not parse or lacks the
// containers and database; it warns on an undeclared stage, an unknown Odoo
// version or invalid deploy actions.
func evalProfile(s doctorSections, key, project string) (config.RemoteProfile, connectTarget, DoctorCheck) {
	c := DoctorCheck{ID: "profile", Status: doctorOK, Message: "server profile"}
	file := "~/.config/echo/projects/" + key + ".toml"
	if !s.ok("project") {
		c.Status, c.Message = doctorFailed, "no Echo profile for this project on the server — run `init` there"
		c.Fields = [][2]string{{"file", file}}
		return config.RemoteProfile{}, connectTarget{}, c
	}
	prof, err := config.ParseRemoteProfile([]byte(s["global"].out), []byte(s["project"].out))
	if err != nil {
		var perr *config.ParseError
		detail := err.Error()
		if errors.As(err, &perr) {
			detail = perr.Detail()
			if perr.Path == config.GlobalFileName {
				file = "~/.config/echo/" + config.GlobalFileName
			}
		}
		c.Status, c.Message = doctorFailed, "server profile does not parse"
		c.Fields = [][2]string{{"file", file}, {"error", detail}}
		return config.RemoteProfile{}, connectTarget{}, c
	}
	var missing []string
	for _, f := range [][2]string{{"db_name", prof.DBName}, {"odoo_container", prof.OdooContainer}, {"db_container", prof.DBContainer}} {
		if strings.TrimSpace(f[1]) == "" {
			missing = append(missing, f[0])
		}
	}
	if len(missing) > 0 {
		c.Status, c.Message = doctorFailed, "server profile is incomplete"
		c.Fields = [][2]string{{"file", file}, {"missing", strings.Join(missing, ",")}}
		return prof, remoteConnectTarget(prof), c
	}
	target := remoteConnectTarget(prof)
	c.Fields = statusFields(target.odooVersion, prof.Stage, project, prof.DBName)
	if !target.stageDeclared {
		c.warn(fmt.Sprintf("stage is not declared (stage=%q) — gated as prod", target.rawStage),
			[2]string{"stage_declared", "false"})
	}
	if strings.TrimSpace(prof.OdooVersion) == "" {
		c.warn("odoo_version is not set")
	}
	if err := config.ValidateDeployActions(prof.DeployActions); err != nil {
		c.warn("deploy actions are invalid", [2]string{"actions", err.Error()})
	}
	return prof, target, c
}

// evalRsync judges rsync on both ends. A missing rsync fails only when a
// deploy would push; otherwise it is a warning about `push`.
func evalRsync(localErr error, s doctorSections, push bool) DoctorCheck {
	c := DoctorCheck{ID: "rsync", Status: doctorOK, Message: "rsync found on both ends",
		Fields: [][2]string{{"push", onOff(push)}}}
	local, remote := localErr == nil, s.ok("rsync")
	if local && remote {
		return c
	}
	side, where := "both", "locally and on the server"
	switch {
	case local:
		side, where = "remote", "on the server"
	case remote:
		side, where = "local", "locally"
	}
	c.Status, c.Message = doctorWarn, "rsync not found "+where
	if push {
		c.Status = doctorFailed
	}
	c.Fields = append([][2]string{{"side", side}}, c.Fields...)
	return c
}

// evalGit mirrors gitPreflight on a git-deploy target and adds what the
// deploy would find: whether the deploy branch exists and how dirty the
// checkout is.
func evalGit(g gitDeployConfig, absDir, rootCommit string, s doctorSections) DoctorCheck {
	c := DoctorCheck{ID: "git"}
	if !g.enabled {
		c.Status, c.Reason, c.Message = doctorSkipped, "git_deploy_off", "rsync target, no git deploy"
		return c
	}
	c.Fields = [][2]string{{"dir", absDir}, {"branch", g.branch}}
	if !s.ok("git") {
		cause := lastNonEmptyLine([]byte(s["git"].out))
		if cause == "" {
			cause = "exit status " + strconv.Itoa(s["git"].rc)
		}
		c.Status, c.Message = doctorFailed, errRemoteGitMissing(errors.New(cause)).Error()
		return c
	}
	if !s.ok("git.worktree") || strings.TrimSpace(s["git.worktree"].out) != "true" {
		c.Status, c.Message = doctorFailed, errNotACheckout(absDir).Error()
		return c
	}
	if rootCommit != "" && !s.ok("git.root") {
		c.Status, c.Message = doctorFailed, errNotAClone(absDir, rootCommit).Error()
		return c
	}
	branchExists := "no"
	if s.ok("git.branch") {
		branchExists = "yes"
	}
	c.Status, c.Message = doctorOK, "git deploy ready"
	c.Fields = append(c.Fields,
		[2]string{"branch_exists", branchExists},
		[2]string{"dirty", strconv.Itoa(len(nonEmptyLines(s["git.status"].out)))})
	if rootCommit == "" {
		c.warn("the current directory is not a git repository — cannot tell whether the checkout is a clone of it")
	}
	return c
}

// diskMeasure is the database size and the free space where a checkpoint of
// the measured method lands; err is set when either could not be read.
type diskMeasure struct {
	size, free int64
	err        error
}

// measureCheckpointDisk reads the measurements from the batches: the size from
// the DB batch, the free space from the data directory (`db`) or from the
// host filesystem under remote_path (`dump`), as checkpointPreflight does.
func measureCheckpointDisk(method string, host, db doctorSections, dbErr error) diskMeasure {
	if dbErr != nil {
		return diskMeasure{err: errors.New(lastNonEmptyLine([]byte(dbErr.Error())))}
	}
	if !db.ok("size") {
		return diskMeasure{err: errors.New("database size unreadable")}
	}
	size, err := strconv.ParseInt(strings.TrimSpace(db["size"].out), 10, 64)
	if err != nil {
		return diskMeasure{err: fmt.Errorf("database size unreadable: %w", err)}
	}
	sections, freeSec := db, "datadf"
	if method == "dump" {
		sections, freeSec = host, "hostdf"
	}
	if !sections.ok(freeSec) {
		return diskMeasure{err: errors.New("free space unreadable")}
	}
	free, err := parseDfAvailable(sections[freeSec].out)
	if err != nil {
		return diskMeasure{err: fmt.Errorf("free space unreadable: %w", err)}
	}
	return diskMeasure{size: size, free: free}
}

// evalDisk fails when a deploy would checkpoint and the disk is short of
// checkpointNeed; with checkpoints off a short disk only warns.
func evalDisk(enabled bool, method string, m diskMeasure) DoctorCheck {
	c := DoctorCheck{ID: "disk", Status: doctorOK,
		Fields: [][2]string{{"checkpoint", onOff(enabled)}, {"method", method}}}
	if m.err != nil {
		c.Status, c.Message = doctorWarn, "checkpoint disk unmeasurable"
		c.Fields = append(c.Fields, [2]string{"error", m.err.Error()})
		return c
	}
	need := checkpointNeed(m.size, method)
	c.Fields = append(c.Fields,
		[2]string{"db_size", humanBytes(m.size)}, [2]string{"need", humanBytes(need)}, [2]string{"free", humanBytes(m.free)})
	switch {
	case m.free >= need:
		c.Message = "enough disk for a " + method + " checkpoint"
	case enabled:
		c.Status, c.Message = doctorFailed, "not enough disk for a "+method+" checkpoint"
	default:
		c.Status, c.Message = doctorWarn, "not enough disk for a "+method+" checkpoint (checkpoints are off)"
	}
	return c
}

// evalLock reports the lock's state and summary. Nothing about it fails: the
// lock is metadata and the next deploy rewrites it.
func evalLock(s doctorSections) DoctorCheck {
	c := DoctorCheck{ID: "lock", Status: doctorOK}
	sec, found := s["lock"]
	switch {
	case !found || sec.rc != 0:
		c.warn("deploy lock unreadable", [2]string{"state", lockUnreadable.String()})
	default:
		raw, state := lockReadOutput([]byte(sec.out))
		var lock DeployLock
		var err error
		if state == lockFound {
			lock, state, err = parseDeployLock(raw)
		}
		c.Fields = [][2]string{{"state", state.String()}}
		switch state {
		case lockAbsent:
			c.Message = "no deploy lock"
		case lockCorrupt:
			c.Message = "deploy lock"
			c.warn("deploy lock is corrupt — the next write replaces it", [2]string{"error", err.Error()})
		default:
			c.Message = "deploy lock"
			c.Fields = append(c.Fields, lockSummaryFields(lock)...)
			for _, e := range lock.Modules {
				if !e.Verified {
					c.warn("deploy lock has unverified entries — a push or a failed run left them")
					break
				}
			}
		}
	}
	if tracked := nonEmptyLines(s["lock.tracked"].out); len(tracked) > 0 {
		c.warn("the server's git repository tracks .echo/ — untrack it there with `git rm --cached -r .echo`",
			[2]string{"files", strings.Join(tracked, ",")})
	}
	return c
}

// pushDestPlan is where a push to one target would land without flags: an
// explicit destination (server, then local [push] path) or, when none is
// configured, the auto-detect candidates in order.
type pushDestPlan struct {
	raw        string
	explicit   string
	source     string
	mkdir      bool
	candidates []string
}

func plannedPushDest(prof config.RemoteProfile, cfg *config.Config, remotePath string) pushDestPlan {
	dest, source, mkdir := resolvePushDest(pushArgs{}, prof, cfg)
	if dest != "" {
		return pushDestPlan{raw: dest, explicit: resolveDestPath(remotePath, dest), source: source, mkdir: mkdir}
	}
	var candidates []string
	for _, b := range remoteAddonsCandidates(prof.AddonsPaths) {
		candidates = append(candidates, path.Join(remotePath, b))
	}
	return pushDestPlan{source: "auto", candidates: candidates}
}

func (p pushDestPlan) paths() []string {
	if p.raw != "" {
		if p.explicit == "" {
			return nil
		}
		return []string{p.explicit}
	}
	return p.candidates
}

// realDest is the real path the plan lands in, "" when it does not exist yet.
func (p pushDestPlan) realDest(realPaths map[string]string) string {
	for _, d := range p.paths() {
		if r := realPaths[d]; r != "" {
			return r
		}
	}
	return ""
}

type peerDest struct {
	name  string
	stage string
	dest  pushDestPlan
}

func probePaths(own pushDestPlan, peers []peerDest) []string {
	seen := map[string]bool{}
	var out []string
	add := func(paths []string) {
		for _, p := range paths {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	add(own.paths())
	for _, p := range peers {
		add(p.dest.paths())
	}
	return out
}

// destRealPaths maps each probed path to its `readlink -f`, omitting the ones
// that are not existing directories.
func destRealPaths(probes []string, s doctorSections) map[string]string {
	realPaths := map[string]string{}
	for i, p := range probes {
		name := "dir." + strconv.Itoa(i)
		if s.ok(name) {
			if r := firstLine(s[name].out); r != "" {
				realPaths[p] = r
			}
		}
	}
	return realPaths
}

// evalDest checks the destination a push would use and returns its real path
// ("" when it does not exist). The compose root, a missing directory that
// would not be created, and an auto-detect with no existing addons directory
// fail.
func evalDest(own pushDestPlan, realPaths map[string]string) (DoctorCheck, string) {
	c := DoctorCheck{ID: "dest", Status: doctorOK, Message: "push destination"}
	if own.raw == "" {
		for _, cand := range own.candidates {
			if r := realPaths[cand]; r != "" {
				c.Fields = [][2]string{{"dest", cand}, {"source", own.source}, {"detect", "base"}}
				if r != cand {
					c.Fields = append(c.Fields, [2]string{"real", r})
				}
				return c, r
			}
		}
		c.Status, c.Message = doctorFailed, "no addons directory found for a push"
		c.Fields = [][2]string{{"tried", strings.Join(own.candidates, ",")}, {"source", own.source}}
		return c, ""
	}
	if own.explicit == "" {
		c.Status, c.Message = doctorFailed, "push destination cannot be the compose project root"
		c.Fields = [][2]string{{"dest", own.raw}, {"source", own.source}}
		return c, ""
	}
	c.Fields = [][2]string{{"dest", own.explicit}, {"source", own.source}}
	r := realPaths[own.explicit]
	switch {
	case r != "":
		if r != own.explicit {
			c.Fields = append(c.Fields, [2]string{"real", r})
		}
	case own.mkdir:
		c.Message = "push destination does not exist yet — the next push creates it"
		c.Fields = append(c.Fields, [2]string{"mkdir", "true"})
	default:
		c.Status, c.Message = doctorFailed, "push destination does not exist (pass --mkdir or set [push] mkdir = true)"
	}
	return c, r
}

// evalDestShared reports each same-host target whose push destination is the
// same directory: a warning when that target declares another stage, plain
// information when the stage matches. Sharing is legal, so it never fails.
func evalDestShared(ownReal, stage string, peers []peerDest, realPaths map[string]string) []DoctorCheck {
	if ownReal == "" {
		return nil
	}
	var out []DoctorCheck
	for _, p := range peers {
		if p.dest.realDest(realPaths) != ownReal {
			continue
		}
		c := DoctorCheck{ID: "dest.shared", Status: doctorOK, Message: "push destination shared",
			Fields: [][2]string{{"dest", ownReal}, {"with", p.name}, {"stage", p.stage}}}
		if p.stage != stage {
			c.Status = doctorWarn
		}
		out = append(out, c)
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
