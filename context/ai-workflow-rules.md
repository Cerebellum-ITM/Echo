# AI Workflow Rules

## Approach

Build Echo incrementally, one spec unit at a time. Context files define what
to build, how to build it, and the current state of progress. Always implement
against the specs — do not infer or invent behavior not defined in the context files.

## Scoping Rules

- Work on one spec unit at a time; do not bleed into adjacent units
- Prefer small, verifiable increments over large speculative changes
- Do not combine unrelated system boundaries in a single implementation step
- Do not install new Go dependencies unless required by the current unit

## When to Split Work

Split an implementation step if it combines:

- Theme/styling changes AND command logic changes
- Multiple unrelated command groups in the same PR
- REPL changes AND config detection changes
- Any behavior not clearly defined in the active spec

If a change cannot be verified end to end in one terminal session, the scope is too broad — split it.

## Handling Missing Requirements

- Do not invent command behavior not listed in `project-overview.md`
- If a command's exact Docker/Odoo invocation is ambiguous, add an open question
  to `progress-tracker.md` and ask the user before implementing
- If Odoo version differences for a command are unknown, implement for v18 first
  and mark v17/v19 variants as TBD in a comment

## Protected Files

Do not modify the following unless explicitly instructed:

- `DESIGN_TOKENS.md` — reference only; do not import or parse it at runtime
- `go.sum` — managed by `go mod tidy`, never edit by hand
- Any generated `.pb.go` files if protobuf is added later

## Keeping Docs in Sync

Update the relevant context file whenever implementation changes:

- Package boundaries or folder structure → `architecture.md`
- New color token usage or layout decisions → `ui-context.md`
- New Go conventions established → `code-standards.md`
- Scope additions or removals → `project-overview.md`

## Before Moving to the Next Unit

1. The unit works end to end in a real terminal session (not just `go build`)
2. No invariant from `architecture.md` was violated
3. `go build ./...` passes with no errors
4. `go vet ./...` produces no warnings
5. `progress-tracker.md` is updated to reflect the completed unit
