package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
)

// ErrAliasUsage wraps alias errors that are the caller's fault (bad flags,
// an invalid alias name) so the REPL can map them to the usage exit code
// instead of a generic execution failure.
var ErrAliasUsage = errors.New("alias usage")

// AliasOpts configures an `alias` invocation. Root is the resolved project
// directory (what a bare `alias <name>` registers the name to).
type AliasOpts struct {
	Cfg  *config.Config
	Root string
	Args []string
}

// AliasResult is the outcome of an `alias` action, rendered by the REPL.
type AliasResult struct {
	Action  string                // list | set | remove | migrate
	Name    string                // set/remove: the alias name
	Path    string                // set: the path it points to
	Removed bool                  // remove: whether it existed
	Aliases []config.ProjectAlias // list
	Added   []string              // migrate
	Skipped []string              // migrate
}

// RunAlias manages the global project-alias registry: list, set (bare
// name → current project root), remove, or migrate from connect targets.
// It mutates global.toml directly and never needs a TTY.
func RunAlias(opts AliasOpts) (AliasResult, error) {
	var (
		list, migrate bool
		rmName        string
		setName       string
	)
	for i := 0; i < len(opts.Args); i++ {
		a := opts.Args[i]
		switch {
		case a == "--list":
			list = true
		case a == "--migrate":
			migrate = true
		case a == "--rm":
			if i+1 >= len(opts.Args) {
				return AliasResult{}, fmt.Errorf("%w: --rm requires an alias name", ErrAliasUsage)
			}
			i++
			rmName = opts.Args[i]
		case strings.HasPrefix(a, "--rm="):
			rmName = strings.TrimPrefix(a, "--rm=")
		case strings.HasPrefix(a, "-"):
			return AliasResult{}, fmt.Errorf("%w: unknown flag: %s", ErrAliasUsage, a)
		default:
			if setName != "" {
				return AliasResult{}, fmt.Errorf("%w: alias takes a single name: %s", ErrAliasUsage, a)
			}
			setName = a
		}
	}

	switch {
	case migrate:
		added, skipped, err := config.MigrateConnectAliases()
		if err != nil {
			return AliasResult{}, err
		}
		return AliasResult{Action: "migrate", Added: added, Skipped: skipped}, nil
	case rmName != "":
		removed, err := config.RemoveProjectAlias(rmName)
		if err != nil {
			return AliasResult{}, err
		}
		return AliasResult{Action: "remove", Name: rmName, Removed: removed}, nil
	case setName != "":
		if err := config.ValidateAliasName(setName); err != nil {
			return AliasResult{}, fmt.Errorf("%w: %v", ErrAliasUsage, err)
		}
		if err := config.SetProjectAlias(setName, opts.Root); err != nil {
			return AliasResult{}, err
		}
		return AliasResult{Action: "set", Name: setName, Path: opts.Root}, nil
	case list:
		fallthrough
	default:
		aliases, err := config.ProjectAliasList()
		if err != nil {
			return AliasResult{}, err
		}
		return AliasResult{Action: "list", Aliases: aliases}, nil
	}
}
