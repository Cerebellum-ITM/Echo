package cmd

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

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
}

// ConnectResult carries the outcome of a successful `connect` run. The
// cookie is injected into a local Chrome via CDP, so there is no URL or
// session file to hand back — only what the REPL needs to log.
type ConnectResult struct {
	Login   string
	UID     int
	BaseURL string
	Remote  bool // true when the session was minted over SSH
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
// configured DB without requiring their password, then lands the
// session cookie in the dev's local browser by driving Chrome through
// the DevTools Protocol. Minting runs locally (`compose exec`) or over
// SSH against the remote host, depending on `[connect].ssh_host`.
//
// Selection is interactive (fuzzy picker) when no login is given,
// non-interactive when called as `connect <login>`.
func RunConnect(ctx context.Context, opts ConnectOpts) (ConnectResult, error) {
	var res ConnectResult
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return res, err
	}
	if opts.Cfg.ConnectSSHHost != "" && opts.Cfg.ConnectRemotePath == "" {
		return res, fmt.Errorf("[connect].remote_path is required when ssh_host is set")
	}
	if err := maybeConfirmProd(ShellOpts{
		Cfg: opts.Cfg, Root: opts.Root, Args: opts.Args, Palette: opts.Palette,
	}, "connect"); err != nil {
		return res, err
	}

	login, includeInactive := parseConnectArgs(opts.Args)

	users, err := listConnectUsers(ctx, opts, includeInactive)
	if err != nil {
		return res, fmt.Errorf("list users: %w", err)
	}

	target, err := pickConnectUser(users, login, opts.Palette)
	if err != nil {
		return res, err
	}

	minted, err := mintConnectSession(ctx, opts, target.ID)
	if err != nil {
		return res, fmt.Errorf("mint session: %w", err)
	}

	base := strings.TrimRight(minted.BaseURL, "/")
	if base == "" {
		return res, fmt.Errorf(
			"web.base.url is empty — set it in Odoo (Settings → Technical → Parameters → System Parameters) before using connect")
	}

	if err := landSessionCookie(ctx, opts.Cfg, base, minted.Sid); err != nil {
		return res, fmt.Errorf("open browser: %w", err)
	}

	return ConnectResult{
		Login:   minted.Login,
		UID:     minted.UID,
		BaseURL: base,
		Remote:  opts.Cfg.ConnectSSHHost != "",
	}, nil
}

func parseConnectArgs(args []string) (login string, includeInactive bool) {
	for _, a := range args {
		switch {
		case a == "--all":
			includeInactive = true
		case a == "--force":
			// handled by maybeConfirmProd
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

func listConnectUsers(ctx context.Context, opts ConnectOpts, includeInactive bool) ([]userRow, error) {
	envVars, err := connectDBEnvFor(ctx, opts)
	if err != nil {
		return nil, err
	}
	envVars["ECHO_DB"] = opts.Cfg.DBName
	if includeInactive {
		envVars["ECHO_INCLUDE_INACTIVE"] = "1"
	}
	out, err := execConnectScript(ctx, opts, connectListScript, envVars)
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
		return nil, fmt.Errorf("no users found in %q", opts.Cfg.DBName)
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

	picked, err := runSingleFuzzyPicker("Select user to impersonate", labels, palette)
	if err != nil {
		return userRow{}, err
	}
	for i, lbl := range labels {
		if lbl == picked {
			return users[i], nil
		}
	}
	return userRow{}, fmt.Errorf("picker returned unknown label %q", picked)
}

func mintConnectSession(ctx context.Context, opts ConnectOpts, uid int) (mintResult, error) {
	var res mintResult
	envVars, err := connectDBEnvFor(ctx, opts)
	if err != nil {
		return res, err
	}
	envVars["ECHO_DB"] = opts.Cfg.DBName
	envVars["ECHO_UID"] = fmt.Sprintf("%d", uid)
	out, err := execConnectScript(ctx, opts, connectMintScript, envVars)
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

// connectDBEnvFor returns the ECHO_DB_* env vars for either the local
// container (read from the local `.env`) or the remote one (read over
// SSH), depending on whether an SSH host is configured.
func connectDBEnvFor(ctx context.Context, opts ConnectOpts) (map[string]string, error) {
	if opts.Cfg.ConnectSSHHost == "" {
		return connectDBEnv(opts), nil
	}
	return connectDBEnvRemote(ctx, opts)
}

// connectDBEnv builds the env vars the embedded Python scripts read to
// reconstruct the same `--db_host/--db_port/--db_user/--db_password`
// flags the running Odoo uses. Without these, `parse_config([])` inside
// the container falls back to the Unix socket default, which does not
// exist when Postgres runs in a separate compose service.
func connectDBEnv(opts ConnectOpts) map[string]string {
	return dbEnvFromPostgres(opts.Cfg, env.Load(opts.Root))
}

// connectDBEnvRemote reads the remote project's `.env` over SSH and
// builds the same ECHO_DB_* map. If the file is unreadable it returns an
// empty map so the container's own ODOO_RC resolves the DB instead.
func connectDBEnvRemote(ctx context.Context, opts ConnectOpts) (map[string]string, error) {
	remoteEnv := shellQuote(opts.Cfg.ConnectRemotePath + "/.env")
	out, err := runSSH(ctx, opts.Cfg.ConnectSSHHost, "cat "+remoteEnv, nil)
	if err != nil {
		// Fall back to the container's own config rather than failing.
		return map[string]string{}, nil
	}
	return dbEnvFromPostgres(opts.Cfg, env.Parse(bytes.NewReader(out))), nil
}

// dbEnvFromPostgres maps POSTGRES_* dotenv values to the ECHO_DB_* env
// the embedded scripts consume. ECHO_DB_HOST is the compose service name
// (resolved on the docker network), not a POSTGRES_* value.
func dbEnvFromPostgres(cfg *config.Config, pg map[string]string) map[string]string {
	out := map[string]string{}
	if h := cfg.DBContainer; h != "" {
		out["ECHO_DB_HOST"] = h
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
// container, locally or over SSH depending on the connect config.
func execConnectScript(ctx context.Context, opts ConnectOpts, script []byte, env map[string]string) ([]byte, error) {
	if opts.Cfg.ConnectSSHHost != "" {
		return execPythonRemote(ctx, opts, script, env)
	}
	return execPythonInOdoo(ctx, opts, script, env)
}

// execPythonInOdoo runs `<compose> exec -T [-e K=V ...] <odoo> python3 -`
// with the embedded script piped through stdin. Returns combined stdout.
// Stderr is captured for error context but not returned on success.
func execPythonInOdoo(ctx context.Context, opts ConnectOpts, script []byte, env map[string]string) ([]byte, error) {
	argv := append(docker.SplitCompose(opts.Cfg.ComposeCmd), "exec", "-T")
	for k, v := range env {
		argv = append(argv, "-e", k+"="+v)
	}
	argv = append(argv, opts.Cfg.OdooContainer, "python3", "-")
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
func execPythonRemote(ctx context.Context, opts ConnectOpts, script []byte, env map[string]string) ([]byte, error) {
	compose := opts.Cfg.ConnectRemoteCompose
	if compose == "" {
		compose = opts.Cfg.ComposeCmd
	}
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(shellQuote(opts.Cfg.ConnectRemotePath))
	b.WriteString(" && ")
	b.WriteString(compose)
	b.WriteString(" exec -T")
	for k, v := range env {
		b.WriteString(" -e ")
		b.WriteString(shellQuote(k + "=" + v))
	}
	b.WriteString(" ")
	b.WriteString(shellQuote(opts.Cfg.OdooContainer))
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
