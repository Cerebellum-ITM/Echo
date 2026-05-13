package docker

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
)

// ExecInteractive runs `<compose> exec <container> <argv...>` with
// stdin/stdout/stderr attached to the current TTY. SIGINT is consumed
// in the parent so the subprocess (in the same process group) handles
// the interrupt and exits cleanly — same pattern as LogsFollow.
func ExecInteractive(ctx context.Context, composeCmd, dir, container string, argv []string) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()
	go func() {
		for range sigChan {
			// consume; the subprocess gets its own copy via the process group
		}
	}()

	full := append(splitCompose(composeCmd), "exec", container)
	full = append(full, argv...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
