package docker

import "context"

// Exec runs `<compose> exec -T <container> <argv...>` in dir, streaming
// combined stdout/stderr to onLine.
func Exec(ctx context.Context, composeCmd, dir, container string, argv []string, onLine func(string)) error {
	args := append([]string{"exec", "-T", container}, argv...)
	return runStreamed(ctx, composeCmd, dir, onLine, args...)
}
