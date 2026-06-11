package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// PSPublisher is one published port of a compose service container, as
// emitted under `Publishers` by `docker compose ps --format json`.
type PSPublisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}

// PSContainer is one row of `docker compose ps --format json`: a compose
// service's container with its state, human status, health, and ports.
type PSContainer struct {
	Name       string        `json:"Name"`
	Service    string        `json:"Service"`
	Image      string        `json:"Image"`
	State      string        `json:"State"`
	Status     string        `json:"Status"`
	Health     string        `json:"Health"`
	Publishers []PSPublisher `json:"Publishers"`
}

// Ports renders the published ports compactly as `pub→target/proto`,
// comma-separated, skipping unpublished (internal-only) ports. Empty when
// nothing is published.
func (c PSContainer) Ports() string {
	seen := make(map[string]bool)
	var parts []string
	for _, p := range c.Publishers {
		if p.PublishedPort == 0 {
			continue
		}
		s := strconv.Itoa(p.PublishedPort) + "→" + strconv.Itoa(p.TargetPort)
		if p.Protocol != "" && p.Protocol != "tcp" {
			s += "/" + p.Protocol
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// PSList runs `<compose> ps --format json` and parses the result into
// structured rows. Default scope (running/created containers) matches the
// bare `docker compose ps`. Returns nil with no error when nothing is up.
func PSList(ctx context.Context, composeCmd, dir string) ([]PSContainer, error) {
	args := append(SplitCompose(composeCmd), "ps", "--format", "json")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("compose ps: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("compose ps: %w", err)
	}
	return parsePSJSON(out)
}

// parsePSJSON decodes `docker compose ps --format json`, which emits either a
// single JSON array (newer compose) or newline-delimited JSON objects (older
// compose). Both forms are handled.
func parsePSJSON(b []byte) ([]PSContainer, error) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var arr []PSContainer
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("parse compose ps json: %w", err)
		}
		return arr, nil
	}
	var list []PSContainer
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var c PSContainer
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("parse compose ps json line: %w", err)
		}
		list = append(list, c)
	}
	return list, nil
}
