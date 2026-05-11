# Unit 04: Docker Compose Commands

## Goal

Implement the REPL commands for the docker-compose lifecycle: `up`, `down`,
`restart`, `ps`, `logs`. Each command runs `<compose> <subcommand> [args]` in
the project root and streams output back to the user. The interactive
`logs -f` mode hands the terminal to the subprocess so Ctrl+C cancels the
follow loop and returns the user to the REPL prompt.

## Design

### Commands

| Command   | Action                                                | Notes                          |
|-----------|-------------------------------------------------------|--------------------------------|
| `up`      | `compose up -d [services...]`                         | Detached; returns when started |
| `down`    | `compose down [services...]`                          |                                |
| `restart` | `compose restart [services...]`                       |                                |
| `ps`      | `compose ps`                                          | Plain pass-through             |
| `logs`    | `compose logs [services...]`                          | Default: bounded               |
| `logs -f` | `compose logs -f [services...]`                       | TTY pass-through; Ctrl+C exits |

All take optional positional service names; default is all services.

### Output handling

Two execution modes:

1. **Streaming mode** (`up`, `down`, `restart`, `ps`, `logs` without `-f`)
   - Pipe stdout/stderr through scanners; each line goes to the REPL via a
     `Line` callback so styling is consistent with the rest of the CLI.
   - stdout → `Line{Kind: "out"}`, stderr → `Line{Kind: "dim"}`.

2. **Interactive mode** (`logs -f`)
   - Set `cmd.Stdin/Stdout/Stderr = os.Stdin/Stdout/Stderr` for native
     pass-through.
   - Install a SIGINT handler that consumes the signal in the parent (so the
     REPL does not die), letting the subprocess handle the interrupt and
     exit. Restore the previous handler on return.
   - Print a single `s.Dim` line on entry showing the command being run.

Both modes return when the subprocess exits. Non-zero exit prints an error
line with the subprocess's stderr message.

### Cancellation

The `logs -f` flow must:
- Forward Ctrl+C to the subprocess via the shared process group.
- Suppress the SIGINT from killing the REPL.
- Cleanly return when the subprocess exits, with the terminal in a usable
  state (no leftover raw mode, cursor visible).

## Implementation

### `internal/docker/compose.go` extensions

Add primitives that operate on `cfg.ComposeCmd` + `root`:

```go
// Down stops and removes containers.
func Down(ctx context.Context, composeCmd, dir string, services []string, onLine func(string)) error

// Restart restarts the given services (or all if empty).
func Restart(ctx context.Context, composeCmd, dir string, services []string, onLine func(string)) error

// PS streams `compose ps` output line by line.
func PS(ctx context.Context, composeCmd, dir string, onLine func(string)) error

// Logs streams `compose logs [services]` line by line. Bounded output.
func Logs(ctx context.Context, composeCmd, dir string, services []string, onLine func(string)) error

// LogsFollow runs `compose logs -f [services]` with full TTY pass-through.
// Returns when the subprocess exits. Ctrl+C is forwarded to the subprocess.
func LogsFollow(ctx context.Context, composeCmd, dir string, services []string) error
```

Internal helper `runStreamed(ctx, args, dir, onLine)` centralises the
pipe-scan-callback pattern used by Up/Down/Restart/PS/Logs.

### `internal/cmd/docker.go` (new)

One handler per command:

```go
func RunUp(ctx context.Context, opts DockerOpts) error
func RunDown(ctx context.Context, opts DockerOpts) error
func RunRestart(ctx context.Context, opts DockerOpts) error
func RunPS(ctx context.Context, opts DockerOpts) error
func RunLogs(ctx context.Context, opts DockerOpts) error  // dispatches follow vs bounded
```

```go
type DockerOpts struct {
    Cfg       *config.Config
    Root      string
    Args      []string          // CLI args after the command name
    StreamOut func(string)      // emits a styled line to the REPL
    StreamErr func(string)      // for stderr; nil falls back to StreamOut
}
```

`RunLogs` parses `-f` from `Args`. If present, calls `docker.LogsFollow`
which is interactive; otherwise calls `docker.Logs` with streaming.

### `internal/repl/repl.go` dispatch additions

```go
case "up":      sess.runDocker(ctx, "up", args, cmd.RunUp)
case "down":    sess.runDocker(ctx, "down", args, cmd.RunDown)
case "restart": sess.runDocker(ctx, "restart", args, cmd.RunRestart)
case "ps":      sess.runDocker(ctx, "ps", args, cmd.RunPS)
case "logs":    sess.runDocker(ctx, "logs", args, cmd.RunLogs)
```

A single `runDocker` helper builds `DockerOpts` with streaming callbacks
that produce styled `Line` output (kind `out` for stdout, `dim` for
stderr).

### Signal handling for interactive mode

In `docker.LogsFollow`:

```go
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, os.Interrupt)
defer signal.Stop(sigChan)
go func() { for range sigChan { /* consume */ } }()

cmd := exec.CommandContext(ctx, args[0], args[1:]...)
cmd.Dir = dir
cmd.Stdin = os.Stdin
cmd.Stdout = os.Stdout
cmd.Stderr = os.Stderr
return cmd.Run()
```

The subprocess and the REPL share the same process group, so SIGINT
arrives at both; the REPL consumes its copy, the subprocess exits.

## Dependencies

None beyond what is already in `go.mod`.

## Verify when done

- [ ] `go build ./...` passes.
- [ ] `up` starts containers; `compose ps` confirms they are running.
- [ ] `down` stops them; `ps` shows nothing.
- [ ] `restart odoo` restarts a single service.
- [ ] `ps` prints the standard compose table styled with the active theme.
- [ ] `logs odoo` prints the last batch of logs and returns.
- [ ] `logs -f odoo` streams in real time; Ctrl+C returns to the prompt
      without killing the CLI.
- [ ] After any docker command, the REPL prompt re-appears correctly with
      the line editor and history intact (no terminal mode leftovers).
- [ ] Non-zero exit codes from compose surface a clear error line.
