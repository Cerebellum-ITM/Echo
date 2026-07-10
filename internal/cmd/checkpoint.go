package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// CheckpointOpts configures a `checkpoint` run.
type CheckpointOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	Palette   theme.Palette
	Log       func(level, sub, msg, db string, fields ...[2]string)
	StreamOut func(string)
}

func (o CheckpointOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

func (o CheckpointOpts) stream(line string) {
	if o.StreamOut != nil {
		o.StreamOut(line)
	}
}

// checkpointArgs is the parsed shape of the checkpoint input.
type checkpointArgs struct {
	sub     string // "list" | "create" | "rm"
	name    string // rm target (optional)
	from    string
	remote  bool
	jsonOut bool
	method  string
	all     bool
	force   bool
}

// parseCheckpointArgs parses the subcommand (default list) plus
// --from/--remote/--json/--method/--all/--force. Remote switches are read via
// remoteFlagsIn; the rest are consumed here.
func parseCheckpointArgs(args []string) (checkpointArgs, error) {
	out := checkpointArgs{sub: "list"}
	out.from, out.remote = remoteFlagsIn(args)
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			i++ // value consumed by remoteFlagsIn
		case strings.HasPrefix(a, "--from="), a == "--remote":
			// consumed by remoteFlagsIn
		case a == "--json":
			out.jsonOut = true
		case a == "--all":
			out.all = true
		case a == "--force":
			out.force = true
		case a == "--method":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%w: --method requires db or dump", ErrUsage)
			}
			out.method = args[i+1]
			i++
		case strings.HasPrefix(a, "--method="):
			out.method = strings.TrimPrefix(a, "--method=")
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) > 0 {
		out.sub = positionals[0]
	}
	switch out.sub {
	case "list", "create", "rm":
	default:
		return out, fmt.Errorf("%w: unknown checkpoint subcommand %q (use list, create, or rm)", ErrUsage, out.sub)
	}
	if out.sub == "rm" && len(positionals) > 1 {
		out.name = positionals[1]
	}
	if out.method != "" && out.method != "db" && out.method != "dump" {
		return out, fmt.Errorf("%w: --method takes db or dump, got %q", ErrUsage, out.method)
	}
	return out, nil
}

// CheckpointRow is one checkpoint in a `checkpoint list` result.
type CheckpointRow struct {
	Name       string   `json:"name"`
	Method     string   `json:"method"`
	SizeBytes  int64    `json:"size_bytes"`
	Size       string   `json:"size"`
	AgeSeconds int64    `json:"age_seconds"`
	Age        string   `json:"age"`
	Status     string   `json:"status"` // ok | stale | orphan
	DeploySHAs []string `json:"deploy_shas,omitempty"`
}

// CheckpointResult is the machine-readable summary a `checkpoint list`
// produces (also carries the live DB size and disk-free footer).
type CheckpointResult struct {
	Sub           string          `json:"-"`
	Rows          []CheckpointRow `json:"checkpoints"`
	DB            string          `json:"db"`
	DBSizeBytes   int64           `json:"db_size_bytes"`
	DBSize        string          `json:"db_size"`
	DiskFreeBytes int64           `json:"disk_free_bytes"`
	DiskFree      string          `json:"disk_free"`
	JSON          bool            `json:"-"`
}

// RunCheckpoint dispatches the checkpoint subcommands (list/create/rm) against
// a resolved remote target. list measures and reconciles stored checkpoints
// against the server; create takes a manual checkpoint; rm deletes one or all.
func RunCheckpoint(ctx context.Context, opts CheckpointOpts) (CheckpointResult, error) {
	p, err := parseCheckpointArgs(opts.Args)
	if err != nil {
		return CheckpointResult{}, err
	}
	logFn := func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) }
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, logFn)
	if err != nil {
		return CheckpointResult{}, err
	}
	projectKey := config.ProjectKey(opts.Root)
	targetKey := config.DeployTargetKey(rsc.sshHost, rsc.remotePath)

	switch p.sub {
	case "create":
		return runCheckpointCreate(ctx, opts, rsc, p, projectKey, targetKey)
	case "rm":
		return runCheckpointRm(ctx, opts, rsc, p, projectKey, targetKey)
	default:
		return runCheckpointList(ctx, opts, rsc, p, projectKey, targetKey)
	}
}

