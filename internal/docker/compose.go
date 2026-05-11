package docker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
)

var ErrComposeNotFound = errors.New("neither 'docker compose' nor 'docker-compose' found in PATH")

// DetectCompose returns the working compose command flavor.
func DetectCompose(ctx context.Context) (string, error) {
	if err := exec.CommandContext(ctx, "docker", "compose", "version").Run(); err == nil {
		return "docker compose", nil
	}
	if err := exec.CommandContext(ctx, "docker-compose", "--version").Run(); err == nil {
		return "docker-compose", nil
	}
	return "", ErrComposeNotFound
}

// Container holds both the docker container name and its compose service.
type Container struct {
	Service string
	Name    string
}

// Label returns "name - service" for display.
func (c Container) Label() string { return c.Name + " - " + c.Service }

// ListContainers returns running services with both their compose service
// name and the actual container name.
func ListContainers(ctx context.Context, composeCmd, dir string) ([]Container, error) {
	args := append(splitCompose(composeCmd),
		"ps", "--status=running",
		"--format", "{{.Service}}\t{{.Name}}",
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var containers []Container
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		containers = append(containers, Container{
			Service: strings.TrimSpace(parts[0]),
			Name:    strings.TrimSpace(parts[1]),
		})
	}
	return containers, nil
}

// Up runs `<compose> up -d [services...]` and streams output line by line.
func Up(ctx context.Context, composeCmd, dir string, services []string, onLine func(string)) error {
	args := append([]string{"up", "-d"}, services...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}

// Down runs `<compose> down [services...]` and streams output line by line.
func Down(ctx context.Context, composeCmd, dir string, services []string, onLine func(string)) error {
	args := append([]string{"down"}, services...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}

// Restart runs `<compose> restart [services...]` and streams output.
func Restart(ctx context.Context, composeCmd, dir string, services []string, onLine func(string)) error {
	args := append([]string{"restart"}, services...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}

// PS runs `<compose> ps` and streams the table to onLine.
func PS(ctx context.Context, composeCmd, dir string, onLine func(string)) error {
	return runStreamed(ctx, composeCmd, dir, onLine, "ps")
}

// Logs runs `<compose> logs [--tail N] [services...]` (bounded) and streams
// output. tail is the line count limit; empty string means unbounded.
func Logs(ctx context.Context, composeCmd, dir, tail string, services []string, onLine func(string)) error {
	args := []string{"logs"}
	if tail != "" {
		args = append(args, "--tail", tail)
	}
	args = append(args, services...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}

// LogsFollow runs `<compose> logs -f [--tail N] [services...]` with full TTY
// pass-through. SIGINT is consumed in the parent so the subprocess (in the
// same process group) handles the interrupt and exits cleanly.
func LogsFollow(ctx context.Context, composeCmd, dir, tail string, services []string) error {
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

	full := append(splitCompose(composeCmd), "logs", "-f")
	if tail != "" {
		full = append(full, "--tail", tail)
	}
	full = append(full, services...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runStreamed is the shared pattern: pipe combined stdout/stderr, scan
// line-by-line, deliver each line to onLine.
func runStreamed(ctx context.Context, composeCmd, dir string, onLine func(string), subcommand ...string) error {
	full := append(splitCompose(composeCmd), subcommand...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Dir = dir

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

func streamLines(r io.Reader, onLine func(string)) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		}
	}
}

func splitCompose(cmd string) []string {
	return strings.Fields(cmd)
}
