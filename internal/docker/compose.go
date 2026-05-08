package docker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
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

// ListContainers returns names of services currently running in the project.
func ListContainers(ctx context.Context, composeCmd, dir string) ([]string, error) {
	args := append(splitCompose(composeCmd), "ps", "--services", "--status=running")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var services []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			services = append(services, line)
		}
	}
	return services, nil
}

// Up runs `<compose> up -d` in dir, streaming combined stdout/stderr line
// by line through onLine. Returns when the subprocess exits.
func Up(ctx context.Context, composeCmd, dir string, onLine func(string)) error {
	args := append(splitCompose(composeCmd), "up", "-d")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
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
