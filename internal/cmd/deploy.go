package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// DeployOpts configures a `deploy` run.
type DeployOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log emits one Odoo-style progress line (rendered by the REPL through
	// emitOdooLog under `echo.deploy[.sub]`), mirroring i18n-pull's logger.
	Log func(level, sub, msg, db string, fields ...[2]string)
	// StreamOut receives the remote stop/up/odoo lines as they stream.
	StreamOut func(string)
}

// log emits a progress line when a logger is set; a no-op otherwise.
func (o DeployOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// deployArgs is the parsed shape of the deploy input.
type deployArgs struct {
	from   string
	limit  int
	dryRun bool
	force  bool
}

// parseDeployArgs extracts --from/--limit/--dry-run/--force. Deploy takes
// no positionals — the commits come from the interactive picker.
func parseDeployArgs(args []string) (deployArgs, error) {
	out := deployArgs{limit: 20}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--from requires a target name")
			}
			out.from = args[i+1]
			i++
		case strings.HasPrefix(a, "--from="):
			out.from = strings.TrimPrefix(a, "--from=")
		case a == "--limit":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--limit requires a number")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return out, fmt.Errorf("--limit takes a positive number, got %q", args[i+1])
			}
			out.limit = n
			i++
		case strings.HasPrefix(a, "--limit="):
			v := strings.TrimPrefix(a, "--limit=")
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return out, fmt.Errorf("--limit takes a positive number, got %q", v)
			}
			out.limit = n
		case a == "--dry-run":
			out.dryRun = true
		case a == "--force":
			out.force = true
		case strings.HasPrefix(a, "-"):
			return out, fmt.Errorf("unknown flag: %s", a)
		default:
			return out, fmt.Errorf("deploy takes no positional arguments (commits are picked interactively)")
		}
	}
	return out, nil
}

// deployCommit is one local commit offered in the picker.
type deployCommit struct {
	sha     string
	subject string
}

func (c deployCommit) short() string {
	if len(c.sha) > 7 {
		return c.sha[:7]
	}
	return c.sha
}

// deploySubjectRe captures the module name from the project's commit
// scheme `[Tag] module_name: title`.
var deploySubjectRe = regexp.MustCompile(`^\[[^\]]+\]\s*([A-Za-z0-9_]+)\s*:`)

