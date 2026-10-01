# Unit 111: manifest cross-check — the other half of "what will the loader refuse"

## Goal

`lint` gains two checks that compare a module's `__manifest__.py` against what
is actually on disk, in both directions:

1. **Listed but missing.** A path in `data` or `demo` that does not exist. The
   loader opens manifest entries in order and aborts the registry load on the
   first one it cannot read — the same class of failure Units 109 and 110
   already cover, arriving through a different door.
2. **Present but unlisted.** A data-shaped `.xml` that no manifest mentions.
   Nothing loads it, so it cannot break anything today; it explains *why* a
   finding in such a file is inert, and it is how a file that was meant to be
   wired up gets noticed before someone wonders why their view never appeared.

No new command, no new flag. Both run inside the existing `lint`, and therefore
inside `deploy`'s pre-flight, with no extra wiring.

## Why this belongs with 109/110 rather than in them

Unit 109 already parses every manifest's `data`/`demo` lists to decide severity
— that work is done and thrown away. This unit reads the same data in the
opposite direction, which is why it is cheap: the set of listed paths is
already in hand, and the two checks are the two set differences against disk.

It stayed out of Unit 109 because it is a different *kind* of check — project
structure, not markup — and mixing them would have made that unit's contract
("this file does not break on markup") untrue. Now that the severity model
exists, the structural checks slot into it without changing it.

## Behavior

**Scope: the module directories already in scope.** Whole-tree run → every
module under the addons roots. Named modules → those modules. An explicit file
path → the module that contains it, when there is one. Nothing new is walked.

### Check 1 — listed but missing

| | |
|---|---|
| rule | `manifest-missing` |
| severity | **always `err`** |
| reported at | the `__manifest__.py` line where the entry appears |
| message | `listed in the manifest but not on disk: <rel path>` |

Severity is unconditional, and this is the one place where the manifest-aware
model from Unit 109 does not apply — it *cannot*. That model asks "does a
manifest list this file?" to decide whether the loader will read it; here the
answer is yes by construction, and the file's absence is precisely the defect.
A `warn` would be wrong: this aborts the load every time.

Every listed entry is checked, not only `.xml` — a missing
`security/ir.model.access.csv` aborts the load exactly the same way.

The finding is anchored to the manifest line rather than to the phantom file,
because that is the line the user has to edit. A finding pointing at a path
that does not exist is not navigable.

### Check 2 — present but unlisted

| | |
|---|---|
| rule | `manifest-unlisted` |
| severity | **always `warn`** |
| reported at | line 1 of the file |
| message | `no manifest lists this file; the loader never reads it` |

Never `err`, under any circumstance. An unlisted `.xml` is routine during
development — a file being written, a fixture, something parked — and blocking a
deploy on it would make the pre-flight unusable within a day. This check earns
its place by explaining inertness, not by gating anything.

To keep it quiet enough to leave on, a file qualifies only when it is
**data-shaped**: an `<odoo>` or `<openerp>` root element. That excludes OWL
`<templates>` files, which reach Odoo through the assets bundle rather than
through `data` and would otherwise all be flagged. Also skipped by location:
`static/`, `tests/`, `migrations/` and `i18n/`, none of which the data loader
reads by manifest listing.

### Interaction with the existing findings

The two new rules join the same sorted, deduplicated finding list, render
through the same log lines and JSON records, and count into the same
`errors`/`warnings` summary. `deploy`'s pre-flight blocks on
`manifest-missing` because it is an `err`, and ignores `manifest-unlisted`
because it is a `warn` — no pre-flight code changes at all.

## Design

`Options` gains one field, so the checks stay opt-in at the library boundary and
`Check` remains the single entry point (one sorted result, one dedupe pass):

```go
type Options struct {
    Grammar      []byte
    ManifestSet  map[string]bool
    Xmllint      string
    ManifestDirs []string   // module dirs to cross-check; nil = markup only
}
```

`ManifestFiles` becomes `ManifestEntries`, returning `[]ManifestEntry{Path,
Line}` — the line number is new, and it is what makes check 1 actionable.
`ManifestSet` keeps its signature and is built from the same entries.

`applySeverity` must **not** rewrite these two rules' `Kind`. Both carry their
severity from the rule itself, for the reasons above, so the manifest rules are
exempted explicitly rather than by accident of ordering.

## Tests

- `manifest-missing`: a `data` entry with no file on disk → `err`, anchored to
  the manifest's own line number, message naming the relative path.
- A missing non-XML entry (`security/*.csv`) is caught too.
- `manifest-unlisted`: a data-shaped `.xml` no manifest lists → `warn`.
- Never escalates: the unlisted finding stays `warn` even when other findings
  in the same module are `err`.
- Quiet by construction: an OWL `<templates>` file outside `static/` is not
  flagged; files under `static/`, `tests/`, `migrations/` and `i18n/` are not
  flagged.
- A fully consistent module produces neither finding.
- `ManifestEntries` line numbers: entries on distinct lines report distinct
  lines; a single-line list still resolves.
- `applySeverity` leaves both manifest rules' `Kind` untouched.
- Command layer: `lint` reports both rules, `--json` carries them, and the
  summary counts them.
- `deploy` pre-flight blocks on `manifest-missing` with no pre-flight changes,
  and does not block on `manifest-unlisted`.

## Verify when done

- `echo_cli lint` over `all_odoo` still exits 0, with the two known
  well-formedness warnings plus however many `manifest-unlisted` warnings the
  repo genuinely has — and **zero** `manifest-missing`, since the repo deploys
  today. A `manifest-missing` there would be a live bug, not a lint artifact.
  *(Verified: 356 files, `errors=0 warnings=16` — the 2 known `xml-syntax` plus
  14 `manifest-unlisted`, zero `manifest-missing`. All 14 are genuine orphans,
  including a literal `new_note_view (copy).xml` and three `odoo scaffold`
  leftovers in `apiccima/`. One of them is
  `ccima_crm_reassign/views/product_overlay_templates.xml` — the same file
  carrying a markup finding, so the report now explains why that defect is
  inert instead of leaving the reader to wonder.)*
- Deleting a file a manifest lists makes `lint` report `manifest-missing` at the
  manifest's line and `deploy` refuse the run, before any remote contact.
  *(Verified: `lint` exits 1 with the finding anchored to `__manifest__.py`, and
  `deploy --modules demo_mod` blocks without reaching `reading remote profile`.)*
- Adding an unlisted `.xml` to a module produces exactly one `warn` and does not
  change the exit code. *(Verified: one WARNING, exit 0.)*
- Runtime stays in the same order of magnitude: the checks read manifests that
  Unit 109 already reads and stat files it already walked. *(Verified: CPU time
  over `all_odoo` unchanged at ~0.20 s user+sys.)*
