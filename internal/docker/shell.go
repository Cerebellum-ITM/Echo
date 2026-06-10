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
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// LineTransform restyles one complete output line (without its trailing
// newline) for display. It returns the replacement text and true when the
// line was recognized and restyled, or ("", false) to pass the original
// bytes through verbatim. ExecInteractive uses it to colorize the Odoo
// startup logs that a `shell` session prints raw through the PTY, so they
// match the rest of Echo's Odoo-styled output.
type LineTransform func(line string) (string, bool)

// partialFlushDelay bounds how long a not-yet-terminated line that *looks
// like* a forming log line (starts with a digit) is held before being
// written raw. Interactive content (prompts, keystroke echo) never starts
// with a digit and is flushed immediately, so this delay never adds input
// latency — it only covers a log line split across PTY reads.
const partialFlushDelay = 30 * time.Millisecond

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
//
// When transform is non-nil and stdin is a TTY, the PTY output is split
// into lines and each recognized log line is restyled before reaching the
// user's terminal (the capture buffer keeps the raw text, ANSI-free). A nil
// transform keeps the plain byte-for-byte tee.
func ExecInteractive(ctx context.Context, composeCmd, dir, container string, argv []string, transform LineTransform) (string, bool, error) {
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

	full := append(SplitCompose(composeCmd), "exec", container)
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

	// Dup stdin so we can close OUR copy of the fd after the subprocess
	// exits — that unblocks the goroutine's Read with EBADF without
	// touching the real os.Stdin the REPL needs once we return. Without
	// this, each shell session leaks one stdin-reader goroutine that
	// keeps stealing keystrokes from the REPL prompt afterwards.
	stdinFd, err := syscall.Dup(int(os.Stdin.Fd()))
	if err != nil {
		return "", interrupted.Load(), err
	}
	stdinDup := os.NewFile(uintptr(stdinFd), "stdin-dup")

	// Forward host stdin → PTY master, inspecting the byte stream for
	// the ETX (^C, 0x03) sentinel. In raw mode the kernel does NOT
	// translate Ctrl+C into SIGINT on the host — it lets the literal
	// byte through so the in-container TTY (which isn't in raw mode)
	// can handle it. That means signal.Notify never fires for us, so
	// we must spot the byte ourselves to know the user cancelled.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, rerr := stdinDup.Read(buf)
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
	// exec runs with -t by default. With a transform, lines are restyled
	// on the way to stdout while the capture keeps the raw, ANSI-free text.
	var buf bytes.Buffer
	if transform != nil {
		copyWithLineTransform(os.Stdout, &buf, ptmx, transform)
	} else {
		_, _ = io.Copy(io.MultiWriter(os.Stdout, &buf), ptmx)
	}

	err = cmd.Wait()
	// Close the dup *before* returning so the stdin-reader goroutine
	// exits cleanly (defers haven't run yet, so the terminal is still
	// in raw mode — the goroutine wakes up, gets EBADF, returns, and
	// only then does term.Restore put the TTY back into cooked mode).
	_ = stdinDup.Close()
	return buf.String(), interrupted.Load(), err
}

// copyWithLineTransform tees src → out (styled) and src → capture (raw),
// restyling each complete line through transform. It reads src in a
// goroutine so a short timer can flush a partial line that looks like a
// forming log line; interactive content (anything not starting with a
// digit) is flushed to out immediately, so keystroke echo never lags.
func copyWithLineTransform(out, capture io.Writer, src io.Reader, transform LineTransform) {
	type chunk struct {
		b   []byte
		err error
	}
	ch := make(chan chunk, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
				ch <- chunk{b: b}
			}
			if err != nil {
				ch <- chunk{err: err}
				return
			}
		}
	}()

	var pending []byte
	flushRaw := func() {
		if len(pending) > 0 {
			out.Write(pending)
			capture.Write(pending)
			pending = pending[:0]
		}
	}

	timer := time.NewTimer(partialFlushDelay)
	timer.Stop()
	for {
		select {
		case c := <-ch:
			if c.err != nil {
				pending = emitCompleteLines(out, capture, pending, transform)
				flushRaw()
				return
			}
			pending = append(pending, c.b...)
			pending = emitCompleteLines(out, capture, pending, transform)
			timer.Stop()
			// A leftover partial that could be a forming log line (starts with
			// a digit) waits briefly for its newline; anything else is
			// interactive and goes out now so the prompt/echo stays snappy.
			if len(pending) > 0 {
				if isDigit(pending[0]) {
					timer.Reset(partialFlushDelay)
				} else {
					flushRaw()
				}
			}
		case <-timer.C:
			flushRaw()
		}
	}
}

// emitCompleteLines drains every newline-terminated line from pending,
// writing the raw bytes to capture and the styled (or verbatim) line to
// out, and returns the unconsumed remainder.
func emitCompleteLines(out, capture io.Writer, pending []byte, transform LineTransform) []byte {
	for {
		i := bytes.IndexByte(pending, '\n')
		if i < 0 {
			return pending
		}
		lineWithNL := pending[:i+1]
		capture.Write(lineWithNL)

		content := pending[:i]
		ending := "\n"
		if len(content) > 0 && content[len(content)-1] == '\r' {
			content = content[:len(content)-1]
			ending = "\r\n"
		}
		if styled, ok := transform(string(content)); ok {
			io.WriteString(out, styled+ending)
		} else {
			out.Write(lineWithNL)
		}
		pending = pending[i+1:]
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