// runCheckpointList measures every recorded checkpoint, reconciles the store
// against the server (flagging stale/orphan entries and pruning stale
// metadata), and reports the live DB size + disk-free footer.
func runCheckpointList(ctx context.Context, opts CheckpointOpts, rsc remoteShellContext, p checkpointArgs, projectKey, targetKey string) (CheckpointResult, error) {
	db := rsc.prof.DBName
	entries := config.LoadCheckpoints(projectKey, targetKey)

	remoteDBs, _ := remoteListDatabases(ctx, rsc)
	dbSet := make(map[string]bool, len(remoteDBs))
	for _, d := range remoteDBs {
		dbSet[d] = true
	}
	tracked := make(map[string]bool, len(entries))

	var rows []CheckpointRow
	for _, e := range entries {
		tracked[e.Name] = true
		row := CheckpointRow{
			Name: e.Name, Method: e.Method, Status: "ok",
			AgeSeconds: int64(time.Since(e.CreatedAt).Seconds()),
			Age:        humanAge(time.Since(e.CreatedAt)),
			DeploySHAs: shortSHAs(e.DeploySHAs),
		}
		exists := false
		if e.Method == "dump" {
			exists = remoteFileExists(ctx, rsc, e.DumpPath)
			if exists {
				sz, _ := remoteFileSize(ctx, rsc, e.DumpPath)
				row.SizeBytes = sz
			}
		} else {
			exists = dbSet[e.Name]
			if exists {
				sz, _ := remoteDBSize(ctx, rsc, e.Name)
				row.SizeBytes = sz
			}
		}
		if !exists {
			row.Status = "stale"
			// The recorded object is gone: drop it from the store so the list
			// converges on reality.
			_ = config.RemoveCheckpoint(projectKey, targetKey, e.Name)
		}
		row.Size = humanBytes(row.SizeBytes)
		rows = append(rows, row)
	}

	// Untracked __ckpt_ databases on the server surface as orphans.
	for _, d := range remoteDBs {
		if tracked[d] || !strings.Contains(d, "__ckpt_") {
			continue
		}
		sz, _ := remoteDBSize(ctx, rsc, d)
		rows = append(rows, CheckpointRow{
			Name: d, Method: "db", Status: "orphan",
			SizeBytes: sz, Size: humanBytes(sz), Age: "—",
		})
	}

	dbSize, _ := remoteDBSize(ctx, rsc, db)
	var freeBytes int64
	if dataDir, err := remoteDataDir(ctx, rsc); err == nil {
		freeBytes, _ = remoteDiskFreeBytes(ctx, rsc, dataDir)
	}

	res := CheckpointResult{
		Sub: "list", Rows: rows, DB: db,
		DBSizeBytes: dbSize, DBSize: humanBytes(dbSize),
		DiskFreeBytes: freeBytes, DiskFree: humanBytes(freeBytes),
		JSON: p.jsonOut,
	}
	if !p.jsonOut {
		renderCheckpointTable(opts, res)
	}
	return res, nil
}

// runCheckpointCreate takes a manual checkpoint using the deploy machinery:
// preflight, then (db method) stop → template copy → up, or (dump method) a
// live pg_dump. It records the checkpoint and prunes the retention tail.
func runCheckpointCreate(ctx context.Context, opts CheckpointOpts, rsc remoteShellContext, p checkpointArgs, projectKey, targetKey string) (CheckpointResult, error) {
	method := p.method
	if method == "" {
		method = opts.Cfg.CheckpointMethod
		if method == "" {
			method = "db"
		}
	}
	if err := confirmRemoteProd(opts.Palette, "checkpoint", rsc, opts.Args); err != nil {
		return CheckpointResult{}, err
	}
	if err := checkpointPreflight(ctx, rsc, method, opts.Log); err != nil {
		return CheckpointResult{}, err
	}

	stopStack := method != "dump" // a template copy needs exclusive DB access
	if stopStack {
		opts.log("INFO", "checkpoint", "stopping stack for template copy", rsc.prof.DBName)
		if err := runSSHStream(ctx, rsc.sshHost, remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "stop"), nil, opts.StreamOut); err != nil {
			return CheckpointResult{}, fmt.Errorf("stop failed: %w", err)
		}
	}
	entry, info, err := createCheckpoint(ctx, rsc, method, nil, opts.StreamOut, opts.Log)
	if stopStack {
		// Bring the stack back up regardless of the checkpoint outcome.
		_ = runSSHStream(ctx, rsc.sshHost, remoteComposeCmd(rsc.remotePath, rsc.target.composeCmd, "up", "-d"), nil, opts.StreamOut)
	}
	if err != nil {
		return CheckpointResult{}, err
	}
	if err := config.AddCheckpoint(projectKey, targetKey, entry); err == nil {
		pruneCheckpoints(ctx, rsc, projectKey, targetKey, opts.Cfg.CheckpointKeep, opts.Log)
	}
	opts.stream("→ checkpoint " + info.Name)
	return CheckpointResult{Sub: "create", DB: rsc.prof.DBName}, nil
}

