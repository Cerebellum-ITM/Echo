# REQUEST · command-help
> From Echo (Unit 140) to the `odoo-probe` skill (`~/Documents/Projects/odoo-probe`). Written 2026-10-19. Status: open.

## What is asked

Tell agents to learn an Echo command from the binary before guessing flags or reading the Echo repo:
`echo_cli help <command>` (or `echo_cli <command> --help`), `echo_cli help agents` for the headless contract,
`echo_cli help <text>` to search, and `--json` on any of them for a machine-readable form.

## Why

Agents kept opening the Echo repo or the 1,600-line README to find a flag, and `db-pull --help` used to run the
pull instead of printing help. Unit 140 makes every command's help complete (every accepted flag, notes on TTY
and prod gates, examples) and reachable from anywhere.

## Already verified

- `help <cmd>`, `<cmd> --help`, `help agents`, search and `--json` work outside a project on a binary built from
  `feature/command-help` (2026-10-19, [ledger](../knowledge/operations.md#live-verification-ledger) row 140).
- Help flags are tested against `commandFlags` both ways (`internal/repl/help_test.go`).

## Done looks like

- [ ] The skill's command-reference part points to `echo_cli help <cmd>` / `help agents` first and keeps only
      what help does not say (stage gates of the skill itself, write-mode permissions).
- [ ] Applied only after the Echo release that ships Unit 140 is installed as `echo_cli`.

## Do not

- Do not copy the help text into the skill; it would drift from the binary.
