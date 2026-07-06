# Unit 75: remote `test` — `--from <target>` / `--remote`

## Goal

Extend the `test` command (Unit 11) so it can run the Odoo test suite
against a **remote** Odoo host, reusing the exact SSH transport that
`deploy` / `shell-run` / remote `logs`+`restart` (Units 60/62/72)
already use. `test <mod...> --from <target>` and `test <mod...> --remote`
resolve the same connect target, build `odoo.Test(...)` argv, wrap it in
`remoteContainerCmd`, and stream the output line by line through
`runSSHStream` into Echo's Odoo-styled renderer — the remote analog of
the local `test`. Without a remote flag, `test` behaves **exactly as
today** (local container). This unit adds the remote branch only.

## Design

**One picker, two transports.** `test` keeps its full local behavior.
The remote branch is opt-in via the shared `--from <name>` /
`--from=<name>` / `--remote` convention (`remoteFlagsIn`, `shell_remote.go`):
`--from <name>` names a global connect target (implies remote); bare
`--remote` uses the resolution chain without a name (this dir's `link`
binding → global-targets fallback). This is byte-for-byte the same
flag surface as remote `shell-run`/`logs`/`restart`, so there is nothing
new to learn.

**The remote connection comes from the REMOTE profile, never local
config.** Resolution is delegated to `resolveRemoteShell(...)`, which
resolves the target, fetches the server's Echo profile (`composeCmd`,
`OdooContainer`, `DBContainer`, `DBName`, `stage`, `odooVersion`), and
assembles the `odoo.Conn` (remote DB container as host, `POSTGRES_*`
pulled from the remote env). `odoo.Test` then emits `-d/--db_host/...`
against the remote Postgres exactly as it does locally — the builder is
transport-agnostic and needs **no changes**.

**Transport = the deploy/shell-run primitives.** The remote run goes
through `remoteContainerCmd(rsc.remotePath, rsc.target, odoo.Test(rsc.conn,
opts))` (→ `cd <path> && <compose> exec -T <odoo> odoo --no-http
--http-port=8189 --test-tags … --stop-after-init --log-level=test`) piped
to `runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut)`. The
`-T` (no TTY) exec means Odoo emits plain, un-ANSI logs that
`emitStreamLine` / `logColorer` colorize identically to a local test
(Units 08/20) — same streaming invariant as `deploy` (stream, never
buffer-and-dump).

**Module selection.** Modules are resolved **before** branching, so the
picker path is shared: `test sale --from prod` uses the positionals;
`test --from prod` (no modules) opens the same local fuzzy picker
(title `"Modules to test"`), which lists the modules of the linked local
checkout — the code that corresponds to the remote deployment (the
`link` binding ties this dir to the target, same assumption `deploy`
makes when it maps local commits to remote modules). `--tags <spec>`
still overrides the auto `/<mod>` filter and forwards verbatim.

**Prod gate — stricter on remote, like every other remote verb.** A
remote test shares the target's live Postgres. Even with
`--stop-after-init` on port 8189, the suite writes to that DB (test
transactions roll back, but `--update` genuinely upgrades the modules
first). So the remote branch gates on the **remote** profile's stage via
`confirmRemoteProd(palette, "test", rsc, args)`: a `prod` target prompts
a red confirm, `--force` bypasses, non-TTY fails closed. The local
`test` path stays ungated, unchanged (Unit 11 decision). `--update`
against a remote is called out in help as the module-reloading, riskier
mode.

**Flag stripping.** `--from <val>` / `--from=<val>` / `--remote` are
consumed off `opts.Args` before the positional/`--tags`/`--update` parse,
so the value token after a bare `--from` is never mistaken for a module
name. (Today's parse loop silently drops any `-`-prefixed token but would
capture the bare `prod` after `--from` as a module — this unit closes
that gap.)

### Argv produced (remote)

| Invocation | remote argv (after conn-flags) |
|---|---|
| `test sale --from prod` | `--no-http --http-port=8189 --test-tags /sale --stop-after-init --log-level=test` |
| `test sale account --remote` | `--no-http --http-port=8189 --test-tags /sale,/account --stop-after-init --log-level=test` |
| `test sale --from prod --update` | `--no-http --http-port=8189 --test-tags /sale -u sale --stop-after-init --log-level=test` |
| `test sale --from prod --tags :TestX.test_y` | `--no-http --http-port=8189 --test-tags :TestX.test_y --stop-after-init --log-level=test` |

Identical to the local Unit 11 argv — only the transport changes. Flags
are the same across Odoo 17/18/19 (no version branching).

## Implementation

### `internal/cmd/test_remote.go` — new file

New helper mirroring `runShellScriptRemote` (`shellrun.go`):

```go
// runTestRemote runs the resolved test suite in a REMOTE Odoo container
// over SSH: `ssh <host> 'cd <path> && <compose> exec -T <odoo> odoo …'`,
// streamed live through runSSHStream — the remote analog of runOdoo's
// docker.Exec path, sharing the transport with deploy/shell-run.
func runTestRemote(ctx context.Context, opts ModulesOpts, from string, o odoo.TestOpts) error {
	rsc, err := resolveRemoteShell(ctx, opts.Cfg, opts.Palette, opts.Root, from, nil)
	if err != nil {
		return err
	}
	if err := confirmRemoteProd(opts.Palette, "test", rsc, opts.Args); err != nil {
		return err
	}
	remoteCmd := remoteContainerCmd(rsc.remotePath, rsc.target, odoo.Test(rsc.conn, o))
	return runSSHStream(ctx, rsc.sshHost, remoteCmd, nil, opts.StreamOut)
}
```

