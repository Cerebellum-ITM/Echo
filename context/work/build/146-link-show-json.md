# Unit 146 · link-show-json
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-19.

## Goal

`link --show --json` returns the binding and the resolved remote target as one JSON object, so an agent's
pre-flight (stage, DB, Odoo major, what is deployed) reads data instead of parsing the `echo.system.status`
line (`~/Documents/Projects/odoo-probe/SKILL.md` pre-flight section).

## Design

- `--json` is accepted with `--show` (today only with `--list`, `internal/cmd/link.go` `parseLinkArgs`).
- The object is printed on stdout once the server profile is read; every log line goes to stderr; the
  `compose ps` table is not rendered (its rows are in the object instead).
- Shape:
  ```json
  {"target": "develop", "ssh_host": "muutrade", "remote_path": "/srv/x",
   "stage": "dev", "stage_declared": true, "db": "x", "odoo_version": "18.0",
   "compose": "docker compose", "reverb": {"project": "p", "env": "e"} | null,
   "deployed": {"branch": "echo/deploy", "sha": "…"} | null,
   "containers": [{"service": "odoo", "state": "running", "health": "healthy", "status": "Up 3 hours"}] | null}
  ```
  `stage` is the normalized gate stage (undeclared → `prod`, `stage_declared=false`), the same value every
  remote gate reads. `deployed` is what `link --show` already reads for its human output when available; if
  the human path does not read it today, the field is `null` (no new SSH call for it). `containers` is `null`
  when the structured `compose ps` read fails.
- Not bound: exit 2 with `ErrUsage` and nothing on stdout, like the human path's error.

## Implementation

`internal/cmd/link.go` (parser + a `LinkShowResult` callback or return value), `internal/repl/link.go` (print
the JSON, skip finalize like `--list --json`), `commandDoc` note + example, tests for the parser and for the
JSON assembly over a fake transport. `CHANGELOG.md` `Added`.

## Done

Build/vet/test green, help cross-checks green, CHANGELOG entry, ledger row 146 "live verification pending".
