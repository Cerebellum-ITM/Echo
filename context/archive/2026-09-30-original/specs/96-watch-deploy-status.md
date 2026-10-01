# Unit 96: `watch` per-cycle deploy record + `logview --json`

## Goal

Give an **agent** (or any headless caller) a way to learn *whether its
commit was auto-deployed by `watch`* — without re-invoking `watch`, without
SSH, and without the 1Password SSH agent.

The insight: `watch` runs on the user's machine and `deploy` saves its
command-log **locally** (`root = projectDir`, never pushed to the server).
The agent runs on that same machine, in the same project dir. So a headless,
read-only **local file read** is enough — if `watch` leaves a per-cycle
record the agent can query.

Today it doesn't: `watch` is a single long-running REPL command whose record
is written only on exit, and its internal `deployCommitsHeadless` never calls
`SaveCmdLog`. And `logview` has no machine-readable output (TUI + a plain
text `--list`, no `--json`).

This unit closes both gaps:

1. `watch` writes one cmd-log record **per deploy cycle** (`command:
   watch-deploy`), carrying the deployed SHAs and the deployed branch tip.
2. `logview --json` dumps the run list as JSON to stdout, headless.

## Design

### `watch-deploy` record per cycle

In `watchCycle`, after `deployCommitsHeadless` returns, persist one
`config.CmdLogRecord` via `config.SaveCmdLog(opts.Root, rec)`:

- `Command: "watch-deploy"` — a distinct verb so the agent filters cleanly.
- `Cmd: "deploy --commits <sha1>,<sha2>"` — the effective command line; the
  short SHAs are greppable, mirroring what `deployCommitsHeadless` runs.
- `DB / Stage / From` — from `rsc.prof.DBName`, `rsc.target.stage`, `from`.
- `Exit` — `0` on success, `1` when `derr != nil`.
- `Started` — the cycle start time (stamped at `watchCycle` entry).
- `DeployedTip: <new>` — **new field** (full SHA of the branch tip this
  cycle deployed). This is what lets the agent do the robust check
  `git merge-base --is-ancestor <mySHA> <DeployedTip>`, since `watch`
  batches: a later commit whose range includes the agent's commit is what
  actually ships.
- `Errors: 0/1`, `Lines`: a short synthetic summary (modules, commits,
  result) so the record is also legible in the `logview` browser. (v1 does
  **not** capture the full deploy output stream — the go/no-go signal is
  `Exit` + `DeployedTip`; failure detail stays in the terminal / follow
  logs.)

Guards mirror `saveCmdLog`: skip when `opts.Cfg == nil || CmdLogsDisabled`;
best-effort (ignore the write error); run one `PruneCmdLogs` pass after.

Only cycles that reach the deploy write a record. A rewritten branch, an
empty range, or "no deployable modules" return before the deploy and record
nothing (they deployed nothing).

### `CmdLogRecord.DeployedTip` / `CmdLogMeta.DeployedTip`

Add `DeployedTip string \`json:"deployed_tip,omitempty"\`` to both structs.
`ListCmdLogs` copies it into the meta. `omitempty` keeps every existing
record and every non-watch command byte-identical (empty tip omitted).

### `logview --json`

A headless, non-interactive dump of the resolved run list (`[]CmdLogMeta`)
as a JSON array to stdout — the same metas the browser and `--list` use,
composing with the existing source resolution (local by default, or
`--remote`/`--from` for a remote target's history).

- New `--json` flag in `parseLogviewArgs` (returned alongside the others).
- In `runLogview`, after `metas` is resolved (local or remote) and before
  the `--list` / TTY / TUI branches: if `--json`, `json.Marshal(metas)` to
  `os.Stdout` (the `finishActionsJSON` convention — payload to stdout, close
  line suppressed), set `exitOK`, return. Empty list → `[]` (not an error;
  the agent treats "no runs yet" as "not deployed yet").
- `--json` + `--clear` is a usage error (clear has no payload); `--json`
  wins over `--list`/`--last`/the TUI.

`logview` is already projectless and a meta command, so `echo_cli logview
--json` runs from the project dir with no compose project and no SSH.

### The agent's check (documentation only — `odoo-probe` skill)

```
mySHA = git rev-parse HEAD
poll (bounded, ~3× the watch interval):
    recs = echo_cli logview --json          # LOCAL, no --from, no SSH
    r = newest rec with command == "watch-deploy"
    if r.deployed_tip and git merge-base --is-ancestor mySHA r.deployed_tip:
        r.exit == 0 → deployed ✓ (report stage/db) ; else → deploy failed
        break
```

Skill wording lands in a **later** doc pass, not this unit.

## Implementation

### `internal/config/cmd_logs.go`
- Add `DeployedTip` to `CmdLogRecord` and `CmdLogMeta`.
- `ListCmdLogs`: map `rec.DeployedTip` into the meta.

### `internal/cmd/watch.go`
- Stamp `cycleStart := time.Now()` at `watchCycle` entry.
- After `deployCommitsHeadless`, build and `SaveCmdLog` the `watch-deploy`
  record (guards + prune as above). New helper
  `saveWatchDeployRecord(opts, rsc, from, shas, modules, tip, derr,
  cycleStart)` keeps `watchCycle` readable.

### `internal/repl/logview.go`
- `parseLogviewArgs`: add `--json` (new bool return).
- `runLogview`: handle `--json` after source resolution; reject `--json
  --clear`.
- New `logviewPrintJSON(metas)` mirroring `finishActionsJSON`.

### Registration
- `commandFlags["logview"]` += `--json`.
- Help row for `logview --json` ("Dump the run list as JSON (headless)").
- README: a short "headless / agents" note on `logview --json` and the
  `watch-deploy` record.
- CHANGELOG `[Unreleased]` `### Added`.

## Dependencies

- Unit 81 (command-log history / `SaveCmdLog`), Unit 82 (`logview`
  browser), Unit 84/87 (`watch`). All landed. No new packages.

## Verify when done

- [ ] A `watch` cycle that deploys writes a `watch-deploy` record locally
      with the deployed SHAs in `Cmd`, the branch tip in `deployed_tip`, and
      `Exit` reflecting success/failure.
- [ ] Cycles that deploy nothing (rewritten branch, empty range, no
      deployable modules) write no record.
- [ ] `echo_cli logview --json` prints a JSON array of the run metas to
      stdout headlessly (no TTY, no SSH, no compose project); empty history
      prints `[]` with exit 0.
- [ ] `logview --json --remote`/`--from <t>` dumps the remote history as
      JSON; `logview --json --clear` is a usage error.
- [ ] Existing records and non-watch commands decode unchanged
      (`deployed_tip` omitted when empty).
- [ ] Tests: `ListCmdLogs` round-trips `DeployedTip`; `parseLogviewArgs`
      parses `--json`; the `watch-deploy` record builder sets the fields;
      `logviewPrintJSON` emits valid JSON for a sample + the empty case.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass;
      registry/help cross-check tests stay green.
