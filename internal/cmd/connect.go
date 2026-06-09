package cmd

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/theme"
)

//go:embed scripts/connect_list_users.py
var connectListScript []byte

//go:embed scripts/connect_mint.py
var connectMintScript []byte

type ConnectOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	// Log, when set, receives Odoo-style progress events during a run so
	// the REPL can render them in its own log stream. `sub` is the logger
	// suffix under `echo.connect` ("" for the base logger, "cache" →
	// `echo.connect.cache`, "mint" → `echo.connect.mint`). Nil is a no-op,
	// so non-REPL callers (and tests) don't need to supply one.
	Log ConnectLogger
}

// ConnectLogger emits one Odoo-style progress line during RunConnect.
type ConnectLogger func(level, sub, msg, db string, fields ...[2]string)

// log is a nil-safe shortcut for emitting a progress event.
func (o ConnectOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// ConnectResult carries the outcome of a successful `connect` run. The
// cookie is injected into a local Chrome via CDP, so there is no URL or
// session file to hand back — only what the REPL needs to log.
type ConnectResult struct {
	Login   string
	UID     int
	DBName  string
	BaseURL string
	Remote  bool // true when the session was minted over SSH
	Reused  bool // true when a cached cookie was reused (no re-mint)
}

// connectTarget is the resolved container/db mapping the mint runs
// against. For a local Odoo it comes straight from the local config; for
// a remote one it is read from the server's own Echo profile over SSH,
// so the remote container/db names never have to be re-declared locally.
type connectTarget struct {
	remote        bool
	composeCmd    string
	odooContainer string
	dbContainer   string
	dbName        string
	stage         string
}

type userRow struct {
	ID     int    `json:"id"`
	Login  string `json:"login"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type mintResult struct {
	Sid         string `json:"sid"`
	SessionFile string `json:"session_file"`
	Login       string `json:"login"`
	UID         int    `json:"uid"`
	BaseURL     string `json:"base_url"`
}

// RunConnect mints an Odoo web session for an arbitrary user of the
// target DB without requiring their password, then lands the session
// cookie in the dev's local browser by driving Chrome through the
// DevTools Protocol. Minting runs locally (`compose exec`) or over SSH
// against the remote host configured in `[connect]`, reusing that host's
// own Echo profile for the container/db mapping.
//
// Selection is interactive (fuzzy picker) when no login is given,
// non-interactive when called as `connect <login>`.
func RunConnect(ctx context.Context, opts ConnectOpts) (ConnectResult, error) {
	var res ConnectResult

	target, err := resolveConnectTarget(ctx, opts)
	if err != nil {
		return res, err
	}
	if target.odooContainer == "" || target.dbName == "" {
		return res, fmt.Errorf("incomplete Odoo config (container/db) — run `init`%s",
			map[bool]string{true: " on the remote host", false: ""}[target.remote])
	}
	db := target.dbName
	opts.log("INFO", "", "target resolved", db,
		[2]string{"mode", connectMode(target.remote)},
		[2]string{"container", target.odooContainer})

	if err := maybeConfirmConnectProd(opts, target); err != nil {
		return res, err
	}

	login, includeInactive, fresh, newWindow := parseConnectArgs(opts.Args)
	windowMode := connectWindowMode(newWindow)

	cacheKey := connectCacheKey(opts, target)
	cache := config.LoadConnectSessions(cacheKey)
	opts.log("DEBUG", "cache", fmt.Sprintf("loaded %d cached session(s)", len(cache)), db)

	// Resolve which user to connect as. When a login is cached (or the
	// user picks one from the recent list) the selection carries the
	// cached session, so we skip both the user query and the mint.
	sel, err := resolveConnectSelection(ctx, opts, target, login, includeInactive, cache)
	if err != nil {
		return res, err
	}

	// Fast path: reuse the cached cookie if it's within the TTL and still
	// authenticates. `--fresh` forces a re-mint.
	if fresh && sel.cached != nil {
		opts.log("INFO", "cache", "--fresh: ignoring cached session", db, [2]string{"login", sel.login})
	}
	if !fresh && sel.cached != nil {
		if connectSessionExpired(*sel.cached) {
			opts.log("WARNING", "cache", "cached session past TTL — re-minting", db, [2]string{"login", sel.login})
		} else {
			opts.log("INFO", "cache", "validating cached session", db, [2]string{"login", sel.login})
			if probeSession(ctx, sel.cached.BaseURL, sel.cached.SID) {
				opts.log("INFO", "cache", "cached session valid — reusing cookie", db, [2]string{"login", sel.login})
				opts.log("INFO", "", "opening chrome", db,
					[2]string{"url", sel.cached.BaseURL + "/odoo"}, [2]string{"window", windowMode})
				if err := landSessionCookie(ctx, opts.Cfg, sel.cached.BaseURL, sel.cached.SID, newWindow); err != nil {
					return res, fmt.Errorf("open browser: %w", err)
				}
				return ConnectResult{
					Login:   sel.cached.Login,
					UID:     sel.cached.UID,
					DBName:  db,
					BaseURL: sel.cached.BaseURL,
					Remote:  target.remote,
					Reused:  true,
				}, nil
			}
			opts.log("WARNING", "cache", "cached session invalid — re-minting", db, [2]string{"login", sel.login})
		}
	}

	opts.log("INFO", "mint", "minting session", db,
		[2]string{"login", sel.login}, [2]string{"uid", fmt.Sprintf("%d", sel.uid)})
	minted, err := mintConnectSession(ctx, opts, target, sel.uid)
	if err != nil {
		return res, fmt.Errorf("mint session: %w", err)
	}
	opts.log("INFO", "mint", "session minted", db, [2]string{"login", minted.Login})

	base := strings.TrimRight(minted.BaseURL, "/")
	if base == "" {
		return res, fmt.Errorf(
			"web.base.url is empty — set it in Odoo (Settings → Technical → Parameters → System Parameters) before using connect")
	}
	// Prefer HTTPS on the same host when it's actually served, so the
	// session lands securely; falls back to web.base.url's own scheme.
	upgraded := preferHTTPS(ctx, base)
	if upgraded != base {
		opts.log("DEBUG", "", "upgraded to https", db, [2]string{"base", upgraded})
	}
	base = upgraded

	opts.log("INFO", "", "opening chrome", db,
		[2]string{"url", base + "/odoo"}, [2]string{"window", windowMode})
	if err := landSessionCookie(ctx, opts.Cfg, base, minted.Sid, newWindow); err != nil {
		return res, fmt.Errorf("open browser: %w", err)
	}

	// Cache the fresh session for next time (best-effort: a cache write
	// failure must not fail an otherwise successful connect).
	if err := config.SaveConnectSession(cacheKey, config.ConnectSession{
		Login:    minted.Login,
		UID:      minted.UID,
		SID:      minted.Sid,
		BaseURL:  base,
		MintedAt: time.Now(),
	}); err != nil {
		opts.log("DEBUG", "cache", "could not write session cache", db, [2]string{"err", err.Error()})
	} else {
		opts.log("DEBUG", "cache", "session cached", db, [2]string{"login", minted.Login})
	}

	return ConnectResult{
		Login:   minted.Login,
		UID:     minted.UID,
		DBName:  db,
		BaseURL: base,
		Remote:  target.remote,
	}, nil
}

// connectMode labels the resolved target for log output.
func connectMode(remote bool) string {
	if remote {
		return "remote"
	}
	return "local"
}

// connectSelection is the resolved user to connect as: login + uid, plus
// the cached session when one is available for reuse.
type connectSelection struct {
	login  string
	uid    int
	cached *config.ConnectSession
}

// resolveConnectSelection decides who to connect as while querying Odoo as
// little as possible:
//
//   - explicit `connect <login>` with a cache hit → reuse the cached uid,
//     no user query;
//   - explicit login with no cache → query users to resolve login→uid;
//   - interactive with cached sessions → offer the recent logins first,
//     querying the full list only if the user asks for it;
//   - interactive with no cache → the original query + picker.
func resolveConnectSelection(ctx context.Context, opts ConnectOpts, target connectTarget, login string, includeInactive bool, cache map[string]config.ConnectSession) (connectSelection, error) {
	db := target.dbName
	if login != "" {
		if e, ok := cache[login]; ok {
			opts.log("INFO", "cache", "cache hit — skipping user query", db,
				[2]string{"login", login}, [2]string{"uid", fmt.Sprintf("%d", e.UID)})
			entry := e
			return connectSelection{login: login, uid: e.UID, cached: &entry}, nil
		}
		users, err := opts.listUsersLogged(ctx, target, includeInactive)
		if err != nil {
			return connectSelection{}, err
		}
		u, err := pickConnectUser(users, login, opts.Palette)
		if err != nil {
			return connectSelection{}, err
		}
		return connectSelection{login: u.Login, uid: u.ID}, nil
	}

	if len(cache) > 0 {
		opts.log("INFO", "cache", fmt.Sprintf("%d recent session(s) — pick one or fetch all", len(cache)), db)
		chosen, fetchAll, err := pickRecentSessions(cache, opts.Palette)
		if err != nil {
			return connectSelection{}, err
		}
		if !fetchAll {
			opts.log("INFO", "cache", "reusing recent login", db, [2]string{"login", chosen})
			e := cache[chosen]
			return connectSelection{login: chosen, uid: e.UID, cached: &e}, nil
		}
	}

	users, err := opts.listUsersLogged(ctx, target, includeInactive)
	if err != nil {
		return connectSelection{}, err
	}
	u, err := pickConnectUser(users, "", opts.Palette)
	if err != nil {
		return connectSelection{}, err
	}
	return connectSelection{login: u.Login, uid: u.ID}, nil
}

// listUsersLogged wraps listConnectUsers with INFO log lines bracketing the
// `res.users` query, so the (sometimes slow) query is visible in the stream.
func (opts ConnectOpts) listUsersLogged(ctx context.Context, target connectTarget, includeInactive bool) ([]userRow, error) {
	opts.log("INFO", "", "querying users", target.dbName)
	users, err := listConnectUsers(ctx, opts, target, includeInactive)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	opts.log("INFO", "", fmt.Sprintf("%d user(s) found", len(users)), target.dbName)
	return users, nil
}

// resolveConnectTarget returns the container/db mapping to mint against.
// Local config when no SSH host is set; otherwise the server's own Echo
// profile fetched over SSH (located by hashing the remote project path).
func resolveConnectTarget(ctx context.Context, opts ConnectOpts) (connectTarget, error) {
	if opts.Cfg.ConnectSSHHost == "" {
		return connectTarget{
			composeCmd:    opts.Cfg.ComposeCmd,
			odooContainer: opts.Cfg.OdooContainer,
			dbContainer:   opts.Cfg.DBContainer,
			dbName:        opts.Cfg.DBName,
			stage:         opts.Cfg.Stage,
		}, nil
	}
	if opts.Cfg.ConnectRemotePath == "" {
		return connectTarget{}, fmt.Errorf("[connect].remote_path is required when ssh_host is set")
	}
	prof, err := fetchRemoteProfile(ctx, opts)
	if err != nil {
		return connectTarget{}, err
	}
	return connectTarget{
		remote:        true,
		composeCmd:    prof.ComposeCmd,
		odooContainer: prof.OdooContainer,
		dbContainer:   prof.DBContainer,
		dbName:        prof.DBName,
		stage:         prof.Stage,
	}, nil
}

// fetchRemoteProfile reads the remote host's Echo `global.toml` and the
// project profile for `remote_path` (keyed by the same path hash Echo
// uses locally) over SSH, then parses them into a RemoteProfile.
func fetchRemoteProfile(ctx context.Context, opts ConnectOpts) (config.RemoteProfile, error) {
	host := opts.Cfg.ConnectSSHHost
	key := config.ProjectKey(opts.Cfg.ConnectRemotePath)

	// global.toml is optional (compose cmd falls back to a default).
	globalData, _ := runSSH(ctx, host, "cat ~/.config/echo/global.toml", nil)

	projData, err := runSSH(ctx, host, "cat ~/.config/echo/projects/"+key+".toml", nil)
	if err != nil {
		return config.RemoteProfile{}, fmt.Errorf(
			"no Echo profile for %q on %s (expected projects/%s.toml) — run `init` there first: %w",
			opts.Cfg.ConnectRemotePath, host, key, err)
	}
	return config.ParseRemoteProfile(globalData, projData), nil
}

// maybeConfirmConnectProd replicates maybeConfirmProd but keys off the
// resolved target's stage (which, for a remote run, comes from the
// server's profile, not the local config).
func maybeConfirmConnectProd(opts ConnectOpts, target connectTarget) error {
	if !strings.EqualFold(target.stage, "prod") {
		return nil
	}
	for _, a := range opts.Args {
		if a == "--force" {
			return nil
		}
	}
	return confirmProd(opts.Palette, "connect", target.dbName)
}

func parseConnectArgs(args []string) (login string, includeInactive, fresh, newWindow bool) {
	for _, a := range args {
		switch {
		case a == "--all":
			includeInactive = true
		case a == "--fresh":
			fresh = true
		case a == "--new-window":
			newWindow = true
		case a == "--force":
			// handled by maybeConfirmConnectProd
		case strings.HasPrefix(a, "-"):
			// unknown flag, ignore for forward-compat
		default:
			if login == "" {
				login = a
			}
		}
	}
	return
}

// connectWindowMode labels how the browser window was opened, for logs.
func connectWindowMode(newWindow bool) string {
	if newWindow {
		return "incognito"
	}
	return "tab"
}

func listConnectUsers(ctx context.Context, opts ConnectOpts, target connectTarget, includeInactive bool) ([]userRow, error) {
	envVars, err := connectDBEnvFor(ctx, opts, target)
	if err != nil {
		return nil, err
	}
	envVars["ECHO_DB"] = target.dbName
	if includeInactive {
		envVars["ECHO_INCLUDE_INACTIVE"] = "1"
	}
	out, err := execConnectScript(ctx, opts, target, connectListScript, envVars)
	if err != nil {
		return nil, err
	}
	payload := lastNonEmptyLine(out)
	if payload == "" {
		return nil, fmt.Errorf("empty output from list_users")
	}
	var users []userRow
	if err := json.Unmarshal([]byte(payload), &users); err != nil {
		return nil, fmt.Errorf("decode users: %w (raw=%q)", err, payload)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("no users found in %q", target.dbName)
	}
	return users, nil
}

func pickConnectUser(users []userRow, login string, palette theme.Palette) (userRow, error) {
	if login != "" {
		for _, u := range users {
			if u.Login == login {
				return u, nil
			}
		}
		return userRow{}, fmt.Errorf("no user with login %q", login)
	}

	labels := make([]string, len(users))
	maxLogin := 0
	for _, u := range users {
		if len(u.Login) > maxLogin {
			maxLogin = len(u.Login)
		}
	}
	for i, u := range users {
		flag := " "
		if !u.Active {
			flag = "!"
		}
		labels[i] = fmt.Sprintf("%s %-*s  %s", flag, maxLogin, u.Login, u.Name)
	}

	chosen, err := runSingleFuzzyPicker("Select user to impersonate", labels, palette)
	if err != nil {
		return userRow{}, err
	}
	for i, lbl := range labels {
		if lbl == chosen {
			return users[i], nil
		}
	}
	return userRow{}, fmt.Errorf("picker returned unknown label %q", chosen)
}

func mintConnectSession(ctx context.Context, opts ConnectOpts, target connectTarget, uid int) (mintResult, error) {
	var res mintResult
	envVars, err := connectDBEnvFor(ctx, opts, target)
	if err != nil {
		return res, err
	}
	envVars["ECHO_DB"] = target.dbName
	envVars["ECHO_UID"] = fmt.Sprintf("%d", uid)
	out, err := execConnectScript(ctx, opts, target, connectMintScript, envVars)
	if err != nil {
		return res, err
	}
	payload := lastNonEmptyLine(out)
	if payload == "" {
		return res, fmt.Errorf("empty output from mint")
	}
	if err := json.Unmarshal([]byte(payload), &res); err != nil {
		return res, fmt.Errorf("decode mint result: %w (raw=%q)", err, payload)
	}
	return res, nil
}

// connectDBEnvFor returns the ECHO_DB_* env vars the embedded scripts use
// to reach Postgres, sourced from the local `.env` (local mode) or the
// remote one read over SSH (remote mode).
func connectDBEnvFor(ctx context.Context, opts ConnectOpts, target connectTarget) (map[string]string, error) {
	if !target.remote {
		return dbEnvFromPostgres(target.dbContainer, env.Load(opts.Root)), nil
	}
	remoteEnv := shellQuote(opts.Cfg.ConnectRemotePath + "/.env")
	out, err := runSSH(ctx, opts.Cfg.ConnectSSHHost, "cat "+remoteEnv, nil)
	if err != nil {
		// Fall back to the container's own ODOO_RC rather than failing.
		return map[string]string{}, nil
	}
	return dbEnvFromPostgres(target.dbContainer, env.Parse(bytes.NewReader(out))), nil
}

// dbEnvFromPostgres maps POSTGRES_* dotenv values to the ECHO_DB_* env
// the embedded scripts consume. ECHO_DB_HOST is the compose service name
// (resolved on the docker network), not a POSTGRES_* value.
func dbEnvFromPostgres(dbContainer string, pg map[string]string) map[string]string {
	out := map[string]string{}
	if dbContainer != "" {
		out["ECHO_DB_HOST"] = dbContainer
	}
	if p := pg["POSTGRES_PORT"]; p != "" {
		out["ECHO_DB_PORT"] = p
	} else {
		out["ECHO_DB_PORT"] = "5432"
	}
	if u := pg["POSTGRES_USER"]; u != "" {
		out["ECHO_DB_USER"] = u
	}
	if pw := pg["POSTGRES_PASSWORD"]; pw != "" {
		out["ECHO_DB_PASSWORD"] = pw
	}
	return out
}

// execConnectScript runs the embedded Python script inside the Odoo
// container, locally or over SSH depending on the target.
func execConnectScript(ctx context.Context, opts ConnectOpts, target connectTarget, script []byte, env map[string]string) ([]byte, error) {
	if target.remote {
		return execPythonRemote(ctx, opts, target, script, env)
	}
	return execPythonInOdoo(ctx, opts, target, script, env)
}

// execPythonInOdoo runs `<compose> exec -T [-e K=V ...] <odoo> python3 -`
// in the local project dir with the script piped through stdin.
func execPythonInOdoo(ctx context.Context, opts ConnectOpts, target connectTarget, script []byte, env map[string]string) ([]byte, error) {
	argv := append(docker.SplitCompose(target.composeCmd), "exec", "-T")
	for k, v := range env {
		argv = append(argv, "-e", k+"="+v)
	}
	argv = append(argv, target.odooContainer, "python3", "-")
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = opts.Root
	cmd.Stdin = bytes.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return out, nil
}

// execPythonRemote runs the same script inside the Odoo container on the
// remote host over SSH:
//
//	ssh -o BatchMode=yes <host> 'cd <remote_path> && <compose> exec -T [-e K=V ...] <odoo> python3 -'
//
// The script is piped through ssh's stdin. BatchMode makes a missing key
// fail fast instead of hanging on a password prompt.
func execPythonRemote(ctx context.Context, opts ConnectOpts, target connectTarget, script []byte, env map[string]string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(shellQuote(opts.Cfg.ConnectRemotePath))
	b.WriteString(" && ")
	b.WriteString(target.composeCmd)
	b.WriteString(" exec -T")
	for k, v := range env {
		b.WriteString(" -e ")
		b.WriteString(shellQuote(k + "=" + v))
	}
	b.WriteString(" ")
	b.WriteString(shellQuote(target.odooContainer))
	b.WriteString(" python3 -")
	return runSSH(ctx, opts.Cfg.ConnectSSHHost, b.String(), script)
}

// runSSH executes a single remote command over SSH, optionally piping
// stdin. Returns combined stdout; stderr is folded into the error.
func runSSH(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", host, remoteCmd)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return out, nil
}

// shellQuote wraps s in single quotes for safe interpolation into a
// remote shell command, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// lastNonEmptyLine returns the last non-blank line of out, trimmed.
// Used because Odoo may emit log lines before our JSON payload despite
// the logging.setLevel(ERROR) in the scripts.
func lastNonEmptyLine(out []byte) string {
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if s != "" {
			return s
		}
	}
	return ""
}
