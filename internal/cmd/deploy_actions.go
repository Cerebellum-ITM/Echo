package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

// actionEnv is the deploy context handed to every action's script as env
// vars, so a script needs no interpolation syntax of its own.
type actionEnv struct {
	stage      string
	db         string
	remotePath string
	modules    string // space-separated resolved update+install set
}

// deployActionError carries the failing action's identity so the deploy
// caller can route the failure (abort pre-stop vs. mark-failed post-deploy).
type deployActionError struct {
	name  string
	phase string
	err   error
}

func (e *deployActionError) Error() string {
	return fmt.Sprintf("deploy action %q (%s) failed: %v", e.name, e.phase, e.err)
}
func (e *deployActionError) Unwrap() error { return e.err }

// resolveDeployActions applies the wholesale precedence — `--no-actions` ›
// server list › local list › none — validates the winning list, and
// returns it with its source label ("server"/"local"/"") for the plan.
func resolveDeployActions(prof config.RemoteProfile, cfg *config.Config, noActions bool) ([]config.DeployAction, string, error) {
	if noActions {
		return nil, "", nil
	}
	var actions []config.DeployAction
	source := ""
	switch {
	case len(prof.DeployActions) > 0:
		actions, source = prof.DeployActions, "server"
	case cfg != nil && len(cfg.DeployActions) > 0:
		actions, source = cfg.DeployActions, "local"
	}
	if err := config.ValidateDeployActions(actions); err != nil {
		return nil, "", err
	}
	return actions, source, nil
}

// actionsForPhase filters an action list to one phase, preserving order.
func actionsForPhase(actions []config.DeployAction, phase string) []config.DeployAction {
	var out []config.DeployAction
	for _, a := range actions {
		if a.Phase == phase {
			out = append(out, a)
		}
	}
	return out
}

// runDeployActions runs every action of a phase in declared order, streaming
// each one's output and stopping (fail-fast) at the first failure with a
// typed error. env supplies the ECHO_* context vars.
func runDeployActions(ctx context.Context, rsc remoteShellContext, opts DeployOpts, actions []config.DeployAction, phase string, env actionEnv) error {
	for _, a := range actionsForPhase(actions, phase) {
		opts.log("INFO", "action", "running", rsc.prof.DBName,
			[2]string{"action", a.Name}, [2]string{"phase", a.Phase}, [2]string{"where", a.Where})
		start := time.Now()
		vars := actionEnvVars(env, a.Phase)
		var err error
		if a.Where == config.WhereRemote {
			err = actionRunRemote(ctx, rsc, a, vars, opts.StreamOut)
		} else {
			err = actionRunLocal(ctx, opts.Root, a, vars, opts.StreamOut)
		}
		if err != nil {
			opts.log("ERROR", "action", "action failed", rsc.prof.DBName,
				[2]string{"action", a.Name}, [2]string{"error", err.Error()})
			return &deployActionError{name: a.Name, phase: a.Phase, err: err}
		}
		opts.log("INFO", "action", "action done", rsc.prof.DBName,
			[2]string{"action", a.Name}, [2]string{"took", time.Since(start).Round(time.Millisecond).String()})
	}
	return nil
}

// actionEnvVars renders the deploy context into a KEY=value env slice.
func actionEnvVars(env actionEnv, phase string) []string {
	return []string{
		"ECHO_STAGE=" + env.stage,
		"ECHO_DB=" + env.db,
		"ECHO_REMOTE_PATH=" + env.remotePath,
		"ECHO_MODULES=" + env.modules,
		"ECHO_PHASE=" + phase,
	}
}

// actionRunRemote runs a remote action over SSH: the ECHO_* vars are
// exported, the shell cds into the project dir, and the script runs under
// `sh -c`. A package var so tests can stub the transport.
var actionRunRemote = func(ctx context.Context, rsc remoteShellContext, a config.DeployAction, env []string, out func(string)) error {
	cmd := envPrefix(env) + "cd " + shellQuote(rsc.remotePath) + " && sh -c " + shellQuote(a.Run)
	return runSSHStream(ctx, rsc.sshHost, cmd, nil, out)
}

// actionRunLocal runs a local action with `sh -c` from the project root,
// inheriting the process env plus the ECHO_* vars, streaming combined
// stdout+stderr line by line. A package var so tests can stub execution.
var actionRunLocal = func(ctx context.Context, root string, a config.DeployAction, env []string, out func(string)) error {
	c := exec.CommandContext(ctx, "sh", "-c", a.Run)
	c.Dir = root
	c.Env = append(os.Environ(), env...)
	return streamCombined(c, out)
}

// envPrefix builds a `KEY=value KEY2=value2 ` prefix (each value quoted) for
// a remote command. Empty slice → empty string.
func envPrefix(env []string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		b.WriteString(kv[:i])
		b.WriteByte('=')
		b.WriteString(shellQuote(kv[i+1:]))
		b.WriteByte(' ')
	}
	return b.String()
}

// streamCombined runs c with stdout+stderr merged into one line stream sent
// to out (nil-safe), returning the process's exit error.
func streamCombined(c *exec.Cmd, out func(string)) error {
	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw
	if err := c.Start(); err != nil {
		_ = pw.Close()
		return err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if out != nil {
				out(sc.Text())
			}
		}
	}()
	err := c.Wait()
	_ = pw.Close() // unblock the scanner once the process exits
	wg.Wait()
	return err
}
