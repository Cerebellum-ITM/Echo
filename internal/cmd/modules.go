package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
	"github.com/pascualchavez/echo/internal/odoo"
)

type ModulesOpts struct {
	Cfg       *config.Config
	Root      string
	Args      []string
	StreamOut func(string)
}

var (
	ErrNoOdooContainer = errors.New("no Odoo container configured — run `init` first")
	ErrNoDB            = errors.New("no database configured — run `init` first")
	ErrNoModulesGiven  = errors.New("no module names given")
)

func RunInstall(ctx context.Context, opts ModulesOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	withDemo := false
	modules := make([]string, 0, len(opts.Args))
	for _, a := range opts.Args {
		switch a {
		case "--with-demo":
			withDemo = true
		default:
			modules = append(modules, a)
		}
	}
	if len(modules) == 0 {
		return ErrNoModulesGiven
	}
	return runOdoo(ctx, opts, odoo.Install(opts.Cfg.DBName, modules, withDemo))
}

func RunUpdate(ctx context.Context, opts ModulesOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	all := false
	modules := make([]string, 0, len(opts.Args))
	for _, a := range opts.Args {
		switch a {
		case "--all":
			all = true
		default:
			modules = append(modules, a)
		}
	}
	if all {
		return runOdoo(ctx, opts, odoo.UpdateAll(opts.Cfg.DBName))
	}
	if len(modules) == 0 {
		return ErrNoModulesGiven
	}
	return runOdoo(ctx, opts, odoo.Update(opts.Cfg.DBName, modules))
}

func RunUninstall(ctx context.Context, opts ModulesOpts) error {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return err
	}
	if len(opts.Args) == 0 {
		return ErrNoModulesGiven
	}
	return runOdoo(ctx, opts, odoo.Uninstall(opts.Cfg.DBName, opts.Args))
}

// RunModules lists local module directories (one level deep) under root,
// addons/, and custom/.
func RunModules(ctx context.Context, opts ModulesOpts) error {
	var found []string
	for _, sub := range []string{".", "addons", "custom"} {
		dir := filepath.Join(opts.Root, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			manifest := filepath.Join(dir, e.Name(), "__manifest__.py")
			if _, err := os.Stat(manifest); err == nil {
				found = append(found, e.Name())
			}
		}
	}
	if len(found) == 0 {
		if opts.StreamOut != nil {
			opts.StreamOut("(no modules found in ./, ./addons/, or ./custom/)")
		}
		return nil
	}
	sort.Strings(found)
	dedupe := make([]string, 0, len(found))
	last := ""
	for _, m := range found {
		if m != last {
			dedupe = append(dedupe, m)
			last = m
		}
	}
	for _, m := range dedupe {
		opts.StreamOut(m)
	}
	if opts.StreamOut != nil {
		opts.StreamOut(fmt.Sprintf("(%d modules)", len(dedupe)))
	}
	return nil
}

func runOdoo(ctx context.Context, opts ModulesOpts, argv odoo.Cmd) error {
	return docker.Exec(ctx, opts.Cfg.ComposeCmd, opts.Root, opts.Cfg.OdooContainer, argv, opts.StreamOut)
}

func requireOdooConfig(cfg *config.Config) error {
	if cfg.OdooContainer == "" {
		return ErrNoOdooContainer
	}
	if cfg.DBName == "" {
		return ErrNoDB
	}
	return nil
}
