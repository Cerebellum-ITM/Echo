package docker

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// ExecInteractive runs `<compose> exec <container> <argv...>` under a
// host-side PTY so the in-container process still sees a TTY (line
// editing, colors, prompts all work) AND we can tee the combined
// stdout/stderr stream into an in-memory buffer for auto-copy on
// failure. The captured combined output is returned alongside the
// exit error.
//
// docker compose exec with the default `-t` allocates a remote PTY,
// which fuses the container's stdout and stderr into one stream — so
// tee'ing only stderr from our side would miss tracebacks. Wrapping
// the docker subprocess in our own PTY captures the full stream that
// reaches the user's terminal.
//
// When stdin is not a TTY (e.g. running echo under a pipe in tests),
// the function falls back to a plain pipe-tee. SIGINT is consumed in
// the parent so the subprocess (in the same process group) handles
// the interrupt and exits cleanly — same pattern as LogsFollow.
func ExecInteractive(ctx context.Context, composeCmd, dir, container string, argv []string) (string, bool, error) {
	var interrupted atomic.Bool
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()
	go func() {
		for range sigChan {
			interrupted.Store(true)
			// consume; the subprocess gets its own copy via the process group
		}
	}()

	full := append(splitCompose(composeCmd), "exec", container)
	full = append(full, argv...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Dir = dir

	// Non-TTY fallback: capture both streams via pipes. The subprocess
	// won't be interactive but at least the command runs and output is
	// captured.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		var buf bytes.Buffer
		cmd.Stdin = os.Stdin
		cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
		err := cmd.Run()
		return buf.String(), interrupted.Load(), err
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", interrupted.Load(), err
	}
	defer ptmx.Close()

	// Propagate terminal resize events into the PTY.
	winchChan := make(chan os.Signal, 1)
	signal.Notify(winchChan, syscall.SIGWINCH)
	defer signal.Stop(winchChan)
	go func() {
		for range winchChan {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	winchChan <- syscall.SIGWINCH

	// Put the host terminal in raw mode so keystrokes pass through
	// without local echo/cooking; restore on exit.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return "", interrupted.Load(), err
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Forward host stdin → PTY master, inspecting the byte stream for
	// the ETX (^C, 0x03) sentinel. In raw mode the kernel does NOT
	// translate Ctrl+C into SIGINT on the host — it lets the literal
	// byte through so the in-container TTY (which isn't in raw mode)
	// can handle it. That means signal.Notify never fires for us, so
	// we must spot the byte ourselves to know the user cancelled.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				for _, b := range buf[:n] {
					if b == 0x03 {
						interrupted.Store(true)
						break
					}
				}
				_, _ = ptmx.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Tee PTY master → stdout AND capture buffer. The container's
	// stdout+stderr are fused in this stream because docker compose
	// exec runs with -t by default.
	var buf bytes.Buffer
	_, _ = io.Copy(io.MultiWriter(os.Stdout, &buf), ptmx)

	err = cmd.Wait()
	return buf.String(), interrupted.Load(), err
}
