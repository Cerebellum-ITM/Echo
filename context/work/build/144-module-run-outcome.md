# Unit 144 · module-run-outcome
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-19.

## Goal

`install`, `update`, `uninstall` and `test` never report green when nothing useful ran, and can return their
outcome as data: unknown flags and unknown module names are usage errors (exit 2), a `test` that ran zero tests
fails, and `--json` prints one object with the errors (exception, frames with file:line), warnings, migrations
and the test tally with each failing test.

## Design

Found in the 2026-10-19 audit (read in the code, not run):

- The local `install`/`update` parsers append every unknown token as a module (`internal/cmd/modules.go:158`,
  `:202`): `update sale --json` runs `-u sale,--json`. `test` drops unknown flags silently
  (`internal/cmd/test_remote.go:50`).
- A misspelled module in `test` gives `--test-tags /typo`: zero tests, exit 0.
- `runModules` routes every non-cancel error to `copyFailureLog` (`internal/repl/repl.go:449-458`), so usage
  errors (`ErrInvalidLogLevel`, `ErrLastExclusive`, `ErrNoLastUpdate`, `--tags requires a value`) exit 1 and
  overwrite the clipboard.
- An agent learns which test failed only by reading the whole log.

Decisions:

1. **Validation part (its own commit).**
   - Any token starting with `-` that the command does not know is `ErrUsage` (exit 2), on every path (local,
     remote, Reverb) of the four commands. The known set is exactly that command's `commandFlags` entry.
   - Module names are checked only on the **local** path, only for `install`, `update` and `test`, and only
     when addons discovery succeeds: a name not in the discovered addons is `ErrUsage` with a `hint=` field
     naming up to three close names (edit distance ≤ 2, or the name is a prefix/suffix of one). `--all`,
     `--last` and picker results are not checked. Remote paths are not checked (sideloaded addons are not in
     the local repo).
   - Each of the usage sentinels above is wrapped with `ErrUsage`; `runModules` routes `ErrUsage` through
     `finalize` with `exitUsage` and no auto-copy ([trap](../../knowledge/architecture-and-traps.md#cross-cutting-traps)).
   - `test` that ran zero tests: when the run finishes with no Odoo test tally line or a tally of `of 0 tests`,
     Echo emits `ERROR echo.test: no tests ran` with `modules=… tags=…` and the run exits 1.
2. **Outcome parser in a leaf package.** `internal/odoo/outcome.go`: `type Outcome struct` and
   `func (o *Outcome) Feed(line string)`, pure, fed every streamed line (ANSI already stripped). It knows:
   - Odoo log header `YYYY-MM-DD HH:MM:SS,mmm <pid> <LEVEL> <db> <logger>: <msg>`.
   - An ERROR/CRITICAL header opens an error entry; following lines that are not a log header belong to it
     (the traceback). From those: `frames` (`File "<f>", line <n>, in <func>`) and `exception` (the last
     non-indented line, e.g. `ValueError: bad`).
   - WARNING headers become warnings (logger, msg), capped at 50 with a `warnings_truncated` count.
   - Tests: a header whose message starts `FAIL: ` or `ERROR: ` followed by `Class.method` (optionally a
     parenthesized test id) opens a failure `{kind: FAIL|ERROR, test, module, file, line, message}`; `module`
     comes from the logger `odoo.addons.<module>.tests…`; `file`/`line` is the deepest frame under that
     module; `message` is the exception line. The tally `N failed, M error(s) of T tests` (logger
     `odoo.tests.result` or `odoo.tests.stats`, any level) sets `run`, `failed`, `errors`; several tallies add
     up.
   - **Hypothesis:** the FAIL/ERROR header and tally formats are the same on 17, 18 and 19; the parser is
     tested with lines copied from each version's `odoo/tests/result.py` / `runner.py` format strings.
3. **`--json` on the four commands.** Logs stream to stderr; stdout gets one object at the end:
   ```json
   {"command": "test", "target": "develop" | null, "db": "x", "modules": ["m"], "exit": 1,
    "duration_ms": 123456,
    "errors": [{"logger": "...", "message": "...", "exception": "ValueError: ...",
                "frames": [{"file": "...", "line": 42, "func": "_compute"}]}],
    "warnings": [{"logger": "...", "message": "..."}], "warnings_truncated": 0,
    "tests": {"run": 12, "failed": 1, "errors": 0,
              "failures": [{"kind": "FAIL", "test": "TestX.test_y", "module": "m",
                            "file": "...", "line": 88, "message": "AssertionError: ..."}]},
    "migrations": [ ... same entries the migration tracker reports ... ]}
   ```
   `tests` is present only for `test` (and for `update`/`install` when a tally was seen). A usage error with
   `--json` prints nothing on stdout and exits 2. Exit semantics are unchanged otherwise.
4. **Human mode gains one line per failing test** after the closing line:
   `echo.test.failure test=TestX.test_y module=m file=… line=88` (ERROR level), and the closing line of
   `test` carries `run= failed= errors=`.

## Implementation

- Part A (validation): `internal/cmd/modules.go`, `internal/cmd/test_remote.go`, the remote/Reverb update
  parsers, `internal/repl/repl.go` (`runModules`), tests for every rejected case.
- Part B (outcome): `internal/odoo/outcome.go` + tests; `internal/repl/` wiring of `Outcome` next to
  `runStats` for the four commands, the `--json` branch, the failure lines; `commandFlags` `--json` on the
  four; `commandDoc` notes and one `--json` example each; README section for the shape.
- `CHANGELOG.md` `[Unreleased]`: part A under `Fixed`, part B under `Added`.

## Done

Build/vet/test green, help cross-checks green, CHANGELOG entries, ledger row 144 "live verification pending"
(a failing test and a failing update on a dev stack of each major).
