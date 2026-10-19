# Unit 140 · command-help
> Spec for one unit of [the plan](plan.md). Implement exactly this: no more, no less. Last verified: 2026-10-19.

## Goal

An agent (or a person) learns how any command works from the binary alone: `echo_cli help <cmd>`,
`echo_cli <cmd> --help` and `echo_cli help <cmd> --json` print one complete page per command (usage, every flag,
notes, examples), `echo_cli help agents` prints the headless contract, and `echo_cli help <text>` searches.
Nobody needs the repo or the 1,600-line README to drive Echo.

## Design

Why: measured on `main` (`8643a1d`) on 2026-10-19: `echo_cli help deploy` ignores its argument and prints all
307 lines (exit 0); `echo_cli deploy --help` fails with `usage error: unknown flag: --help`; the help rows are
hand-written strings, so flags drift (`deploy` accepts `--set-checkpoint`, `--consume-checkpoint`, `--no-test`,
`--rename`, … that no row names, and `sideload`'s rows sit under `view`). [code-standards](../../knowledge/code-standards.md)
lists "a parser accepting a flag missing from `commandFlags`" as not enforced; this unit enforces the next link:
every `commandFlags` entry is documented.

Decisions (the implementer makes none):

1. **One source of truth.** A `commandDoc` per command replaces the `helpEntry` rows of `helpSections()`:
   `name`, `section`, `usage` (e.g. `deploy [--from <target>] [--commits <shas> | --modules <names> | --auto]`),
   `summary` (one line, today's description), `subs []helpItem` (subcommands such as `checkpoint create`,
   `link --add` stays a flag), `flags []helpItem`, `notes []string`, `examples []helpExample`.
   `helpItem{name, arg, desc}`: `name` is exactly one flag or subcommand (`--rollback-on-fail` and
   `--no-rollback-on-fail` are two items; no `a / b` or `a/b` rows), `arg` the value placeholder
   (`<target>`, `[=db|dump]`) or empty. `helpExample{cmd, desc}`: `cmd` without the `echo_cli` prefix.
   Sections keep today's titles and order: Project, Modules, Database, Shell, Docker, Session. `helpSections()`,
   the sequence picker's description (`sequence.go`) and `helpCommandNames()` are derived from the docs.
2. **Topics.** The three footer blocks become topics, plus a new one: `scripting`, `build`, `reverb`, `agents`.
   A topic is a title plus `[]helpItem` rows (the existing `helpEntry` text, unchanged except `agents`). Topic
   names never collide with a `Registry` name (a test asserts it).
3. **Forms of `help`.**
   - `help` (no argument): interactive pager when it opens today (REPL, or one-shot with stdin and stdout TTYs),
     one page per section with each command's summary, subs and flags (the same look as today, now complete),
     then one page per topic. Flat (non-TTY, recipe): an **index** only: per section, one line per command
     (`usage`-free name + summary), then the topic names, then the footer line
     `help <command> for flags and examples · help agents for headless use · help <text> to search`.
   - `help <cmd>` (exact `Registry` name, or `exit`/`quit`): the command page, always flat, never the pager.
   - `help <topic>`: the topic page, flat.
   - `help <text>` (anything else): search, below.
   - `--json` anywhere among `help`'s args switches any of the forms above to JSON on stdout (decision 6).
     Any other argument beyond the first, or an unknown flag, → `warn` + exit 2.
4. **Command page layout** (flat, through `sess.print`, so `ECHO_LOG_FORMAT=json` and captures behave as for any
   output): title line `<name> — <summary>` in Accent; `Usage` header then `  echo_cli <usage>`; `Subcommands`
   (when any) and `Flags` blocks rendered with `renderHelpEntries` (label in Info, desc in Out); `Notes` as
   `  - <note>` lines; `Examples` as `  echo_cli <cmd>` (Info) followed by `      <desc>` (Faint); blank lines
   between blocks; empty blocks omitted. Last line: `  see also: help agents` for commands that have `--json`,
   `--from` or `--remote`.
5. **`<cmd> --help` and `<cmd> -h`.** Both print the page of `<cmd>` and exit 0, with no project needed.
   `--help` is recognised in any position before a literal `--`; `-h` only as the sole argument. Exception: for
   the passthrough commands `bash`, `psql`, `shell`, `shell-run`, `connect` and `logs`, only the first argument
   counts (so `shell-run "… --help"` and `psql -c '…' --help` reach the wrapped tool). One function,
   `repl.HelpRequest(args []string) (cmd string, ok bool)` (args = command name + its args), decides; the REPL
   dispatcher calls it before the build-mode intercept, and `main.go` calls it before project detection and
   rewrites the run to `help <cmd>` (projectless, like `--help` alone today). `--build` combined with `--help`:
   help wins.
6. **JSON** (stdout only, one object, `encoding/json` with two-space indent; nothing else on stdout):
   - index: `{"version": "<FullVersion>", "sections": [{"title", "commands": ["init", …]}], "commands": [<doc>…], "topics": ["scripting", "build", "reverb", "agents"]}`
   - command: `<doc>` = `{"name", "section", "usage", "summary", "subcommands": [<item>], "flags": [<item>], "notes": [string], "examples": [{"cmd", "desc"}], "remote": bool, "json_output": bool}`,
     `<item>` = `{"name", "arg", "desc"}`; empty lists are `[]`, never `null`. `remote` = the command accepts
     `--from` or `--remote`; `json_output` = it accepts `--json`.
   - topic: `{"topic", "title", "rows": [<item>]}` (a topic row's `name` is its left column).
   - search: `{"query", "matches": [{"command", "item", "desc"}]}` (`item` empty for a command-level match).
7. **Search.** Case-insensitive substring of the query against: command names and summaries, sub and flag names
   and descs, topic names and row text. Output: one line per match, `<command> <item>` padded, then the desc;
   command-level matches first, then items, each group in `Registry` order; topic matches as `help <topic>`.
   At least one match → exit 0. None → `warn: no help matches "<text>"; run help for the index`, exit 2.
8. **Content rules** for the docs (this is the bulk of the unit):
   - Every command in `Registry` except `exit`/`quit` has a doc; `exit`/`quit` share one doc named `exit`
     (`help quit` shows it).
   - Every flag in `commandFlags[cmd]` appears as a flag item of that command's doc, and no flag item is
     missing from `commandFlags[cmd]`, except `-E` and `--env` (deprecated, deleted by Unit 122): they are not
     documented per command; `help reverb` keeps its `-E` rows.
   - **Required** notes (≥ 1) and examples (≥ 1, ≤ 3) for every command with `--from`, `--remote` or `--json`
     in `commandFlags`, plus `promote` and `sequence`. Optional elsewhere.
   - Notes say what a caller cannot guess: prerequisites (link, profile, git-deploy target), what runs without a
     TTY and what exits 2 (`ErrNonInteractive`), prod-stage gates and which flag skips them, what goes to stdout
     with `--json`, which flags only configure and exit (`--set-push`, `--test-toggle`, …), and destructive
     effects. Source the facts from the README section of the command and the owning knowledge file
     ([deploy](../../knowledge/deploy.md), [deploy-safety](../../knowledge/deploy-safety.md),
     [remote-targets](../../knowledge/remote-targets.md), [database](../../knowledge/database.md),
     [scripting](../../knowledge/scripting.md)); when they disagree with the code, the code wins.
   - Examples are headless where the command allows it (an agent copies them) and use dummy names
     (`staging`, `sale_custom`); no real hosts, clients or databases.
   - Descriptions stay one line each; style follows the existing rows (imperative, no trailing period).
9. **`help agents` content** (rows, in this order): run one-shot as `echo_cli <cmd> [args]`; `-C <dir|alias>`
   from outside the project; stdout carries only the `--json` payload, logs go to stderr; `ECHO_LOG_FORMAT=json`
   makes every log line one JSON object; exit codes 0 ok, 1 error or ERROR/CRITICAL lines, 2 usage or a picker /
   confirm needed without a TTY, 3 cancelled; without a TTY every picker needs its flag (`--commits`,
   `--modules`, `--auto`, `<mod...>`) and every prod confirm needs `--force`; `--dry-run` / `--save-plan` /
   `--apply` for review-then-run; `run <file>` and `sequence --last` for multi-step; `logview --json` and
   `report` to read past runs; `help <cmd> --json` for a command's machine-readable doc.
10. **Unchanged:** the pager's look and keys ([ui](../../knowledge/ui.md)); `help` stays a meta command
    (`isMetaCommand`); the unknown-command `warn` still points at `help`; the README is not trimmed.

## Implementation

### `internal/repl/helpdocs.go` (new)

- Types `commandDoc`, `helpItem`, `helpExample`, `helpTopic`; `var commandDocs = []commandDoc{…}` in `Registry`
  order; `var helpTopics = []helpTopic{…}` (moves `scriptingHelpEntries`, `buildHelpEntries`,
  `reverbHelpEntries` here and adds `agents`).
- Lookups: `docFor(name) (commandDoc, bool)` (maps `quit` to `exit`), `topicFor(name)`.
- `helpSections()` rebuilt from `commandDocs` (summary row, then subs and flags as indented rows), so the pager
  and the Registry cross-check keep working.

### `internal/repl/help.go` (new; `runHelp` and `printHelpFlat` move here from `repl.go`)

- `runHelp(args)`: parse `--json` and the single positional; route to pager / index / command / topic / search
  (decision 3). Exit codes through `sess.exitCode`.
- `printCommandPage`, `printTopicPage`, `printHelpIndex`, `printHelpSearch`, and their JSON twins.
- `HelpRequest(args)` (exported for `main.go`) and the passthrough set of decision 5.

### `internal/repl/repl.go`, `helppager.go`, `sequence.go`, `main.go`

- Dispatcher: `case "help": sess.runHelp(args)`; before the build-mode intercept, `if c, ok := HelpRequest(…)`
  → `sess.runHelp([]string{c})`.
- `helppager.go` `helpPages()` iterates `helpSections()` then `helpTopics`.
- `sequence.go` description lookup reads `commandDocs` summaries.
- `main.go`: replace the `--help`/`-h` normalization with `HelpRequest`; `help` stays in `projectlessOneShot`.

### Tests (`internal/repl/help_test.go`, `registry_test.go`)

- Every `Registry` name has a doc (exit/quit via `exit`); docs are in `Registry` order; topic names ∉ `Registry`.
- Flag coverage both ways against `commandFlags` (decision 8, `-E`/`--env` exempt); no item name contains `/`
  or a space.
- Required notes/examples for the commands of decision 8; every example's first token is the doc's name, and
  every `-`-prefixed token in it is a known flag of that command (or `universalFlags`).
- `HelpRequest` table: `deploy --help`, `deploy --from x --help`, `deploy -h`, `deploy -h x` (no),
  `shell-run "x --help"` (no), `psql --help` (yes, first arg), `deploy -- --help` (no), `help` itself (no).
- JSON: `help --json`, `help deploy --json`, `help agents --json`, `help checkpoint --json` decode into the
  shapes of decision 6 with no `null` lists; search for `checkpoint` returns `deploy --no-checkpoint` and
  `checkpoint`; `help zzz` exits 2.

### Store and docs

- [code-standards](../../knowledge/code-standards.md#new-command-wiring): step 5 becomes "add a `commandDoc`
  (flags must match `commandFlags`; notes and examples required for remote/`--json` commands)"; the enforcement
  list gains the flag-coverage and example tests.
- [ui](../../knowledge/ui.md): the flat `help` is an index; command and topic pages are flat; the JSON forms.
- [scripting](../../knowledge/scripting.md): `<cmd> --help` and `help <cmd> --json` as the agent entry point.
- README: a short "Getting help" paragraph under Quick start (the four forms). `CHANGELOG.md` `[Unreleased]`:
  `Added` (per-command help, `--help`, `help agents`, search, `--json`) and `Changed` (flat `help` is an index).
- `messages/REQUEST-odoo-probe-command-help.md` (`/ctx request odoo-probe command-help`): the `odoo-probe`
  skill should tell agents to run `echo_cli help <cmd>` / `help agents` (and `--json`) before guessing flags or
  reading the Echo repo, once this unit is released. Written when the unit is done, not before.

## Dependencies

- none (`encoding/json` only).

## Verify when done

- [ ] `echo_cli help deploy </dev/null` prints only the deploy page, lists every `commandFlags["deploy"]` flag
      but `-E`/`--env`, has Notes and Examples; exit 0.
- [ ] `echo_cli deploy --help` and `echo_cli deploy --from x --help` from `/tmp` (no project) print the same page,
      exit 0; `echo_cli shell-run "print(1) --help"` is not intercepted.
- [ ] `echo_cli help --json | jq '.commands | length'` equals `len(Registry) - 1`; `echo_cli help sideload --json`
      validates against decision 6; stdout of every `--json` form is one JSON document with no log lines.
- [ ] `echo_cli help checkpoint` (non-TTY) shows the checkpoint page; `echo_cli help rollback` searches and
      lists `deploy --rollback` and its siblings; `echo_cli help zzz` exits 2.
- [ ] `echo_cli help` in a TTY opens the pager with the complete flag rows plus the four topic pages;
      `echo_cli help | cat` prints the index (≤ 80 lines). Checked in tmux (`tui-probe`).
- [ ] A fresh agent with only `echo_cli help agents` and `echo_cli help deploy` composes a correct headless
      `deploy --from <t> --modules <m> --dry-run --json` without opening the repo (manual check, recorded in the
      [ledger](../../knowledge/operations.md#live-verification-ledger)).
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; changed files `gofmt`-clean; the Registry/help/flag
      cross-checks pass; `CHANGELOG.md` updated.
