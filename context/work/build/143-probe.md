# Unit 143 · probe
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-19.

## Goal

One read-only command that answers questions about a live Odoo database and returns the answer as data:
`probe -c <expr>`, `probe -` (script on stdin) and five presets (`ref`, `read`, `fields`, `param`, `arch`). It
works the same locally and with `--from`/`--remote`, guarantees no write reaches the database, and prints a
framed result that `--json` sends to stdout alone. It replaces the Python heredocs the `odoo-probe` skill makes
agents write for every data question (`~/Documents/Projects/odoo-probe/SKILL.md` "The core probe",
`references/recipes.md`), and the digging for a JSON line in the Odoo boot log.

## Design

Why: an agent today writes a heredoc with its own try/except and `json.dumps`, pipes it through
`shell-run -`, then scans the boot log for its line. Read-only depends on the agent never calling
`env.cr.commit()`. shell-run asks the prod confirm even for reads (`confirmRemoteProd`,
`internal/cmd/shell_remote.go`). Audit of 2026-10-19 (three read-only passes over the skill and the code).

Decisions:

1. **Transport is shell-run's.** The script is built in Go and fed to `odoo shell` through stdin, locally with
   `docker.ExecWithStdinReader` and remotely with `resolveRemoteShell` + `remoteContainerCmd` +
   `runSSHStream`, exactly like `RunShellScript`. Only the argv differs: `odoo.ProbeShell(conn)` is
   `odoo.Shell(conn)` plus `--log-level=warn`, so the boot log is a few lines instead of hundreds.
2. **The Python lives in one embedded template**, `internal/odoo/probe.py` (`//go:embed`), and
   `odoo.ProbeScript(req ProbeRequest) ([]byte, error)` renders it. The request travels as base64 of its JSON
   (`ARGS = json.loads(base64.b64decode("…"))`), never spliced into Python source, so no argument can inject
   code. A user script for `probe -` travels the same way (base64) and runs through `exec`.
3. **Read-only by construction.** The prelude runs `env.cr.rollback()` (ends the transaction the shell opened
   with `context_get`), then `env.cr.execute("SET TRANSACTION READ ONLY")`, and replaces `env.cr.commit` with a
   function that raises `RuntimeError("probe is read-only")`. Any INSERT/UPDATE/DELETE fails in Postgres
   (`cannot execute … in a read-only transaction`) and comes back as the probe's error. The shell's own final
   `cr.rollback()` stays.
   **Hypothesis:** `SET TRANSACTION READ ONLY` after the rollback holds on 17, 18 and 19 and no ORM read path
   needs a write; verify live (ledger row 143).
4. **Framing that survives any transport.** The result is JSON, base64-encoded and printed on stdout in chunks
   of at most 32 KiB, one per line: `@@ECHO_PROBE@@ <seq> <total> <chunk>`. Base64 is immune to ANSI
   stripping, `\r` and the 1 MiB line cap of `runSSHStream` (`internal/cmd/remote.go:156`). The Go side
   reassembles chunks 1..total, decodes, and treats a missing or partial frame as an execution error
   (`probe produced no result`), with the last output lines as context.
5. **Payload shape** (the JSON inside the frame, also what `--json` prints, plus `db`, `stage`, `target`
   added by Go):
   ```json
   {"ok": true, "kind": "eval", "result": <value>, "stdout": "<what the code printed>",
    "db": "x", "stage": "dev", "target": "develop" | null, "duration_ms": 1234}
   {"ok": false, "kind": "eval", "error": {"type": "ValueError", "message": "...",
    "traceback": "...", "file": "/mnt/extra-addons/m/models/x.py", "line": 42}, "stdout": "", ...}
   ```
   `error.file`/`line` is the deepest traceback frame that is not the probe template itself. The code's own
   `print` output is captured with `contextlib.redirect_stdout` and returned in `stdout`, never mixed with
   the frame.
6. **Value conversion** (`default=` of `json.dumps`): a recordset becomes
   `{"model": m, "ids": [...], "display_names": [...]}` (display names only up to 50 records), `date`/`datetime`
   ISO 8601, `bytes` base64 string, `set`/`tuple` list, anything else `str(v)`.
