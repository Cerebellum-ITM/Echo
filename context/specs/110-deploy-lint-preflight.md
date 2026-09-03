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
| `warn` — no manifest lists the file | reported, does not block |

So the blocking set is exactly "markup the loader will read and refuse".

## Behavior

**Scope: the selected modules, not the repo.** `RunDeploy` already computes the
module set it is deploying — `modules`, sorted and deduped at
[deploy.go:996](internal/cmd/deploy.go:996). The pre-flight lints those modules
and nothing else.

Linting the whole tree would be both wasted work and actively wrong: a broken
file in a module this deploy does not touch is not this deploy's problem, and
blocking on it would train everyone to reach for `--no-lint`.

**Placement: after selection, before the first remote contact.** The insertion
point is immediately after `sort.Strings(modules)` ([deploy.go:996](internal/cmd/deploy.go:996))
and before `fetchRemoteProfile` ([deploy.go:1013](internal/cmd/deploy.go:1013)),
which is the run's **first SSH**.

```
subcommands (--set-push, --set-checkpoint, --rollback, --restore-code, …)
remote resolution  (local config / picker — no SSH)
selection (dirty modules, commits ahead, pickDeployItems)
module resolution → `modules`
──────────── pre-flight lint ────────────      ← nothing remote has happened yet
remote profile / module states  (first SSH)
code push  (rsync overlay or git branch advance)
DB checkpoint
module install / update
```

Note `resolveDeployRemote` runs earlier than the lint, at
[deploy.go:841](internal/cmd/deploy.go:841). That is deliberate and harmless: it
reads local config and may open a picker, but it opens no connection. The
invariant that matters is that nothing has been *sent* or *changed* when the
lint decides.

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
// internal/cmd/deploy_lint.go
func deployLintPreflight(opts DeployOpts, p deployArgs, modules []string) error
```

It calls `cmd.LintModules(cfg, root, modules)` — the same entry point that
resolves the grammar and the manifest set for the `lint` command, so the two
call sites cannot drift — streams the findings through `opts.log` under sub
`lint`, and returns `ErrLintBlocked` when any finding is `err`.

`parseDeployArgs` gains `--no-lint` (bool, in `commandFlags["deploy"]`, help and
autocomplete).

One deliberate asymmetry: a lint that *fails to run* (an unreadable tree, an
xmllint that dies) is a `WARNING` and the deploy continues. The lint is a guard,
not the job; a broken guard must not take the deploy down with it. Only findings
block.

### Deliberately not configurable

No `[lint] preflight = false` in the config. A per-project opt-out would be
found and set once by whoever hits a false positive, and then the check is off
forever on the machine that needed it most. `--no-lint` is per-run, visible in
the transcript, and expires when the run ends. If the pre-flight turns out to
produce false positives in practice, the fix is a better rule in Unit 109 — not
a config flag that hides it.

## Tests

Unit tests run against `deployLintPreflight` with a capturing logger, which is
where all the decisions live:

- Blocks: a manifest-listed file with a `--` comment in a selected module →
  `ErrLintBlocked`, the finding reported as `ERROR`, and the error text names
  `--no-lint`.
- Does not block: the same defect in a file no manifest lists → `WARNING` plus
  a clean verdict, no error.
- Scope: with a clean module selected, a broken module elsewhere in the repo is
  neither reported nor blocking.
- `--no-lint`: no error, and the skip `WARNING` is the *only* line emitted.
- A module absent locally (a rename in flight) is a `WARNING`, not a failure.
- `parseDeployArgs`: `--no-lint` parses, defaults false, and survives alongside
  `--auto` / `--modules` / `--push`.
- `--rollback` / `--restore-code` / `--set-push` / `--test-clear` are all
  recognised as manage/restore paths, which return before the selection and so
  never reach the lint.

**Not unit-tested, and why.** The spec originally called for asserting on fake
SSH/rsync layers that a blocked run made zero remote calls. `deploy` has no such
fake layer — `RunDeploy` is only exercised in tests through the config-only
path that returns before any remote work — and building one is a larger change
than this unit. The placement invariant is instead verified end to end against a
deliberately unresolvable host (see below), which is a stronger check than a
mock would be: a real DNS failure would be impossible to miss.

## Verify when done

Verified against a scratch project whose configured target is a host that
cannot resolve (`host.que.no.existe.invalid`), which turns "did it touch the
network?" into an unmissable observation:

- **Blocked**: a `--` comment in a manifest-listed file → the run emits the
  finding and `pre-flight blocked the deploy`, exits 1, and **never logs
  `reading remote profile`** — no DNS failure appears, because no connection was
  attempted. Nothing to roll back because nothing happened.
- **`--no-lint`**: emits `pre-flight skipped flag=--no-lint`, then proceeds to
  `reading remote profile` and fails on the unresolvable host — proving the
  escape hatch really does reach the remote path.
- **Clean file**: emits `pre-flight clean modules=demo_mod files=1 warnings=0`
  and then proceeds to the remote exactly as before, one extra `INFO` line and
  no measurable added wall time.

Still to confirm against a live host (needs a real remote, unavailable in the
dev environment):

- A blocked run leaves no drift (`push --clean` shows nothing) and no new
  checkpoint in the index. The reasoning is sound — the block precedes both
  steps — but it has not been observed.
- `watch`-driven deploy inherits the pre-flight without extra wiring.

## Follow-up this unblocks

The "listed in a manifest but missing on disk" check (Unit 109's Out of scope)
becomes clearly worth its own unit once the pre-flight exists: it is the other
half of the same failure — the loader aborting on a data file it cannot open —
and it is cheaper to implement than the schema pass. It should be the next unit
after this one.