// RunDeploy deploys selected local commits to a remote Odoo instance: a
// multi-select picker over the repo's recent commits, commit→module
// resolution (subject scheme, then single-module diff fallback; unresolved
// commits are skipped and reported), an install/update split from the
// remote `ir_module_module` state, then — plan shown, prod gated — a
// streamed remote `stop` → `up -d` → one combined `-i`/`-u` Odoo run.
// Pre-condition: the new code is already pulled on the server.
func RunDeploy(ctx context.Context, opts DeployOpts) error {
	p, err := parseDeployArgs(opts.Args)
	if err != nil {
		return err
	}

	sshHost, remotePath, fromName, err := resolveDeployRemote(opts, p.from)
	if err != nil {
		return err
	}
	opts.log("INFO", "remote", "target resolved", "",
		[2]string{"host", sshHost}, [2]string{"path", remotePath})

	// Commit selection — interactive by design; a headless run fails closed
	// inside the picker (ErrNonInteractive).
	commits, err := gitRecentCommits(ctx, opts.Root, p.limit)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("no commits found in %s", opts.Root)
	}
	selected, err := pickDeployCommits(commits, opts.Palette)
	if err != nil {
		return err
	}
	opts.log("INFO", "", "commits selected", "",
		[2]string{"n", strconv.Itoa(len(selected))})

	// Commit → module resolution. Unresolved commits are excluded and
	// reported, never fatal — unless nothing at all resolves.
	seen := map[string]bool{}
	var modules []string
	var skipped int
	for _, c := range selected {
		mod, via, reason := resolveCommitModule(ctx, opts.Root, c)
		if mod == "" {
			opts.log("WARNING", "", "skipped", "",
				[2]string{"commit", c.short()}, [2]string{"reason", reason})
			skipped++
			continue
		}
		opts.log("INFO", "", "resolved", "",
			[2]string{"commit", c.short()}, [2]string{"module", mod}, [2]string{"via", via})
		if !seen[mod] {
			seen[mod] = true
			modules = append(modules, mod)
		}
	}
	if len(modules) == 0 {
		return fmt.Errorf("no deployable modules: every selected commit was skipped")
	}
	sort.Strings(modules)

	// Remote profile + DB credentials, same as i18n-pull.
	cfgRemote := *opts.Cfg
	cfgRemote.ConnectSSHHost = sshHost
	cfgRemote.ConnectRemotePath = remotePath
	opts.log("INFO", "remote", "reading remote profile", "", [2]string{"host", sshHost})
	prof, err := fetchRemoteProfile(ctx, ConnectOpts{Cfg: &cfgRemote, Root: opts.Root})
	if err != nil {
		return err
	}
	target := connectTarget{
		remote:        true,
		composeCmd:    prof.ComposeCmd,
		odooContainer: prof.OdooContainer,
		dbContainer:   prof.DBContainer,
		dbName:        prof.DBName,
		stage:         prof.Stage,
		odooVersion:   prof.OdooVersion,
	}
	opts.log("INFO", "system", "system", prof.DBName,
		statusFields(target.odooVersion, prof.Stage,
			statusProjectName(opts.Cfg, true, remotePath, fromName),
			prof.DBName)...)
	conn := odoo.Conn{DB: target.dbName, Host: target.dbContainer}
	pg := remotePullEnv(ctx, sshHost, remotePath)
	conn.Port = pg["POSTGRES_PORT"]
	conn.User = pg["POSTGRES_USER"]
	conn.Password = pg["POSTGRES_PASSWORD"]

	// Install vs update, decided by the remote instance's module states.
	opts.log("INFO", "remote", "querying installed modules", prof.DBName)
	states, err := remoteModuleStates(ctx, sshHost, remotePath, target, conn.User, target.dbName)
	if err != nil {
		return fmt.Errorf("query remote module states: %w", err)
	}
	install, update := splitInstallUpdate(modules, states)
	opts.log("INFO", "", "plan", prof.DBName,
		[2]string{"update", strings.Join(update, ",")},
		[2]string{"install", strings.Join(install, ",")},
		[2]string{"skipped", strconv.Itoa(skipped)})

	if p.dryRun {
		opts.log("INFO", "", "dry-run — nothing executed", prof.DBName)
		return nil
	}
	if strings.EqualFold(target.stage, "prod") && !p.force {
		if err := confirmProd(opts.Palette, "deploy", target.dbName); err != nil {
			return err
		}
	}

	// The three remote steps, each streamed live. Fail-fast with the step
	// named in the error.
	step := func(name, remoteCmd string) error {
		opts.log("INFO", "compose", name, prof.DBName)
		if err := runSSHStream(ctx, sshHost, remoteCmd, nil, opts.StreamOut); err != nil {
			return fmt.Errorf("%s failed: %w", name, err)
		}
		return nil
	}
	if err := step("stop", remoteComposeCmd(remotePath, target.composeCmd, "stop")); err != nil {
		return err
	}
	if err := step("up -d", remoteComposeCmd(remotePath, target.composeCmd, "up", "-d")); err != nil {
		return err
	}
	opts.log("INFO", "odoo", "running module install/update", prof.DBName)
	argv := odoo.InstallUpdate(conn, install, update)
	if err := runSSHStream(ctx, sshHost, remoteContainerCmd(remotePath, target, argv), nil, opts.StreamOut); err != nil {
		return fmt.Errorf("odoo run failed: %w", err)
	}

	opts.log("INFO", "", "deploy complete", prof.DBName,
		[2]string{"update", strconv.Itoa(len(update))},
		[2]string{"install", strconv.Itoa(len(install))},
		[2]string{"skipped", strconv.Itoa(skipped)})
	return nil
}

// resolveDeployRemote mirrors i18n-pull's target resolution: --from →
// project [connect] → global targets fallback (one auto, several picker).
func resolveDeployRemote(opts DeployOpts, from string) (sshHost, remotePath, fromName string, err error) {
	return resolveRemoteTarget(opts.Cfg, opts.Palette, from, opts.Log)
}

// resolveRemoteTarget is the shared remote resolution used by deploy,
// shell and shell-run: an explicit --from name, else the project/link
// [connect], else the global targets fallback (one auto-used, several
// open a TTY-guarded picker, none → ErrNoPullRemote).
func resolveRemoteTarget(cfg *config.Config, palette theme.Palette, from string, log func(level, sub, msg, db string, fields ...[2]string)) (sshHost, remotePath, fromName string, err error) {
	sshHost, remotePath, err = resolvePullRemote(cfg, from)
	if errors.Is(err, ErrNoPullRemote) && from == "" {
		t, perr := pickConnectTarget(cfg.ConnectTargets, palette,
			"Select connect target", log)
		if perr != nil {
			if errors.Is(perr, ErrNoConnectTargets) {
				return "", "", "", ErrNoPullRemote
			}
			return "", "", "", perr
		}
		return t.SSHHost, t.RemotePath, t.Name, nil
	}
	return sshHost, remotePath, from, err
}