// runCheckpointRm deletes a checkpoint (or all of them) — the remote object
// plus its store entry — behind a red confirmation.
func runCheckpointRm(ctx context.Context, opts CheckpointOpts, rsc remoteShellContext, p checkpointArgs, projectKey, targetKey string) (CheckpointResult, error) {
	entries := config.LoadCheckpoints(projectKey, targetKey)

	if p.all {
		if len(entries) == 0 {
			opts.log("INFO", "checkpoint", "nothing to remove", rsc.prof.DBName)
			return CheckpointResult{Sub: "rm", DB: rsc.prof.DBName}, nil
		}
		if !p.force {
			if err := confirmCheckpointRm(opts.Palette, rsc.prof.DBName, strconv.Itoa(len(entries))+" checkpoints"); err != nil {
				return CheckpointResult{}, err
			}
		}
		for _, e := range entries {
			if err := destroyCheckpointObject(ctx, rsc, e); err != nil {
				opts.log("WARNING", "checkpoint", "remove failed", rsc.prof.DBName,
					[2]string{"name", e.Name}, [2]string{"err", err.Error()})
				continue
			}
			_ = config.RemoveCheckpoint(projectKey, targetKey, e.Name)
			opts.log("INFO", "checkpoint", "removed", rsc.prof.DBName, [2]string{"name", e.Name})
		}
		opts.stream("→ removed all checkpoints")
		return CheckpointResult{Sub: "rm", DB: rsc.prof.DBName}, nil
	}

	// Single removal: resolve the name (picker when omitted and interactive).
	name := p.name
	entryByName := make(map[string]config.CheckpointEntry, len(entries))
	for _, e := range entries {
		entryByName[e.Name] = e
	}
	if name == "" {
		if err := requireTTY("checkpoint rm needs a name without a TTY (pass the name or --all)"); err != nil {
			return CheckpointResult{}, err
		}
		if len(entries) == 0 {
			return CheckpointResult{}, fmt.Errorf("%w: no checkpoints to remove", ErrUsage)
		}
		labels := make([]string, len(entries))
		for i, e := range entries {
			labels[i] = e.Name + "  ·  " + e.Method + ", " + humanAge(time.Since(e.CreatedAt)) + " ago"
		}
		pick, err := PickOne("Checkpoint to remove", labels, opts.Palette)
		if err != nil {
			return CheckpointResult{}, err
		}
		name = strings.SplitN(pick, "  ·  ", 2)[0]
	}

	if !p.force {
		if err := confirmCheckpointRm(opts.Palette, rsc.prof.DBName, name); err != nil {
			return CheckpointResult{}, err
		}
	}

	entry, tracked := entryByName[name]
	if !tracked {
		// Allow removing an orphan __ckpt_ DB surfaced by `list`.
		entry = config.CheckpointEntry{Name: name, Method: "db", DB: rsc.prof.DBName}
	}
	if err := destroyCheckpointObject(ctx, rsc, entry); err != nil {
		return CheckpointResult{}, fmt.Errorf("remove %s: %w", name, err)
	}
	if tracked {
		_ = config.RemoveCheckpoint(projectKey, targetKey, name)
	}
	opts.log("INFO", "checkpoint", "removed", rsc.prof.DBName, [2]string{"name", name})
	opts.stream("→ removed " + name)
	return CheckpointResult{Sub: "rm", DB: rsc.prof.DBName}, nil
}

