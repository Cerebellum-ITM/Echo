package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/env"
)

// ModstateOpts configures a `modstate` query.
type ModstateOpts struct {
	Cfg  *config.Config
	Root string
	Args []string
}

// ModstateResult is the outcome of a `modstate` query: the raw rows from
// ir_module_module plus the parsed output flags.
type ModstateResult struct {
	Rows []docker.ModuleStateRow
	JSON bool // --json: caller emits a clean JSON array
	All  bool // --all: include every state (default is installed-only)
}

// RunModstate queries ir_module_module for the active project's database
// and returns every module's name/state/latest_version. By default only
// installed modules are returned; --all includes every state. It performs
// no rendering — the REPL wrapper owns output routing (table vs JSON) so
// the table can reuse the session theme. It never runs `odoo` and never
// needs a TTY (no picker), so it is one-shot eligible and `-C`-aware.
func RunModstate(ctx context.Context, opts ModstateOpts) (ModstateResult, error) {
	if opts.Cfg.DBName == "" || opts.Cfg.DBContainer == "" {
		return ModstateResult{}, ErrNoDB
	}

	res := ModstateResult{}
	for _, a := range opts.Args {
		switch {
		case a == "--json":
			res.JSON = true
		case a == "--all":
			res.All = true
		case strings.HasPrefix(a, "-"):
			return ModstateResult{}, fmt.Errorf("unknown flag: %s", a)
		default:
			return ModstateResult{}, fmt.Errorf("modstate takes no arguments: %s", a)
		}
	}

	user := env.Load(opts.Root)["POSTGRES_USER"]
	rows, err := docker.ModuleStates(ctx,
		opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.DBContainer, user, opts.Cfg.DBName, !res.All)
	if err != nil {
		return ModstateResult{}, fmt.Errorf("query ir_module_module: %w", err)
	}
	res.Rows = rows
	return res, nil
}
