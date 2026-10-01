## Context

@context/README.md

This repo keeps its working context in a `ctx` store (`context/`). Before any new
feature, write its spec with `/ctx spec build NN-name` and wait for approval;
implement an approved spec exactly. Close every session with `/ctx close`.

---

## Versioning & Changelog

This project follows [Semantic Versioning](https://semver.org/) and maintains a
`CHANGELOG.md` in the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
format. Two non-negotiables:

1. **Every meaningful change appends to `[Unreleased]`** in `CHANGELOG.md`,
   under the appropriate section (`Added` / `Changed` / `Fixed` / `Removed` /
   `Deprecated` / `Security`). This entry is part of the same commit as the
   code change.
2. **Every version bump promotes `[Unreleased]` to a new `[X.Y.Z]` section**
   in the same commit that updates the version constant
   (`Version = "..."` in `internal/repl/repl.go`). Never bump the version without
   moving the `[Unreleased]` entries into the new release block — and never
   leave the bump in a separate commit from its changelog promotion.

The `commitcraft` skill enforces the per-commit changelog rule on its end;
this section codifies it on the project's side as well.
