# Unit 110: `deploy` pre-flight — refuse to push XML the loader will reject

## Goal

`deploy` runs Unit 109's lint over the modules it is about to deploy, **before
anything leaves the machine**, and refuses to continue when a manifest-listed
file is broken.

```
echo_cli deploy                 # lints the selected modules, then deploys
echo_cli deploy --no-lint       # skip the pre-flight
```

On by default. The errors this catches abort the registry load, so deploying
with them is never what anyone wanted.

## Why this is a separate unit

Unit 109 built the command and the library. This unit changes the contract of a
command that is already load-bearing, and it introduces a new failure mode —
*deploy blocked* — in exchange for removing an old one. That trade deserves its
own decision, its own tests and its own commit, so it can be reverted without
taking the lint with it.

It is also the unit where the lint stops being something you remember to run.
The command alone does not close the loop; the two failures that motivated Unit
109 happened to an agent that had *written down* the rule.

## Why default-on is safe here (and would not be without Unit 109's severity)

The prototype found two genuine findings over 356 files with zero false
positives — but **both were files no manifest lists**. A naive pre-flight that
failed on every finding would have blocked a deploy over dead files that the
server never reads. That is worse than the problem it solves: it makes `deploy`
stricter than Odoo.

Unit 109's manifest-aware severity is the precondition. This unit blocks on
`err` findings only:

| finding | pre-flight |
|---|---|
| `err` — file listed in `data`/`demo` of a deployed module's manifest | **blocks** |
| `warn` — unlisted file, or a render-time field | reported, does not block |

So the blocking set is exactly "markup the loader will read and refuse".

## Behavior

**Scope: the selected modules, not the repo.** `RunDeploy` already computes the
module set it is deploying — `modules`, sorted and deduped at
[deploy.go:989](internal/cmd/deploy.go:989). The pre-flight lints those modules
and nothing else.

Linting the whole tree would be both wasted work and actively wrong: a broken
file in a module this deploy does not touch is not this deploy's problem, and
blocking on it would train everyone to reach for `--no-lint`.

**Placement: after selection, before the first remote contact.** The insertion
point is immediately after `sort.Strings(modules)` and before
`fetchRemoteProfile` — i.e. before the run's first SSH, before the code push,
before the DB checkpoint, before `-u`.

```
subcommands (--set-push, --set-checkpoint, --rollback, …)
selection (dirty modules, commits ahead, pickDeployItems)
module resolution → `modules`
──────────── pre-flight lint ────────────      ← nothing remote has happened yet
remote profile / module states  (first SSH)
code push  (rsync overlay or git branch advance)
DB checkpoint
module install / update
```

It has to be **after** selection because that is what defines the scope. It is
**before** everything remote so that a blocked deploy costs exactly nothing:
no code copied, no branch advanced, no checkpoint taken, nothing to roll back.
This is the whole difference from today, where the same defect surfaces after
the registry has already aborted and the checkpoint has already been written.

This also sits alongside an existing convention rather than inventing one:
[deploy.go:1038](internal/cmd/deploy.go:1038) already fails a non-linear commit
selection "before any remote change".

**Output.** The findings stream through `deploy`'s own log frame, not the
`lint` command's:

- `err` findings → `ERROR` lines, sub `lint`, with `file` and `line` fields
- `warn` findings → `WARNING` lines, same shape
- a summary `INFO` line naming files checked and counts

On a block, the run ends with a `DeployResult` carrying the failure and a
message that names the escape hatch, so the next step is obvious without
reading docs.

**`--no-lint`.** Skips the pre-flight entirely and emits a `WARNING` saying so.
It exists for the case where you know something the tool does not; it must be
visible in the transcript, because a silent skip in a recorded run is how a
default-on check becomes decoration.

**When `xmllint` is absent.** The pre-flight runs with whatever passes are
available and emits Unit 109's warning naming the skipped ones. A degraded
pre-flight still blocks on what it did catch; it never blocks *because* a
validator is missing.

**Interaction with the rest of `deploy`.** The pre-flight is local and
read-only, so it composes with everything: `--auto`, `--modules`, `--commits`,
git-deploy targets, `watch`-driven runs, headless/script mode. In headless mode
a block is an exit code like any other selection failure. Nothing about
checkpoint, rollback, actions or test policy changes.

`--rollback`, `--restore-code` and the `--set-*` config subcommands return
before selection and therefore never lint — they deploy no new code.

## Design

`RunDeploy` gains one local step. No new package, no new config table:

```go
if !p.noLint {
    findings, err := odoolint.Check(modulePaths(opts.Root, modules), lintOpts)
    // stream findings through opts.log; count Kind == err
    // blocking → return DeployResult{}, deployLintError(n)
}
```

`parseDeployArgs` gains `--no-lint` (bool, in `commandFlags["deploy"]`, help and
autocomplete). Grammar resolution and the manifest set come from Unit 109's
library exactly as the `lint` command builds them — the two call sites share the
resolution helper so they cannot drift.

### Deliberately not configurable

No `[lint] preflight = false` in the config. A per-project opt-out would be
found and set once by whoever hits a false positive, and then the check is off
forever on the machine that needed it most. `--no-lint` is per-run, visible in
the transcript, and expires when the run ends. If the pre-flight turns out to
produce false positives in practice, the fix is a better rule in Unit 109 — not
a config flag that hides it.

## Tests

- Pre-flight blocks: a manifest-listed file with a `--` comment in a selected
  module → `RunDeploy` returns an error, and **no** remote call was made (fake
  SSH/rsync layer records zero invocations).
- Pre-flight does not block: the same defect in a file no manifest lists →
  `WARNING`, deploy proceeds.
- Scope: a broken manifest-listed file in a module *not* selected → deploy
  proceeds, nothing reported.
- `--no-lint`: the broken selected module deploys, and the `WARNING` naming the
  skip is present in the emitted lines.
- Placement: on a block, no checkpoint was created and no push ran — asserted on
  the fakes, not just on the returned error.
- `parseDeployArgs`: `--no-lint` parses, defaults false, and is accepted
  alongside `--auto`/`--modules`/`--commits`.
- `--rollback` / `--restore-code` / `--set-push` never invoke the lint.
- `xmllint` absent + a `--` defect in a listed file: still blocks, with the
  skipped-pass warning present.

## Verify when done

- Reintroduce the `--` comment in a manifest-listed file of a real module and
  run `echo_cli deploy`: the run stops with the server's own message, before any
  SSH, in well under a second. Nothing on the server changed — confirm with
  `push --clean` showing no drift and no new checkpoint in the index.
- Same file, `echo_cli deploy --no-lint`: the deploy proceeds and fails the way
  it does today (registry abort), proving the escape hatch is real and the old
  behaviour is one flag away.
- A normal clean deploy is indistinguishable from before apart from one extra
  `INFO` summary line, and no measurable added wall time.
- `watch`-driven deploy inherits the pre-flight without extra wiring.

## Follow-up this unblocks

The "listed in a manifest but missing on disk" check (Unit 109's Out of scope)
becomes clearly worth its own unit once the pre-flight exists: it is the other
half of the same failure — the loader aborting on a data file it cannot open —
and it is cheaper to implement than the schema pass. It should be the next unit
after this one.