7. **No prod gate.** The database refuses writes, so `probe` joins the read-only verbs that never gate
   (`logs`, `view`, `compare`, `db-pull`; [architecture-and-traps](../../knowledge/architecture-and-traps.md#prod-gate-rule)).
   It still runs arbitrary Python, so the help says plainly that it is read-only for the database only.
8. **Secrets in `param`**: values of keys matching `secret`, `password`, `token`, `api_key` or `apikey`
   (case-insensitive) print as `"***"` unless `--reveal`. `database.secret` is therefore masked by default.
9. **Exit codes**: 0 a result, 1 a Python error or no frame, 2 a usage error (unknown subcommand or flag,
   missing argument, `--domain` not JSON, `--limit` not a positive integer).

## Forms

| Form | Python | `result` |
|---|---|---|
| `probe -c <expr>` | `eval(expr, ns)` with `ns = {env, self: env.user, Command, fields, datetime, json}` | the value |
| `probe -c a=<expr> -c b=<expr>` (repeatable) | each evaluated in order in one boot | object keyed by name; an unnamed `-c` among several is keyed by its expression text |
| `probe -` (stdin) | `exec(script, ns)` | the variable `result` if the script set it, else `null` |
| `probe ref <xmlid>` | `ir.model.data` lookup | `{xmlid, model, id, display_name, module, noupdate}`; missing xmlid is a `ValueError` |
| `probe read <model> [--domain JSON] [--fields a,b] [--limit N] [--order S]` | `search_count` + `search_read` with `active_test` default | `{model, count, rows}`; `--limit` default 20 |
| `probe read <model> --count [--domain JSON]` | `search_count` | `{model, count}` |
| `probe fields <model> [name…]` | `env[model]._fields` (+ `ir.model.fields.modules` when readable) | list of `{name, type, string, relation, required, readonly, store, compute, related, modules}` |
| `probe param [key|prefix]` | `ir.config_parameter.sudo()`; exact key, else `=like prefix%`; no argument lists all | list of `{key, value}` |
| `probe arch <xmlid|model> [--type T]` | root primary view; combined arch; inheritance chain | `{model, type, root: {id, xmlid, name}, arch, chain: [{id, xmlid, name, module, priority, mode, active, inherit_id}]}` |

`-c` is named only when it matches `^[A-Za-z_][A-Za-z0-9_]*=` and the next character is not `=` (so
`-c "a == b"` stays an expression). `arch`: for an xmlid of an extension view, walk `inherit_id` up to the
primary view; for a model, `--type` defaults to `form` and the root is the default view of that type
(`env['ir.ui.view'].default_view(model, type)`). `tree` and `list` are accepted and mapped to whichever the
running version uses (17: `tree`, 18+: `list`, by `odoo.Major`). The arch is `_get_combined_arch()` pretty
printed; if that method is missing, fall back to `get_views` (**Hypothesis**: both exist on 17/18/19). The
chain is a breadth-first walk of `inherit_id` children with `active_test=False`, ordered by priority then id.

## Implementation

### `internal/odoo/probe.go`, `internal/odoo/probe.py` (new)

- `type ProbeRequest struct { Kind string; Exprs []ProbeExpr; Script string; Model string; XMLID string;
  Domain json.RawMessage; Fields []string; Names []string; Limit int; Order string; Count bool; Key string;
  ViewType string; Reveal bool; Major int }`, `type ProbeExpr struct{ Name, Expr string }`.
- `ProbeScript(req) ([]byte, error)`, `ProbeShell(c Conn) Cmd`, `ProbeFramePrefix = "@@ECHO_PROBE@@"`.
- `DecodeProbeFrames(lines []string) (json.RawMessage, error)` reassembles and decodes (pure, tested).

### `internal/cmd/probe.go` (new)

- `ProbeOpts{Cfg, Root, Args, Stdin io.Reader, Palette, Log, StreamOut}`, `ProbeResult{Payload map[string]any,
  OK bool, DB, Stage, Target string}`.
- `parseProbeArgs(args) (odoo.ProbeRequest, from string, remote, jsonOut bool, err error)`: consumes the
  value of `--from`, `-c`, `--domain`, `--fields`, `--limit`, `--order`, `--type`; rejects unknown flags with
  `ErrUsage`; validates `--domain` as a JSON array.
- `RunProbe`: local path requires `requireOdooConfig`; remote path through `resolveRemoteShell`. Every line
  that is not a frame goes to `StreamOut`; frame lines are collected. Returns `ErrProbeFailed` (wrapped with
  the error type and message) when `ok` is false, so the handler exits 1.

### `internal/repl/probe.go` (new)

- `sess.runProbe`: with `--json`, stdout gets exactly the payload (one JSON object, newline), every log line
  goes to stderr (same contract as `modstate --json`), and the closing `echo.probe` line is not printed on
  stdout. Without `--json`: one `echo.probe` INFO line (`kind=… db=… duration=…`), then `result`
  pretty-printed (2-space JSON) and the captured `stdout` if any; on error an `ERROR echo.probe` line with
  `type`, `file`, `line` and the traceback lines. `probe -` reads stdin and fails fast on a TTY stdin, like
  `shell-run -`. No auto-copy (read-only command).

### Wiring (the [checklist](../../knowledge/code-standards.md#new-command-wiring))

`Registry` (Modules group, after `modstate`), `dispatchNames` + `case`, `commandFlags` (`-c`, `--json`,
`--from`, `--remote`, `--domain`, `--fields`, `--limit`, `--order`, `--count`, `--type`, `--reveal`), a
`commandDoc` with notes (read-only transaction, no prod gate, framing, secrets) and three examples,
`projectlessOneShot` (with a remote flag), `isMetaCommand` no, `sequenceCommands` no. `help agents` gains one
row: `probe` for reading live data. `CHANGELOG.md` `[Unreleased]` `Added`.

## Tests

- `internal/odoo/probe_test.go`: the rendered script embeds args only as base64 (no raw user text appears in
  it); frame decode round-trip with several chunks, out-of-order rejection, missing chunk error.
- `internal/cmd/probe_test.go`: parser table (named/unnamed `-c`, `a == b`, each subcommand, bad domain,
  bad limit, unknown flag → `ErrUsage`, `--from` value consumed); a fake-transport run (seam over the exec
  call) feeding frame lines interleaved with log lines.
- Python: `python3 -m py_compile` of the template in a test that skips when `python3` is missing.

## Out of scope

`--user <login>` (probe as another user), access-rights probes (`ir.model.access`/`ir.rule`), a persistent
probe session. Possible later units.

## Done

Build/vet/test green, help cross-checks green, CHANGELOG entry, ledger row 143 "live verification pending"
(read-only probe on a dev target with Odoo 17, 18 and 19: each form, plus a write attempt that must fail).
