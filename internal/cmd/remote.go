package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
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
