package docker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
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
	args := append(SplitCompose(composeCmd),
		"ps", "--status=running",
		"--format", "{{.Service}}\t{{.Name}}",
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return ParseContainers(string(out)), nil
}

// ParseContainers parses `compose ps --format '{{.Service}}\t{{.Name}}'`
// output.
func ParseContainers(out string) []Container {
	var containers []Container
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
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
	return containers
}

// UpCalls returns the compose invocations that start services without ever
// recreating the database container. A plain `up -d` recreates every container
// whose image tag now resolves to another image, so a tag re-pulled on the host
// by anything else (postgres:16 moving to a new Debian base) would put the
// database on a new image and break its collations.
//
// With no services it brings back every stopped service as it is, then lets
// the Odoo service alone be recreated if its definition changed. Named
// services start with --no-deps after the database, which comes up
// --no-recreate. db is "" when the database is not a compose service.
func UpCalls(services []string, odoo, db string) [][]string {
	if len(services) == 0 {
		calls := [][]string{{"up", "--detach", "--no-recreate"}}
		if odoo != "" {
			calls = append(calls, []string{"up", "--detach", "--no-deps", odoo})
		}
		return calls
	}
	var calls [][]string
	if db != "" {
		calls = append(calls, []string{"up", "--detach", "--no-recreate", db})
	}
	others := slices.DeleteFunc(slices.Clone(services), func(s string) bool { return s == db })
	if len(others) > 0 {
		calls = append(calls, append([]string{"up", "--detach", "--no-deps"}, others...))
	}
	return calls
}

// Up starts services through UpCalls and streams output line by line.
func Up(ctx context.Context, composeCmd, dir string, services []string, odoo, db string, onLine func(string)) error {
	for _, args := range UpCalls(services, odoo, db) {
		if err := runStreamed(ctx, composeCmd, dir, onLine, args...); err != nil {
			return err
		}
	}
	return nil
}

// DBImageDriftScript prints "<image>\t<running id>\t<current id>" for the
// database service's container: the image name it was created from, the
// image it runs, and the image that name resolves to on the host now. It
// prints nothing when the container does not exist. Run it in the project
// directory as `sh -c <script> sh <compose command> <db service>`; $1 stays
// unquoted so "docker compose" splits into its words.
const DBImageDriftScript = `cid=$($1 ps -a -q "$2" 2>/dev/null | head -n 1)
[ -n "$cid" ] || exit 0
image=$(docker inspect -f '{{.Config.Image}}' "$cid")
running=$(docker inspect -f '{{.Image}}' "$cid")
current=$(docker image inspect -f '{{.Id}}' "$image" 2>/dev/null)
printf '%s\t%s\t%s\n' "$image" "$running" "$current"
`

// DBImageDrift is a database container running an image other than the one
// its compose image name resolves to now.
type DBImageDrift struct {
	Image   string
	Running string
	Current string
}

// ParseDBImageDrift reads DBImageDriftScript's output. ok is false when the
// container runs the image its name resolves to, or there is nothing to
// compare.
func ParseDBImageDrift(out string) (drift DBImageDrift, ok bool) {
	parts := strings.Split(strings.TrimSpace(out), "\t")
	if len(parts) != 3 {
		return DBImageDrift{}, false
	}
	drift = DBImageDrift{Image: parts[0], Running: parts[1], Current: parts[2]}
	if drift.Running == "" || drift.Current == "" || drift.Running == drift.Current {
		return DBImageDrift{}, false
	}
	return drift, true
}

// ShortImageID trims an image ID to the 12 hex characters docker shows.
func ShortImageID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// CheckDBImageDrift runs DBImageDriftScript against the local project.
func CheckDBImageDrift(ctx context.Context, composeCmd, dir, db string) (DBImageDrift, bool) {
	cmd := exec.CommandContext(ctx, "sh", "-c", DBImageDriftScript, "sh", composeCmd, db)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return DBImageDrift{}, false
	}
	return ParseDBImageDrift(string(out))
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

// Stop runs `<compose> stop [services...]` and streams output line by
// line. Unlike Down it preserves the containers (they can be restarted
// with `start` or `up` without losing their writable layer).
func Stop(ctx context.Context, composeCmd, dir string, services []string, onLine func(string)) error {
	args := append([]string{"stop"}, services...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}

// PS runs `<compose> ps` and streams the table to onLine.
func PS(ctx context.Context, composeCmd, dir string, onLine func(string)) error {
	return runStreamed(ctx, composeCmd, dir, onLine, "ps")
}

// Logs runs `<compose> logs [--tail N] [services...]` (bounded) and streams
// output. tail is the line count limit; empty string means unbounded.
func Logs(ctx context.Context, composeCmd, dir, tail string, services []string, onLine func(string)) error {
	// --no-log-prefix drops compose's `<service>  | ` gutter so each line
	// reaches the colorizer raw, exactly like the Odoo logs `update` streams.
	args := []string{"logs", "--no-log-prefix"}
	if tail != "" {
		args = append(args, "--tail", tail)
	}
	args = append(args, services...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}

// LogsFollow runs `<compose> logs -f [--tail N] [services...]`, piping each
// line to onLine so the caller can colorize it through Echo's Odoo-log
// renderer (matching `up`/`down`/`update`) instead of docker's raw output.
// Ctrl+C cancels the follow: the SIGINT handler cancels the context, which
// kills the child and ends the scan; that stop is reported as a clean exit.
func LogsFollow(ctx context.Context, composeCmd, dir, tail string, services []string, onLine func(string)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()
	go func() {
		for range sigChan {
			cancel()
		}
	}()

	// --no-log-prefix: strip compose's `<service>  | ` gutter so each line
	// reaches emitStreamLine raw and colorizes like `up`/`update`.
	full := append(SplitCompose(composeCmd), "logs", "-f", "--no-log-prefix")
	if tail != "" {
		full = append(full, "--tail", tail)
	}
	full = append(full, services...)
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
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.Canceled {
			return nil // a Ctrl+C-initiated stop is clean, not a failure
		}
		return err
	}
	return nil
}

// runStreamed is the shared pattern: pipe combined stdout/stderr, scan
// line-by-line, deliver each line to onLine.
func runStreamed(ctx context.Context, composeCmd, dir string, onLine func(string), subcommand ...string) error {
	full := append(SplitCompose(composeCmd), subcommand...)
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

func SplitCompose(cmd string) []string {
	return strings.Fields(cmd)
}
