package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pascualchavez/echo/internal/odoolint"
)

// ErrLintBlocked is returned when the pre-flight found markup the data
// loader will refuse in a module this run was about to deploy.
var ErrLintBlocked = fmt.Errorf("lint found blocking problems")

// deployLintPreflight runs Unit 109's lint over the modules this deploy
// selected and reports whether the run may continue.
//
// It is called after the selection resolves — that is what defines the
// scope — and before the first remote contact, so a block costs exactly
// nothing: no code copied, no branch advanced, no checkpoint written,
// nothing to roll back. That is the whole difference from discovering the
// same defect after the registry has already aborted.
//
// Scope is the selected modules, never the repo. A broken file in a module
// this deploy does not touch is not this deploy's problem, and blocking on
// it would just teach everyone to reach for --no-lint.
func deployLintPreflight(opts DeployOpts, p deployArgs, modules []string) error {
	if p.noLint {
		// A silent skip in a recorded run is how a default-on check
		// becomes decoration, so say it out loud.
		opts.log("WARNING", "lint", "pre-flight skipped", "",
			[2]string{"flag", "--no-lint"})
		return nil
	}

	res, missing, err := LintModules(opts.Cfg, opts.Root, modules)
	if err != nil {
		// The lint is a guard, not the job. A validator that fails to run
		// must not take the deploy down with it.
		opts.log("WARNING", "lint", "pre-flight could not run", "",
			[2]string{"err", err.Error()})
		return nil
	}

	// A selection may legitimately name a module that only exists on the
	// server (a rename in flight). Refusing to deploy over that would be
	// the linter inventing a failure mode of its own.
	if len(missing) > 0 {
		opts.log("WARNING", "lint", "modules not found locally, not linted", "",
			[2]string{"modules", strings.Join(missing, ",")})
	}

	// Coverage caveats before the verdict: they change what a clean
	// result below actually means.
	for _, pass := range res.SkippedPasses {
		opts.log("WARNING", "lint", "validator unavailable, pass skipped", "",
			[2]string{"pass", pass}, [2]string{"needs", "xmllint"})
	}
	if !res.GrammarExact && len(res.Modules) > 0 {
		opts.log("WARNING", "lint", "no grammar for the configured Odoo version", "",
			[2]string{"configured", opts.Cfg.OdooVersion}, [2]string{"used", res.GrammarMajor})
	}

	for _, f := range res.Findings {
		level := "ERROR"
		if f.Kind == odoolint.KindWarn {
			level = "WARNING"
		}
		opts.log(level, "lint", f.Message, "",
			[2]string{"file", RelPath(opts.Root, f.File)},
			[2]string{"line", strconv.Itoa(f.Line)},
			[2]string{"rule", f.Rule})
	}

	errs := res.Errors()
	if errs == 0 {
		opts.log("INFO", "lint", "pre-flight clean", "",
			[2]string{"modules", strings.Join(res.Modules, ",")},
			[2]string{"files", strconv.Itoa(res.Files)},
			[2]string{"warnings", strconv.Itoa(res.Warnings())})
		return nil
	}

	opts.log("ERROR", "lint", "pre-flight blocked the deploy", "",
		[2]string{"errors", strconv.Itoa(errs)},
		[2]string{"hint", "fix the files above, or deploy --no-lint"})
	return fmt.Errorf("%w: %d file(s) the data loader would refuse (use --no-lint to override)",
		ErrLintBlocked, errs)
}
