package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/odoo"
	"github.com/pascualchavez/echo/internal/theme"
)

// ErrProbeFailed means the probe ran and its Python raised; the result still
// carries the payload with the error details.
var ErrProbeFailed = errors.New("probe failed")

// probeLocalExec runs the probe shell in the local container; a seam so tests
// can script the output.
var probeLocalExec = docker.ExecWithStdinReader

const probeDefaultLimit = 20

// probeTailLines is how many trailing output lines a run without a result
// quotes in its error.
const probeTailLines = 3

// probeNamedExpr matches the `name=` prefix of a named `-c`; the caller still
// rejects `name==…`, which is a comparison.
var probeNamedExpr = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// ProbeOpts configures a `probe` run.
type ProbeOpts struct {
	Cfg  *config.Config
	Root string
	Args []string
	// Stdin is the script source of `probe -`.
	Stdin     io.Reader
	Palette   theme.Palette
	Log       func(level, sub, msg, db string, fields ...[2]string)
	StreamOut func(string)
}

// ProbeError is the Python exception a failed probe reports.
type ProbeError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Traceback string `json:"traceback"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

// ProbeResult is a decoded probe frame. Payload is what `--json` prints: the
// template's fields plus db, stage, target and duration_ms.
type ProbeResult struct {
	Payload  map[string]any
	OK       bool
	Kind     string
	Result   json.RawMessage
	Stdout   string
	Error    ProbeError
	DB       string
	Stage    string
	Target   string
	Duration time.Duration
}

// probeFlagForm maps each form-specific flag to the one form it applies to.
var probeFlagForm = map[string]string{
	"--domain": "read", "--fields": "read", "--limit": "read", "--order": "read", "--count": "read",
	"--reveal": "param",
	"--type":   "arch",
}

// parseProbeArgs turns probe's arguments into the request for the template.
// Every mistake is wrapped in ErrUsage.
func parseProbeArgs(args []string) (req odoo.ProbeRequest, from string, remote, jsonOut bool, err error) {
	usage := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, a...))
	}
	var positional []string
	used := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "--") {
			name, value, hasValue = a, "", false
		}
		switch name {
		case "--json":
			jsonOut = true
			continue
		case "--remote":
			remote = true
			continue
		case "--count":
			req.Count, used[name] = true, true
			continue
		case "--reveal":
			req.Reveal, used[name] = true, true
			continue
		case "-c", "--from", "-E", "--env", "--domain", "--fields", "--limit", "--order", "--type":
			// value-taking: read below
		default:
			if a != "-" && strings.HasPrefix(a, "-") {
				return req, "", false, false, usage("unknown flag %s", a)
			}
			positional = append(positional, a)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return req, "", false, false, usage("%s needs a value", name)
			}
			i++
			value = args[i]
		}
		used[name] = true
		switch name {
		case "-c":
			req.Exprs = append(req.Exprs, parseProbeExpr(value))
		case "--from":
			from = value
		case "-E", "--env":
			from = ReverbRef(value)
		case "--domain":
			var domain []any
			if json.Unmarshal([]byte(value), &domain) != nil {
				return req, "", false, false, usage("--domain must be a JSON array, got %s", value)
			}
			req.Domain = json.RawMessage(value)
		case "--fields":
			req.Fields = splitCSV(value)
		case "--limit":
			n, convErr := strconv.Atoi(value)
			if convErr != nil || n < 1 {
				return req, "", false, false, usage("--limit must be a positive integer, got %s", value)
			}
			req.Limit = n
		case "--order":
			req.Order = value
		case "--type":
			req.ViewType = value
		}
	}

	switch {
	case len(req.Exprs) > 0:
		if len(positional) > 0 {
			return req, "", false, false, usage("-c takes no positional arguments, got %s", positional[0])
		}
		req.Kind = "eval"
	case len(positional) == 0:
		return req, "", false, false, usage("probe needs -c <expr>, - or one of ref, read, fields, param, arch")
	default:
		req.Kind = positional[0]
		if req.Kind == "-" {
			req.Kind = "script"
		}
		if err := fillProbeForm(&req, positional[1:]); err != nil {
			return req, "", false, false, usage("%s", err)
		}
	}
	for flag := range used {
		if form, ok := probeFlagForm[flag]; ok && form != req.Kind {
			return req, "", false, false, usage("%s only applies to probe %s", flag, form)
		}
	}
	if req.Kind == "read" && req.Limit == 0 {
		req.Limit = probeDefaultLimit
	}
	return req, from, remote, jsonOut, nil
}

// parseProbeExpr splits `name=expr`; `a == b` and `a==b` stay expressions.
func parseProbeExpr(s string) odoo.ProbeExpr {
	loc := probeNamedExpr.FindStringIndex(s)
	if loc == nil || strings.HasPrefix(s[loc[1]:], "=") {
		return odoo.ProbeExpr{Expr: s}
	}
	return odoo.ProbeExpr{Name: s[:loc[1]-1], Expr: s[loc[1]:]}
}

// fillProbeForm checks a subcommand's positionals and stores them.
func fillProbeForm(req *odoo.ProbeRequest, rest []string) error {
	exactlyOne := func(what string) (string, error) {
		if len(rest) != 1 {
			return "", fmt.Errorf("probe %s takes one %s", req.Kind, what)
		}
		return rest[0], nil
	}
	var err error
	switch req.Kind {
	case "script":
		if len(rest) > 0 {
			return fmt.Errorf("probe - takes no other arguments, got %s", rest[0])
		}
	case "ref":
		req.XMLID, err = exactlyOne("<xmlid>")
	case "read":
		req.Model, err = exactlyOne("<model>")
	case "arch":
		req.XMLID, err = exactlyOne("<xmlid|model>")
	case "fields":
		if len(rest) == 0 {
			return errors.New("probe fields takes <model> [name...]")
		}
		req.Model, req.Names = rest[0], rest[1:]
	case "param":
		if len(rest) > 1 {
			return errors.New("probe param takes at most one key or prefix")
		}
		if len(rest) == 1 {
			req.Key = rest[0]
		}
	default:
		return fmt.Errorf("unknown probe form %s (want -c, -, ref, read, fields, param or arch)", req.Kind)
	}
	return err
}

// RunProbe evaluates a read-only probe against the local or remote Odoo
// database through `odoo shell`. Output lines other than the result frames
// go to StreamOut. A probe whose Python raised returns its result together
// with an error wrapping ErrProbeFailed.
func RunProbe(ctx context.Context, opts ProbeOpts) (ProbeResult, error) {
	req, from, remote, _, err := parseProbeArgs(opts.Args)
	if err != nil {
		return ProbeResult{}, err
	}
	if req.Kind == "script" {
		if !StdinPiped() {
			return ProbeResult{}, fmt.Errorf("%w: stdin is a terminal — pipe a script to probe -", ErrUsage)
		}
		body, err := io.ReadAll(opts.Stdin)
		if err != nil {
			return ProbeResult{}, fmt.Errorf("read script: %w", err)
		}
		req.Script = string(body)
	}

	var frames, tail []string
	onLine := func(line string) {
		if strings.HasPrefix(strings.TrimSpace(line), odoo.ProbeFramePrefix) {
			frames = append(frames, line)
			return
		}
		if strings.TrimSpace(line) != "" {
			tail = append(tail, line)
			if len(tail) > probeTailLines {
				tail = tail[1:]
			}
		}
		if opts.StreamOut != nil {
			opts.StreamOut(line)
		}
	}

	var res ProbeResult
	started := time.Now()
	var runErr error
	if from != "" || remote {
		rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, opts.Log)
		if err != nil {
			return res, err
		}
		res.DB, res.Stage, res.Target = rsc.target.dbName, rsc.target.stage, rsc.fromName
		req.Major = odoo.Major(rsc.target.odooVersion)
		script, err := odoo.ProbeScript(req)
		if err != nil {
			return res, err
		}
		remoteCmd := remoteContainerCmd(rsc.remotePath, rsc.target, odoo.ProbeShell(rsc.conn))
		runErr = runSSHStream(ctx, rsc.sshHost, remoteCmd, script, onLine)
	} else {
		if err := requireOdooConfig(opts.Cfg); err != nil {
			return res, err
		}
		res.DB, res.Stage = opts.Cfg.DBName, opts.Cfg.Stage
		req.Major = odoo.Major(opts.Cfg.OdooVersion)
		script, err := odoo.ProbeScript(req)
		if err != nil {
			return res, err
		}
		envVars := env.Load(opts.Root)
		conn := odoo.Conn{
			DB:       opts.Cfg.DBName,
			Host:     opts.Cfg.DBContainer,
			Port:     envVars["POSTGRES_PORT"],
			User:     envVars["POSTGRES_USER"],
			Password: envVars["POSTGRES_PASSWORD"],
		}
		if conn.Port == "" {
			conn.Port = "5432"
		}
		runErr = probeLocalExec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer,
			odoo.ProbeShell(conn), bytes.NewReader(script), onLine)
	}
	res.Duration = time.Since(started)

	// A complete frame is the answer even when the shell exited non-zero
	// after printing it.
	raw, decodeErr := odoo.DecodeProbeFrames(frames)
	if decodeErr != nil {
		cause := decodeErr
		if runErr != nil {
			cause = runErr
		}
		msg := "probe produced no result: " + cause.Error()
		if len(tail) > 0 {
			msg += "; last output: " + strings.Join(tail, " | ")
		}
		return res, errors.New(msg)
	}
	return decodeProbePayload(res, raw)
}

// decodeProbePayload fills res from the template's JSON payload and adds the
// fields only Echo knows.
func decodeProbePayload(res ProbeResult, raw json.RawMessage) (ProbeResult, error) {
	var frame struct {
		OK     bool            `json:"ok"`
		Kind   string          `json:"kind"`
		Result json.RawMessage `json:"result"`
		Stdout string          `json:"stdout"`
		Error  ProbeError      `json:"error"`
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &frame); err != nil {
		return res, fmt.Errorf("decode probe result: %w", err)
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return res, fmt.Errorf("decode probe result: %w", err)
	}
	res.OK, res.Kind, res.Result, res.Stdout, res.Error = frame.OK, frame.Kind, frame.Result, frame.Stdout, frame.Error

	res.Payload = make(map[string]any, len(fields)+4)
	for k, v := range fields {
		res.Payload[k] = v
	}
	res.Payload["db"] = res.DB
	res.Payload["stage"] = res.Stage
	res.Payload["target"] = nil
	if res.Target != "" {
		res.Payload["target"] = res.Target
	}
	res.Payload["duration_ms"] = res.Duration.Milliseconds()

	if !res.OK {
		return res, fmt.Errorf("%w: %s: %s", ErrProbeFailed, res.Error.Type, res.Error.Message)
	}
	return res, nil
}
