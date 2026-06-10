package repl

import (
	"context"
	"errors"

	"github.com/pascualchavez/echo/internal/clipboard"
	"github.com/pascualchavez/echo/internal/cmd"
)

// stripBuildFlag removes `--build` / `-b` from args, reporting whether one
// was present. The remaining tokens are returned in clean (v1: any leftover
// is a usage error, see runBuild).
func stripBuildFlag(args []string) (clean []string, build bool) {
	for _, a := range args {
		if a == "--build" || a == "-b" {
			build = true
			continue
		}
		clean = append(clean, a)
	}
	return clean, build
}

// buildFlagAliases lists flags to drop from a command's build-mode picker
// because they duplicate another offered flag. logs' `-c` is the short
// alias of `--copy` (both live in commandFlags["logs"]); offering both
// would invite a duplicate token in the composed line.
var buildFlagAliases = map[string][]string{"logs": {"-c"}}

// buildFlags returns the command's user-facing flags with aliased
// duplicates removed, preserving commandFlags (help) order.
func buildFlags(command string) []string {
	drop := buildFlagAliases[command]
	out := make([]string, 0, len(commandFlags[command]))
	for _, f := range commandFlags[command] {
		aliased := false
		for _, d := range drop {
			if f == d {
				aliased = true
				break
			}
		}
		if !aliased {
			out = append(out, f)
		}
	}
	return out
}

// runBuild is the session wrapper around cmd.RunBuild: it supplies the
// alias-filtered flag list, renders progress/result as echo.build lines,
// and acts on the result (run it, copy it, or finalize a cancel).
func (sess *session) runBuild(ctx context.Context, name string, rest []string) {
	// v1: --build must be the only argument.
	if len(rest) > 0 {
		sess.exitCode = exitUsage
		emitOdooLog("WARNING", "echo.build", "--build takes no other arguments",
			[]logField{{"cmd", name}}, sess.styles, sess.palette, sess.cfg.DBName)
		return
	}

	res, err := cmd.RunBuild(ctx, cmd.BuildOpts{
		Cfg:     sess.cfg,
		Root:    sess.projectDir,
		Command: name,
		Flags:   buildFlags(name),
		Palette: sess.palette,
		Warnf: func(msg string) {
			emitOdooLog("WARNING", "echo.build", msg, nil,
				sess.styles, sess.palette, sess.cfg.DBName)
		},
	})
	if err != nil {
		if errors.Is(err, cmd.ErrNothingToBuild) {
			sess.exitCode = exitUsage
			emitOdooLog("WARNING", "echo.build", err.Error(), nil,
				sess.styles, sess.palette, sess.cfg.DBName)
			return
		}
		// ErrNonInteractive → exit 2, cancel/abort → exit 3, other → exit 1;
		// finalize maps each via scriptExitCode.
		sess.finalize("build", 0, 0, err)
		return
	}

	switch res.Action {
	case cmd.BuildRun:
		line := cmd.BuildLine(name, res.Args)
		emitOdooLog("INFO", "echo.build", "running composed command",
			[]logField{{"cmd", line}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.dispatchParsed(ctx, name, res.Args)
	case cmd.BuildCopy:
		line := cmd.BuildLine(name, res.Args)
		if err := clipboard.WriteAll(line); err != nil {
			sess.exitCode = exitError
			emitOdooLog("ERROR", "echo.build.error", "could not copy to clipboard",
				[]logField{{"err", err.Error()}},
				sess.styles, sess.palette, sess.cfg.DBName)
			return
		}
		sess.exitCode = exitOK
		emitOdooLog("INFO", "echo.build", "command copied",
			[]logField{{"cmd", line}}, sess.styles, sess.palette, sess.cfg.DBName)
	case cmd.BuildCancel:
		sess.finalize("build", 0, 0, cmd.ErrCancelled)
	}
}

// isDispatchName reports whether name is a command the dispatch switch
// actually routes — build-mode interception only fires for these.
func isDispatchName(name string) bool {
	for _, n := range dispatchNames {
		if n == name {
			return true
		}
	}
	return false
}
