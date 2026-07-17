# Unit 101: `deploy --rollback-on-fail` / `--no-rollback-on-fail` — deterministic failure decision

## Goal

Make the deploy-failure rollback decision **explicit and TTY-independent**, so a
non-interactive caller (an agent, CI) can fix the outcome instead of relying on
terminal detection:

- `--rollback-on-fail` — on a failed run, roll the DB back to the checkpoint,
  without prompting (even in an interactive session).
- `--no-rollback-on-fail` — on a failed run, **leave the broken DB** and the
  checkpoint (recorded for a later `deploy --rollback`), without prompting.

The two are mutually exclusive. When neither is given, behavior is **unchanged**
from today.

### Why this exists (the hole it closes)

`handleDeployFailure` currently decides whether to roll back with:

```go
if !p.force && stdinIsTTY() {
    if !confirmRollback(...) { /* leave broken DB, keep checkpoint */ }
}
```

So the "leave the broken DB so I can inspect it" branch is reachable **only**
interactively; every headless path (`--force`, `watch`, any no-TTY run)
hard-codes *roll back*. Two problems for an agent:

1. **Fragile TTY detection.** An agent that runs Echo inside a pty presents a
   TTY, so it would **hang** on `confirmRollback` — the prompt an agent can't
   answer.
2. **No deterministic "don't roll back."** There is no flag that says "headless,
   but leave the broken DB." `--force` only means *roll back yes*.

An explicit flag pair removes both: the decision no longer depends on whether a
terminal happens to be attached, and both outcomes are reachable headlessly.

## Behavior

The rollback decision in `handleDeployFailure` becomes:

1. `--rollback-on-fail` set → roll back, no prompt.
2. `--no-rollback-on-fail` set → leave the broken DB (record the checkpoint,
   log `skipped — restore later with deploy --rollback`), no prompt.
3. neither set, `!--force` and a TTY → ask `confirmRollback` (today's prompt).
4. neither set, otherwise (`--force`, or no TTY) → roll back (today's default).

The explicit flag **wins over `--force`**: `--force --no-rollback-on-fail`
leaves the broken DB. `watch` (which passes `--force`, no new flag) still rolls
back every cycle — unchanged, as a desatended monitor should never leave a
broken DB between cycles.

## Design

- `deployArgs`: add `rollbackOnFail *bool` (nil = unspecified → fall through to
  the TTY-based default). `--rollback-on-fail` → `&true`,
  `--no-rollback-on-fail` → `&false`. `parseDeployArgs` rejects both together
  (`ErrUsage`). Exact-string matches, so no collision with the standalone
  `--rollback` flag.
- `handleDeployFailure`: replace the inline gate with a resolved `doRollback`
  bool per the four cases above; the "leave broken" body (AddCheckpoint +
  WARNING + return) is reused verbatim for both the interactive-decline and the
  `--no-rollback-on-fail` paths.
- Registered in `commandFlags["deploy"]` and the REPL help.
- No config default (`[deploy] rollback_on_fail`) in this unit — the flags are
  enough; a persisted default can be a later add if a workflow needs it.

## Out of scope

- No change to `watch` (still auto-rolls-back; the flag isn't threaded into
  `deployCommitsHeadless`).
- No change to `deploy --rollback` (the standalone post-hoc restore) — these
  flags only steer the *on-failure* decision during a deploy.
- No change to when a checkpoint is taken (Unit 89/90 policy) — with no
  checkpoint there is nothing to roll back and the flags are inert.

## Tests

- `parseDeployArgs`: `--rollback-on-fail` → `*rollbackOnFail == true`,
  `--no-rollback-on-fail` → `== false`, both together → `ErrUsage`.
- A `resolveRollbackDecision(p, tty)` helper (extracted so it's unit-testable
  without a real TTY) covering the four-case table: explicit true/false win;
  else force→true; else tty→(prompt, not covered here); else →true.