// renderCheckpointTable prints the human `checkpoint list` output: an aligned
// table of the checkpoints followed by a footer with the live DB size and the
// server's free disk.
func renderCheckpointTable(opts CheckpointOpts, res CheckpointResult) {
	if len(res.Rows) == 0 {
		opts.stream("no checkpoints for this target")
	} else {
		nameW, methodW, sizeW, ageW := len("NAME"), len("METHOD"), len("SIZE"), len("AGE")
		for _, r := range res.Rows {
			nameW = max2(nameW, len(r.Name))
			methodW = max2(methodW, len(r.Method))
			sizeW = max2(sizeW, len(r.Size))
			ageW = max2(ageW, len(r.Age))
		}
		header := fmt.Sprintf("%-*s  %-*s  %*s  %*s  %-7s  %s",
			nameW, "NAME", methodW, "METHOD", sizeW, "SIZE", ageW, "AGE", "STATUS", "COMMITS")
		opts.stream(header)
		for _, r := range res.Rows {
			opts.stream(fmt.Sprintf("%-*s  %-*s  %*s  %*s  %-7s  %s",
				nameW, r.Name, methodW, r.Method, sizeW, r.Size, ageW, r.Age,
				r.Status, strings.Join(r.DeploySHAs, ",")))
		}
	}
	opts.log("INFO", "system", "system", res.DB,
		[2]string{"db_size", res.DBSize}, [2]string{"disk_free", res.DiskFree},
		[2]string{"checkpoints", strconv.Itoa(len(res.Rows))})
}

// confirmRollback is the interactive deploy-failure gate: a red confirm asking
// whether to restore the DB from the just-taken checkpoint.
func confirmRollback(palette theme.Palette, db string, entry config.CheckpointEntry) bool {
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Deploy run failed on "+red).
			Description("Roll back to checkpoint "+entry.Name+"? (declining keeps the broken DB for inspection)").
			Affirmative("Roll back").
			Negative("Keep broken DB").
			Value(&confirmed),
	)).
		WithTheme(BuildHuhTheme(palette)).
		WithInput(os.Stdin).
		WithOutput(os.Stdout)
	if err := form.Run(); err != nil {
		return false
	}
	return confirmed
}

// confirmRollbackAged is the `deploy --rollback` gate: a red confirm that
// spells out the checkpoint's age (in red when older than an hour) so the
// operator knows how much post-deploy data a restore would discard.
func confirmRollbackAged(palette theme.Palette, db string, entry config.CheckpointEntry) error {
	if err := requireTTY("pass --force to roll back without a prompt"); err != nil {
		return err
	}
	age := time.Since(entry.CreatedAt)
	redDB := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(db)
	desc := "Restoring checkpoint " + entry.Name + " discards every change since it was taken."
	if age > time.Hour {
		redAge := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(humanAge(age) + " old")
		desc += " This checkpoint is " + redAge + " — that is a lot of data to lose."
	} else {
		desc += " Age: " + humanAge(age) + "."
	}
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Roll back database "+redDB).
			Description(desc).
			Affirmative("Roll back").
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

// confirmCheckpointRm is the red confirm before deleting checkpoint(s).
func confirmCheckpointRm(palette theme.Palette, db, what string) error {
	if err := requireTTY("pass --force to remove without a prompt"); err != nil {
		return err
	}
	red := lipgloss.NewStyle().Foreground(palette.Error).Bold(true).Render(what)
	confirmed := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("⚠  Remove "+red).
			Description("This deletes the checkpoint on "+db+" permanently.").
			Affirmative("Remove").
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

// humanAge renders a duration as a compact age (e.g. "45s", "12m", "3h20m",
// "5d").
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return strconv.Itoa(h) + "h"
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		return strconv.Itoa(int(d.Hours())/24) + "d"
	}
}

// shortSHAs truncates each SHA to 7 chars for compact display.
func shortSHAs(shas []string) []string {
	if len(shas) == 0 {
		return nil
	}
	out := make([]string, len(shas))
	for i, s := range shas {
		out[i] = shortSHA(s)
	}
	return out
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
