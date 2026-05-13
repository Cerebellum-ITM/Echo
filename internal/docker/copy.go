package docker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrServiceNotRunning indicates that `<compose> ps -q <service>` returned
// an empty ID — the compose service has no running container.
var ErrServiceNotRunning = errors.New("compose service is not running")

// ContainerID resolves a compose service to its docker container ID via
// `<compose> ps -q <service>`. Returns the trimmed first non-empty line.
func ContainerID(ctx context.Context, composeCmd, dir, service string) (string, error) {
	args := append(splitCompose(composeCmd), "ps", "-q", service)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve container id for %q: %w", service, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrServiceNotRunning, service)
}

// CopyFromContainer wraps `docker cp <container>:<src> <dst>` on the host.
func CopyFromContainer(ctx context.Context, container, src, dst string) error {
	return runDockerCp(ctx, container+":"+src, dst)
}

// CopyToContainer wraps `docker cp <src> <container>:<dst>` on the host.
func CopyToContainer(ctx context.Context, container, src, dst string) error {
	return runDockerCp(ctx, src, container+":"+dst)
}

func runDockerCp(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "docker", "cp", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("docker cp: %w", err)
		}
		return fmt.Errorf("docker cp: %s", msg)
	}
	return nil
}
