package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// sshStreamCommand builds the ssh invocation runSSHStream executes.
// A package-level hook so tests can substitute a local command for the
// real ssh binary.
var sshStreamCommand = func(ctx context.Context, host, remoteCmd string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", host, remoteCmd)
}

// sshToFileCommand builds the ssh invocation runSSHToFile executes. A
// package-level hook so tests can substitute a local command for the real
// ssh binary.
var sshToFileCommand = func(ctx context.Context, host, remoteCmd string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", host, remoteCmd)
}

// runSSHToFile runs a single remote command over SSH and streams its
// binary stdout straight into destPath (no buffering the whole payload in
// memory), reporting the running byte count through onProgress (throttled
// to ~1 MiB steps, with a final call). stderr is folded into the returned
// error. A non-zero exit — or any copy failure — removes the partial file
// so an interrupted pull never leaves a half-written dump behind.
func runSSHToFile(ctx context.Context, host, remoteCmd, destPath string, onProgress func(int64)) error {
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	cmd := sshToFileCommand(ctx, host, remoteCmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		f.Close()
		os.Remove(destPath)
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	fail := func(e error) error {
		f.Close()
		os.Remove(destPath)
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", e, msg)
		}
		return e
	}

	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	pw := &progressWriter{w: f, onProgress: onProgress}
	if _, err := io.Copy(pw, stdout); err != nil {
		_ = cmd.Wait()
		return fail(err)
	}
	if err := cmd.Wait(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(destPath)
		return err
	}
	if onProgress != nil {
		onProgress(pw.total) // final count
	}
	return nil
}

// progressWriter wraps a file, counting bytes and reporting the running
// total every ~1 MiB so the REPL can print a live size line.
type progressWriter struct {
	w          io.Writer
	total      int64
	reported   int64
	onProgress func(int64)
}

const progressStep = 1 << 20 // 1 MiB

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.total += int64(n)
	if p.onProgress != nil && p.total-p.reported >= progressStep {
		p.reported = p.total
		p.onProgress(p.total)
	}
	return n, err
}

// runSSHStream executes a single remote command over SSH like runSSH,
// but forwards stdout AND stderr to onLine line by line as they are
// produced instead of buffering the whole output — remote runs stream
// live through the same pipeline local subprocesses use (invariant 2).
// Both streams feed the same callback because Odoo and docker compose
// log to stderr. A non-zero exit returns an error carrying the last
// non-blank stderr line for context; nil onLine discards the output.
func runSSHStream(ctx context.Context, host, remoteCmd string, stdin []byte, onLine func(string)) error {
	cmd := sshStreamCommand(ctx, host, remoteCmd)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	var (
		mu          sync.Mutex
		lastErrLine string
	)
	emit := func(line string, fromStderr bool) {
		mu.Lock()
		defer mu.Unlock()
		if fromStderr && strings.TrimSpace(line) != "" {
			lastErrLine = line
		}
		if onLine != nil {
			onLine(line)
		}
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	scan := func(r io.Reader, fromStderr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		// Odoo tracebacks and SQL logs produce very long lines; the
		// default 64 KiB token limit would abort the stream mid-run.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			emit(sc.Text(), fromStderr)
		}
	}
	wg.Add(2)
	go scan(stdout, false)
	go scan(stderr, true)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if s := strings.TrimSpace(lastErrLine); s != "" {
			return fmt.Errorf("%w: %s", err, s)
		}
		return err
	}
	return nil
}

// remoteExecInteractive builds the SSH command for an INTERACTIVE remote
// compose exec: `cd <path> && <compose> exec <container> <argv...>` —
// without `-T`, so compose allocates a TTY inside the container. Pair it
// with `ssh -tt` so the remote side has a controlling terminal.
func remoteExecInteractive(remotePath, composeCmd, container string, argv []string) string {
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(shellQuote(remotePath))
	b.WriteString(" && ")
	b.WriteString(composeCmd)
	b.WriteString(" exec ")
	b.WriteString(shellQuote(container))
	for _, a := range argv {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	return b.String()
}

// remoteComposeCmd builds the SSH command that runs a compose
// subcommand at the remote project root: `cd <path> && <compose>
// <args...>`. Every arg is shell-quoted; the compose command is emitted
// raw so a two-word "docker compose" splits into its two tokens. The
// lifecycle counterpart of remoteExec (which targets a container).
func remoteComposeCmd(remotePath, composeCmd string, args ...string) string {
	var b strings.Builder
	b.WriteString("cd ")
	b.WriteString(shellQuote(remotePath))
	b.WriteString(" && ")
	b.WriteString(composeCmd)
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	return b.String()
}