`log` is passed `nil` (as remote `shell` does at `shell.go:99`); the
REPL wrapper already emits the `test` start/success/failure frame around
`RunTest`.

### `internal/cmd/modules.go` — branch `RunTest`

Add remote-flag detection at the top and strip those flags in the parse
loop; branch to `runTestRemote` after module resolution:

```go
func RunTest(ctx context.Context, opts ModulesOpts) ([]string, error) {
	if err := requireOdooConfig(opts.Cfg); err != nil {
		return nil, err
	}
	from, remote := remoteFlagsIn(opts.Args)

	var (
		modules []string
		tags    string
		update  bool
	)
	args := opts.Args
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			i++ // skip the value token too
		case strings.HasPrefix(a, "--from="), a == "--remote":
			// consumed by remoteFlagsIn
		case a == "--tags":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--tags requires a value")
			}
			tags = args[i+1]
			i++
		case strings.HasPrefix(a, "--tags="):
			tags = strings.TrimPrefix(a, "--tags=")
		case a == "--update":
			update = true
		case strings.HasPrefix(a, "-"):
			// forward-compat: ignore unknown flags
		default:
			modules = append(modules, a)
		}
	}

	if len(modules) == 0 {
		picked, err := pickModulesInteractive(ctx, opts, "Modules to test", nil)
		if err != nil {
			return nil, err
		}
		modules = picked
	}
	emitResolved(opts, modules)

	o := odoo.TestOpts{Modules: modules, Tags: tags, Update: update}
	if from != "" || remote {
		return modules, runTestRemote(ctx, opts, from, o)
	}
	return modules, runOdoo(ctx, opts, odoo.Test(buildConn(opts), o))
}
```

`requireOdooConfig` still guards: the remote path needs the local config
only to resolve the `link` binding / global targets, which is what
`resolveRemoteShell` reads.

### `internal/repl/repl.go` — help only

No dispatch change (`test` is already routed through `runModules`, and
`ModulesOpts` already carries `Palette`, `Root`, `StreamOut`). Add help
entries under the Modules section, after the existing `test` rows:

```go
{"  --from <t>", "Run the suite on a remote target (or --remote for the link binding)"},
```

`--remote` needs no separate row — it's the nameless variant of
`--from`, mirrored from `logs`/`restart`/`shell-run` help.

### Registry / dispatch

Unchanged. `test` is already in `Registry`, `dispatchNames`, and the
Modules help group (Unit 11); the `registry_test.go` consistency checks
stay green.

### Tests

`internal/cmd/test_remote_test.go`:

- `TestRunTestStripsRemoteFlags` — table test on the parse loop: assert
  `--from prod` / `--from=prod` / `--remote` are consumed and never leak
  into the resolved module slice (e.g. `test sale --from prod` →
  modules=`[sale]`, not `[sale prod]`; `test --remote sale --update` →
  modules=`[sale]`, update=true). Drive through a small extracted pure
  parser or assert on the returned `modules` with a stubbed resolver.
- Reuse the existing `odoo.Test` argv tests (Unit 11) — the builder is
  unchanged, so remote and local emit the same argv; no duplication
  needed there.

Prefer extracting the flag/positional parse into a pure
`parseTestArgs(args) (modules []string, tags string, update bool, from string, remote bool)` so it is unit-testable without a container, and
call it from `RunTest`.

## Dependencies

None new. All reuse of existing internal packages
(`resolveRemoteShell`, `remoteContainerCmd`, `runSSHStream`,
`confirmRemoteProd`, `remoteFlagsIn`, `odoo.Test`).

## Verify when done

- [ ] `test sale --from prod` resolves the `prod` target, fetches its
      remote profile, and runs `odoo … --test-tags /sale …` in the
      remote Odoo container over SSH, streaming colorized output.
- [ ] `test sale account --remote` uses the dir's `link` binding and
      filters `--test-tags /sale,/account` (no `-u`).
- [ ] `test sale --from prod --update` adds `-u sale` to the remote
      argv.
- [ ] `test sale --from prod --tags :TestX.test_y` forwards the spec
      verbatim (no auto `/sale`).
- [ ] `--from <val>` / `--remote` are stripped: the value token is never
      treated as a module (`test sale --from prod` → modules=`[sale]`).
- [ ] A `prod`-stage remote target prompts the red remote-prod confirm
      (`confirmRemoteProd`); `--force` bypasses; non-TTY (one-shot
      without `--force`) fails closed.
- [ ] `test <mods>` with **no** remote flag is unchanged — runs locally
      via `runOdoo` / `docker.Exec`, no SSH.
- [ ] Success/failure/cancel still close through the existing REPL
      `test` frame (`echo.test.module.<mod>` / auto-copy on failure /
      `echo.test.cancelled`), local and remote alike.
- [ ] `help` shows the `--from <t>` entry under the Modules `test` block.
- [ ] `go build ./...`, `go vet ./...`, and
      `go test ./internal/cmd/... ./internal/repl/...` all pass.