// pickDeployCommits opens the multi-select picker over the commit list and
// maps the chosen labels back to commits, preserving list order.
func pickDeployCommits(commits []deployCommit, palette theme.Palette) ([]deployCommit, error) {
	labels := make([]string, len(commits))
	byLabel := make(map[string]deployCommit, len(commits))
	for i, c := range commits {
		labels[i] = c.short() + "  " + c.subject
		byLabel[labels[i]] = c
	}
	picked, err := runFuzzyPicker("Select commits to deploy", labels, palette)
	if err != nil {
		return nil, err
	}
	out := make([]deployCommit, 0, len(picked))
	for _, lbl := range picked {
		if c, ok := byLabel[lbl]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// resolveCommitModule maps one commit to its module: the subject scheme
// first (`[Tag] module: title`, valid only when the module exists as an
// addon in the repo), then the diff fallback (the commit's changed paths
// must touch exactly one addon). Returns ("", "", reason) when unresolved.
func resolveCommitModule(ctx context.Context, root string, c deployCommit) (module, via, reason string) {
	if m := moduleFromSubject(root, c.subject); m != "" {
		return m, "subject", ""
	}
	paths, err := gitCommitPaths(ctx, root, c.sha)
	if err != nil {
		return "", "", "git show failed: " + err.Error()
	}
	mods := modulesFromPaths(root, paths)
	switch len(mods) {
	case 1:
		return mods[0], "diff", ""
	case 0:
		return "", "", "no addon module touched"
	default:
		return "", "", "touches several modules: " + strings.Join(mods, ", ")
	}
}

// moduleFromSubject extracts the module from the commit-subject scheme,
// valid only when it names a real addon in the repo (commits scoped to
// non-addon areas like `[FIX] docs: …` fall through to the diff).
func moduleFromSubject(root, subject string) string {
	m := deploySubjectRe.FindStringSubmatch(subject)
	if m == nil || !isAddonDir(root, m[1]) {
		return ""
	}
	return m[1]
}

// modulesFromPaths maps changed paths to the distinct top-level addon
// directories they live in, sorted.
func modulesFromPaths(root string, paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		top := strings.SplitN(filepath.ToSlash(p), "/", 2)[0]
		if top == "" || seen[top] || !isAddonDir(root, top) {
			continue
		}
		seen[top] = true
		out = append(out, top)
	}
	sort.Strings(out)
	return out
}

// isAddonDir reports whether <root>/<name> is an Odoo addon (has a
// __manifest__.py).
func isAddonDir(root, name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	_, err := os.Stat(filepath.Join(root, name, "__manifest__.py"))
	return err == nil
}

// splitInstallUpdate partitions the modules by their remote state: present
// as installed / to upgrade → update; anything else (absent, uninstalled,
// uninstallable) → install. Inputs are sorted, so outputs stay sorted.
func splitInstallUpdate(modules []string, states map[string]string) (install, update []string) {
	for _, m := range modules {
		switch states[m] {
		case "installed", "to upgrade":
			update = append(update, m)
		default:
			install = append(install, m)
		}
	}
	return install, update
}

// remoteModuleStates queries every module's state from the remote
// database (`ir_module_module`), over SSH inside the remote Postgres
// container. Read-only.
func remoteModuleStates(ctx context.Context, sshHost, remotePath string, t connectTarget, pgUser, db string) (map[string]string, error) {
	if pgUser == "" {
		pgUser = "odoo"
	}
	q := "SELECT name, state FROM ir_module_module"
	argv := odoo.Cmd{"psql", "-U", pgUser, "-d", db, "-At", "-c", q}
	out, err := runSSH(ctx, sshHost, remoteDBCmd(remotePath, t, argv), nil)
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, state, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || name == "" {
			continue
		}
		states[name] = state
	}
	return states, nil
}

// gitRecentCommits lists the last n commits of the repo's current branch,
// newest first.
func gitRecentCommits(ctx context.Context, root string, n int) ([]deployCommit, error) {
	out, err := gitOutput(ctx, root, "log", "-n", strconv.Itoa(n), "--pretty=format:%H%x1f%s")
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	var commits []deployCommit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sha, subject, ok := strings.Cut(line, "\x1f")
		if !ok || sha == "" {
			continue
		}
		commits = append(commits, deployCommit{sha: sha, subject: subject})
	}
	return commits, nil
}

// gitCommitPaths lists the paths changed by one commit (repo-relative).
// Merge commits yield no paths under diff-tree's defaults, which makes
// them unresolved — the right outcome for a deploy picker.
func gitCommitPaths(ctx context.Context, root, sha string) ([]string, error) {
	out, err := gitOutput(ctx, root, "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// gitOutput runs one git command against the repo at root, returning
// stdout; stderr is folded into the error (mirrors runSSH).
func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}
