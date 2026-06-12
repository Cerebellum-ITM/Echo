package docker

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// Exec runs `<compose> exec -T <container> <argv...>` in dir, streaming
// combined stdout/stderr to onLine.
func Exec(ctx context.Context, composeCmd, dir, container string, argv []string, onLine func(string)) error {
	args := append([]string{"exec", "-T", container}, argv...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}

// ExecWithStdin runs `<compose> exec -T <container> <argv...>` with the file
// at stdinPath fed to the process's stdin, streaming combined stdout/stderr
// to onLine. It is the non-interactive counterpart used to run an Odoo shell
// script (`odoo shell … < script.py`): `-T` disables the TTY so the pipe is a
// plain stream. Mirrors the stdin-piping pattern of RestoreSQL.
func ExecWithStdin(ctx context.Context, composeCmd, dir, container string, argv []string, stdinPath string, onLine func(string)) error {
	f, err := os.Open(stdinPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return ExecWithStdinReader(ctx, composeCmd, dir, container, argv, f, onLine)
}

// ExecWithStdinReader is ExecWithStdin with the stdin source passed as a
// reader, so callers can feed the process from a pipe (`cat fix.py |
// echo shell`) instead of a file on disk.
func ExecWithStdinReader(ctx context.Context, composeCmd, dir, container string, argv []string, r io.Reader, onLine func(string)) error {
	full := append(SplitCompose(composeCmd), append([]string{"exec", "-T", container}, argv...)...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Dir = dir
	cmd.Stdin = r

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}
	streamLines(stdout, onLine)
	return cmd.Wait()
}
