## Application Building Context

Read the following files in order before implementing or making any
architectural decision:

1. `context/project-overview.md` — product definition, goals, features, and scope
2. `context/architecture.md` — system structure, boundaries, storage model, and invariants
3. `context/ui-context.md` — theme, colors, typography, and component conventions
4. `context/code-standards.md` — implementation rules and conventions
5. `context/ai-workflow-rules.md` — development workflow, scoping rules, and delivery approach
6. `context/progress-tracker.md` — current phase, completed work, open questions, and next steps

Update `context/progress-tracker.md` after each meaningful implementation change.

If implementation changes the architecture, scope, or standards documented in
the context files, update the relevant file before continuing.

---

## Workflow Rules (Spec-Driven Dev)

This project uses the `spec-driven-dev` skill. Follow these rules in every session:

1. **Before implementing any new feature**, run `/spec-driven-dev spec NN nombre`
   to generate a spec file in `context/specs/`. Do not write feature code without
   an approved spec.
2. **After completing a unit**, run `/spec-driven-dev update progress` to mark
   it complete and record decisions/session notes.
3. **If architecture, scope, or standards change**, run
   `/spec-driven-dev update <archivo>` to refresh the affected context file
   before continuing implementation.
4. **At the start of each session**, run `/spec-driven-dev status` to re-enter
   context: current phase, in progress, next unit, open questions.
5. When asked to implement an existing spec, read `context/specs/NN-name.md` and
   implement exactly what is specified — no more, no less.
