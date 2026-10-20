package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
	"github.com/pascualchavez/echo/internal/theme"
)

// ModstateOpts configures a `modstate` query.
type ModstateOpts struct {
	Cfg     *config.Config
	Root    string
	Args    []string
	Palette theme.Palette
	Log     func(level, sub, msg, db string, fields ...[2]string)
}

// ModstateResult is the outcome of a `modstate` query: the raw rows from
// ir_module_module plus the parsed output flags.
type ModstateResult struct {
	Rows []docker.ModuleStateRow
	JSON bool   // --json: caller emits a clean JSON array
	All  bool   // --all: include every state (default is installed-only)
	DB   string // the database that answered, local or remote
}

type modstateArgs struct {
	modules   []string
	from      string
	remote    bool
	all, json bool
}

func parseModstateArgs(args []string) (modstateArgs, error) {
	var p modstateArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			p.json = true
		case a == "--all":
			p.all = true
		case a == "--remote":
			p.remote = true
		case a == "--from":
			if i+1 >= len(args) {
				return p, fmt.Errorf("%w: --from needs a target", ErrUsage)
			}
			i++
			p.from = args[i]
		case strings.HasPrefix(a, "--from="):
			p.from = strings.TrimPrefix(a, "--from=")
		case strings.HasPrefix(a, "-"):
			return p, fmt.Errorf("%w: unknown flag: %s", ErrUsage, a)
		default:
			p.modules = append(p.modules, a)
		}
	}
	return p, nil
}

// RunModstate queries ir_module_module for the local database or, with
// --from/--remote, the target's, and returns every module's
// name/state/latest_version. By default only installed modules are returned;
// --all includes every state, and module names narrow the result to them. It
// performs no rendering and never needs a TTY unless --remote has to pick a
// target.
func RunModstate(ctx context.Context, opts ModstateOpts) (ModstateResult, error) {
	p, err := parseModstateArgs(opts.Args)
	if err != nil {
		return ModstateResult{}, err
	}
	res := ModstateResult{JSON: p.json, All: p.all, DB: opts.Cfg.DBName}

	var rows []docker.ModuleStateRow
	if p.from != "" || p.remote {
		rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, p.from, opts.Log)
		if err != nil {
			return res, err
		}
		res.DB = rsc.target.dbName
		states, versions, err := remoteModuleStates(ctx, rsc.sshHost, rsc.remotePath, rsc.target, rsc.conn.User, rsc.target.dbName)
		if err != nil {
			return res, fmt.Errorf("query ir_module_module: %w", err)
		}
		rows = remoteModuleRows(states, versions, !p.all)
	} else {
		if opts.Cfg.DBName == "" || opts.Cfg.DBContainer == "" {
			return res, ErrNoDB
		}
		user := env.Load(opts.Root)["POSTGRES_USER"]
		rows, err = docker.ModuleStates(ctx,
			opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, user, opts.Cfg.DBName, !p.all)
		if err != nil {
			return res, fmt.Errorf("query ir_module_module: %w", err)
		}
	}
	res.Rows = filterModuleRows(rows, p.modules, p.all)
	return res, nil
}

// remoteModuleRows turns the maps of remoteModuleStates into rows ordered by
// name. That query coalesces a NULL latest_version to empty, so an empty
// version is reported as NULL.
func remoteModuleRows(states, versions map[string]string, installedOnly bool) []docker.ModuleStateRow {
	rows := make([]docker.ModuleStateRow, 0, len(states))
	for name, state := range states {
		if installedOnly && state != "installed" {
			continue
		}
		v := versions[name]
		rows = append(rows, docker.ModuleStateRow{Name: name, State: state, Version: v, VersionNull: v == ""})
	}
	sortModuleRows(rows)
	return rows
}

// filterModuleRows keeps the rows of the named modules; no names keeps them
// all. With all, a named module the database has no row for is reported as
// uninstalled, so every name asked about gets an answer.
func filterModuleRows(rows []docker.ModuleStateRow, names []string, all bool) []docker.ModuleStateRow {
	if len(names) == 0 {
		return rows
	}
	missing := make(map[string]bool, len(names))
	for _, n := range names {
		missing[n] = true
	}
	var out []docker.ModuleStateRow
	for _, r := range rows {
		if missing[r.Name] {
			out = append(out, r)
			delete(missing, r.Name)
		}
	}
	if all {
		for n := range missing {
			out = append(out, docker.ModuleStateRow{Name: n, State: "uninstalled", VersionNull: true})
		}
		sortModuleRows(out)
	}
	return out
}

func sortModuleRows(rows []docker.ModuleStateRow) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
}
