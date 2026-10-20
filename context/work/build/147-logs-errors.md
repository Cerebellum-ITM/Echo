# Unit 147 · logs-errors
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-19.

## Goal

`logs --errors` answers "why does it break" in one call: it reads the last lines of the Odoo service log (local
or remote), groups each ERROR/CRITICAL line with its traceback, and prints only those, newest last; `--json`
returns them as data with the exception and the frames.

## Design

- Reuses `odoo.Outcome` from [Unit 144](144-module-run-outcome.md): the bounded log lines are fed to it and
  its `errors` are the result. Depends on 144.
- `logs --errors` implies `--no-follow`; the window is `-t/--tail N` (default 2000 lines when `--errors` is
  given without `--tail`). `--follow` together with `--errors` is `ErrUsage`.
- `-n <count>` keeps the last `count` errors (default 5).
- Works wherever `logs` works (local, `--from`, `--remote`), read-only, no gate.
- Human output: each error as its header line plus the traceback, rendered by the usual log colorer, then
  `echo.logs errors=<shown> scanned=<lines>`. No errors found: INFO `no errors in the last <N> lines`, exit 0.
- `--json`: stdout `{"scanned": 2000, "errors": [{"time": "…", "db": "…", "logger": "…", "message": "…",
  "exception": "…", "frames": [...]}]}`; logs to stderr. The exit is 0 even when errors are found (it is an
  inspection, not a run).
- `Outcome` entries gain `time` and `db` from the header (additive; Unit 144's JSON gains them too).

## Implementation

The `logs` parser and handler (`internal/cmd/docker.go` / remote logs path, `internal/repl` logs handler),
`commandFlags` (`--errors`, `-n`, `--json`), `commandDoc` note + example, tests over captured log text with
two tracebacks and noise. `CHANGELOG.md` `Added`.

## Done

Build/vet/test green, help cross-checks green, CHANGELOG entry, ledger row 147 "live verification pending".
