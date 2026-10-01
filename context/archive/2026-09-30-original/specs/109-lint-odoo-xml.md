# Unit 109: `lint` — catch broken Odoo XML before the server does

## Goal

A local command that fails the way the server would, in milliseconds instead of
a deploy round trip:

```
echo_cli lint [<mod>...]        # default: every module in the addons paths
echo_cli lint <path/to/file.xml>
echo_cli lint --json            # machine-readable, for hooks and CI
```

Exit 0 when clean, non-zero when anything is wrong, so it works as a git hook,
as a Claude Code `PostToolUse` hook, and inside CI.

Two checks, in this order:

1. **XML well-formedness.** Catches `--` inside a comment, mismatched tags,
   stray `<` (inline JS with `if (v<0)`), duplicate attributes, multiple roots.
2. **Odoo's data-file schema.** Catches markup that parses but that the data
   loader refuses — an attribute where the grammar allows none.

This unit is the standalone command and its library. Wiring it into `deploy` as
a pre-flight is **Unit 110**, deliberately separate: it changes the contract of
a load-bearing command.

### Why this exists (the hole it closes)

Both failures below cost real deploys in one afternoon on `all_odoo`:

- A `--` inside an XML comment. The server aborts the whole registry load with
  `Double hyphen within comment`. Cost: a failed deploy (~90 s) plus the rolled
  back run.
- `t-translation="off"` written on a `<template>` tag. It parses fine and the
  loader refuses it: `Element odoo has extra content: template`. Another failed
  deploy.

Neither is a subtle bug; both are mechanically detectable offline. The reason
they landed anyway is that the only defence was a note in a handoff file saying
"never put `--` in an XML comment" — and the agent that wrote the note tripped
on it twice the same day. **A check that has to be remembered is not a check.**
That is why the hook entry point matters more than the command.

A prototype (Python, 100 lines, thrown away after this spec) ran over the
`all_odoo` repo: **356 XML files, 0.22 s, two genuine findings, zero false
positives**. Both findings were dead files that are not listed in any manifest —
which is precisely why severity has to be manifest-aware (below).

### What this does *not* claim

A clean `lint` means "this does not break on markup". It does not mean "this
loads". Semantic failure — an `xpath` with no anchor, a `ref` to a missing
xml_id, a field that is not on the model — is out of scope and needs a real
registry. Overselling a green run would make the tool actively harmful: an agent
that reads green as "safe to deploy" is no better protected against the *common*
failure than before.

## Verified ground truth

Everything below was checked against the real sources, not assumed.

**The grammar is Odoo's own, and the loader runs exactly this check.**
`odoo/tools/convert.py:772-773` (19.0):

```python
schema = os.path.join(config.root_path, 'import_xml.rng')
relaxng = etree.RelaxNG(etree.parse(schema))
relaxng.assert_(doc)
```

Whole document, every XML file reached through a manifest's `data`/`demo`. So
`xmllint --noout --relaxng import_xml.rng <file>` is a faithful reproduction of
the loader's verdict, not an approximation.

**One grammar covers 17, 18 and 19.** `odoo/import_xml.rng` fetched from the
`17.0`, `18.0` and `19.0` branches is byte-identical:

| | |
|---|---|
| size | 11,407 bytes (all three) |
| sha256 | `eca952160a2bc1c79201a34dbbc511ad5b776bc76e1e64fa89b22dfc1811a90e` |
| `include` / `externalRef` directives | zero — nothing to resolve at runtime |

**The grammar accepts an `<openerp><data>` root** (`rng:define
name="odoo_openerp_data"`, line 268, reachable from `rng:start`). A pre-v10 root
is therefore validated normally. It is not a hole in coverage and must not be
reported as one.

**Go's `encoding/xml` is weaker than libxml2.** It catches the three failures
that have actually bitten us:

| input | Go's verdict |
|---|---|
| `<!-- tiene -- adentro -->` | `invalid sequence "--" not allowed in comments` |
| `<odoo><p>x</odoo>` | `element <p> closed by </odoo>` |
| `if (v<0)` in a template | `invalid XML name: 0` |

But it silently accepts things the server rejects:

| input | Go | libxml2 (the lxml the server uses) |
|---|---|---|
| `<field name="a" name="b"/>` | accepts | `Attribute name redefined` |
| `<odoo/><odoo/>` | accepts | `Extra content at the end of the document` |
| `<?xml?>` after content | accepts | `XML declaration allowed only at the start` |

Those are false negatives: `lint` reports clean and the deploy aborts. This is
why `xmllint` is the authority and Go is the floor, not the reverse.

## Behavior

**Target selection.** With no arguments, every `.xml` under the project's
addons paths (`cfg.AddonsPaths`), skipping `.git`, `__pycache__` and
`node_modules`. With module names, only those modules. A file path is also
accepted, which is what a hook passes. Argument validation happens before any
work: an unknown module name is `ErrUsage`, never an empty run that reports
"0 problems" and looks like success.

**What the well-formedness pass covers.** Every `.xml` file, including files
under `static/` — an OWL template that does not parse is just as broken.

Sub-documents get parsed too. There are **three** shapes, not two:

| shape | handling |
|---|---|
| inline `<field name="arch"><form>…</form></field>` | already covered by the outer parse — nothing to do |
| `CDATA` body | unwrap and re-parse; invisible to the outer parse |
| entity-escaped arch (`&lt;form&gt;…`) | unescape and re-parse |

Findings inside a sub-document are reported at the **containing file's** line
number: the sub-parse's line is offset by the line where the sub-document
starts. A finding reported at "line 3 of the fragment" is useless to a hook.

**Only XML-bearing fields are re-parsed** — `arch` and `arch_db`. Those hold
view definitions the view loader really does parse as XML, so a defect in them
aborts a load exactly like one in the surrounding file.

Every other field is skipped, and `body_html` is why. It holds HTML, which Odoo
parses with `lxml.html`; an HTML parser accepts unclosed `<p>`/`<br>`, bare `%`
template directives and entities an XML parser rejects outright. Checking those
as XML produced **five false positives against two genuine findings** on the
first real repo this ran against — every one of them a mail template. A linter
that cries wolf on every mail template is one people switch off, so the rule is
narrow by construction rather than broad with exceptions.

**Severity is manifest-aware.** This is what makes the tool honest about what it
found, and it is what makes Unit 110 safe to default on:

| the file is | severity |
|---|---|
| listed in `data` or `demo` of a `__manifest__.py` | `err` |
| not listed in any manifest | `warn` |

That is the whole rule — one axis, the manifest.

The loader only ever reads manifest-listed files. A finding in a file nothing
lists is real information ("this will break the day someone adds it" — both of
the prototype's findings were exactly this) but it is not a reason to fail a
run. Without this split the lint is **stricter than the server**, which is the
one thing a pre-flight must not be.

An earlier draft carried a second axis — a `warn` for fields parsed at *render*
time rather than load time. It is gone: the honest fix for content the loader
parses differently is not to soften the severity but to **not check it as XML at
all**, which is what the XML-field rule above does. A `warn` on a mail template
would still be a false positive, just a quieter one.

**What the schema pass covers.** Files whose root element is `<odoo>` or
`<openerp>` and that live outside `static/` — the ones the data loader reads.

**Output.** One line per finding, `<path>:<line>: <message>`, through the
existing `Line` kinds (`err` / `warn` for findings by the rules above, `info`
for the summary). A closing count line, the way `compare` and `modinfo` already
end. `--json` emits `{file, line, kind, rule, message}` records plus a summary
object.

**When a validator is unavailable.** The Go pass always runs — it is compiled
in and cannot be skipped. When `xmllint` is absent, both the libxml2
well-formedness pass and the schema pass are skipped with an explicit `warn`,
and the exit code reflects only what was actually checked. Never fail a run
because a validator is missing; never claim a file is clean when half the checks
did not run. Note the degradation is easy to miss inside a hook, so the warning
must name what was skipped, not just that something was.

## Design

### `internal/odoolint` (new package)

Pure library, no terminal output, no config: takes paths, returns findings. The
command layer renders them. Three consumers will exist (command, hook,
`deploy` pre-flight), so none of them may own the logic.

```go
type Finding struct {
    File, Rule, Message string
    Line                int
    Kind                Kind   // err | warn
}

type Options struct {
    Grammar     []byte              // embedded RNG for the resolved major
    ManifestSet map[string]bool     // abs paths listed in data/demo
    Xmllint     string              // resolved binary path; "" = absent
}

type Result struct {
    Findings      []Finding
    Files         int
    SkippedPasses []string   // passes that did not run; callers MUST surface these
}

func Check(paths []string, opt Options) ([]Finding, error)
```

### Well-formedness: `xmllint` when present, Go always

- **Go `encoding/xml`** runs on every file, unconditionally. It is the floor
  that cannot be skipped on any platform Echo cross-compiles for.
- **`xmllint --noout`** runs when the binary resolves. Same library as lxml, so
  same verdict, same message, same line as the server. It catches the three
  false negatives above.

Findings from the two passes are deduplicated by `(file, line, rule)`; when both
report the same defect, the libxml2 message wins because it is the one the
server will print.

Since the schema pass already needs `xmllint`, a file that gets both passes is
one invocation: `xmllint --noout --relaxng <grammar> <file>` reports
well-formedness errors and schema errors together.

**cgo is rejected.** Go has no RelaxNG implementation and no credible pure-Go
library; cgo bindings to libxml2 would break the cross-compiled release
binaries this project ships. Shelling out is the only option that keeps the
release matrix intact.

### Where the grammar lives: one embedded file

```
internal/odoolint/grammar/
  import_xml.rng          # 11,407 bytes, covers 17 / 18 / 19
```

read with `go:embed` — the pattern `internal/cmd/connect.go:19` already uses for
its Python scripts. A `map[string]string` from major to grammar file resolves
the copy; today every supported major maps to the same file. The major comes
from **`cfg.OdooVersion`** (string, default `"18"`). An unknown major falls back
to the newest embedded grammar with a `warn` naming what it used.

One file, not a directory per major, because the three majors that matter are
byte-identical (verified above). The map is the extension point: the day a major
diverges, a second file drops in and one map entry changes. A directory per
major today would be three copies of one file pretending to be version support.

Embedding rather than depending on a local Odoo checkout:

1. **It is small and self-contained.** 11,407 bytes, zero `include` /
   `externalRef`. Nothing to resolve at runtime.
2. **It barely moves.** Byte-identical across three majors and four years.
3. **A dependency would fail silently where it matters.** Requiring a local
   source tree means a config table of paths per major, and a tool that quietly
   drops to well-formedness-only on every machine that never downloaded Odoo —
   which is exactly the machine where nobody notices the gap.
4. **The alternative is worse than it looks.** Echo *could* fetch the grammar
   from the target instance (`docker.CopyFromContainer` already exists) and that
   is strictly more correct — the grammar of the instance you are deploying to.
   But it needs a reachable instance, which a hook running on every file save
   does not have, and it makes an offline check depend on the network.

The refinement worth keeping in mind, not building now: an optional
`[lint] grammar_source = "instance"` that fetches and caches under
`~/.config/echo/`, for the day a major changes the grammar before Echo ships an
updated copy. Since the embedded copy is what runs by default, being one Odoo
release behind degrades to "validated against the previous major's grammar",
which is a warning, not a wrong answer.

### Manifest reading

A minimal `__manifest__.py` reader collects the `data` and `demo` lists per
module and resolves them to absolute paths. This is deliberately *not* the full
manifest cross-check (see Out of scope) — it computes the severity set and
nothing else. It is a few lines over files the walker already visits, and
without it the severity rules above cannot exist.

### `internal/cmd/lint.go`

Standard command shape: `Run(ctx, cfg, args) (<-chan Line, error)`. Parses args
(`--json`, module names, file paths), resolves targets against
`cfg.AddonsPaths`, resolves the grammar and the manifest set, calls
`odoolint.Check`, streams findings, closes the channel. Registered in
`commandFlags["lint"]`, the REPL help and the autocomplete registry.

`lint` lives in the REPL as well as the CLI. The REPL's value for a pass/fail
check is lower than for the streaming commands, but every other command is in
both and the asymmetry costs more than the three registry lines.

## Implementation order

Each phase is independently useful and independently shippable.

1. **Go well-formedness pass + walker + sub-document handling.** No external
   dependencies. Covers 100% of the failure that actually cost deploys.
2. **`xmllint` well-formedness pass.** Closes the three false negatives.
3. **Embedded grammar + schema pass.** Covers the `t-translation` failure.
4. **Manifest-aware severity.** Makes the output honest, and unblocks Unit 110.
5. **Command layer + registration.**

## Out of scope

- **`deploy` pre-flight.** Unit 110. This unit provides the library it calls.
- **Full manifest cross-checks.** Files listed in `__manifest__.py` that do not
  exist on disk, and `.xml` files no manifest lists. This unit reads manifests
  only to compute severity. The "listed but missing" check aborts the load and
  is cheap, but it is a different kind of check (project structure, not markup)
  and belongs in its own unit — arguably the next one after 110.
- **View-arch validation.** Odoo validates a view's `arch` against a second set
  of grammars (`odoo/addons/base/rng/<type>_view.rng`, used by
  `odoo/tools/view_validation.py:299`) — list, search, calendar, pivot, graph
  and activity. This is where the agent-relevant wins are, since a mistyped
  list/search arch is a routine agent error. It is genuinely bigger to embed
  (`common.rng` alone is 20.5 KB and the per-type files include it), and note
  there is **no** `form_view.rng`: form arch is validated in Python, so that
  layer would never cover forms. Doing it here would double the unit.
- **Anything semantic.** A `ref` to an xml_id that does not exist, an `xpath`
  with no matching anchor, a misspelled model or field. These need a real
  registry: that is what `-u` and an LSP are for. Promising them would make the
  tool lie about what a clean run means.

## Tests

- `odoolint.Check` on fixtures, one per rule: `--` in a comment; `--` inside a
  `CDATA` body; `--` inside an entity-escaped arch; mismatched tags; stray `<`
  from inline JS; duplicate attribute (`xmllint`-only); two roots
  (`xmllint`-only); a valid file; `t-translation` on `<template>` (schema pass);
  an `<openerp><data>` root (validates normally, no warning).
- Sub-document line offset: a finding inside a `CDATA` block at fragment line 3
  reports the containing file's line, not 3.
- Severity: the same defect in a manifest-listed file is `err` and in an
  unlisted file is `warn`.
- Field scope: a broken comment in a `CDATA` `arch` is caught; the same in a
  `body_html` (HTML, parsed by `lxml.html`) is not reported at all.
- Grammar selection: majors 17, 18 and 19 all resolve to the embedded grammar;
  an unknown major → newest with a warning; the embedded file parses as XML (a
  guard against a corrupt vendored copy) and matches the recorded sha256.
- Dedup: a defect both passes catch is reported once, with the libxml2 message.
- `xmllint` absent: Go findings survive, a warning naming the skipped passes is
  emitted, and a file whose only problem is schema-level reports clean rather
  than reporting a false error.
- `parseLintArgs`: `--json`; a module name; a file path; an unknown module →
  `ErrUsage`.
- Command layer: findings arrive as `err`/`warn` lines and the summary as
  `info`; exit status maps from the `err` count only.

## Verify when done

- `echo_cli lint` over `all_odoo` reproduces the prototype's result: the two
  known dead files, nothing else — and both as `warn`, not `err`, because no
  manifest lists them. Exit 0. *(Verified: 356 files, 0.27 s, `errors=0
  warnings=2` — `ccima_crm_reassign/views/product_overlay_templates.xml:169`,
  a stray `<` from inline JS, and
  `pragtech_crm_facebook_leads/views/facebook_dashboard_view.xml:57`, content
  after the root element.)
- Reintroducing each of the two real failures (the `--` comment, the
  `t-translation` attribute) in a manifest-listed file is caught locally, with
  the same message the server gives, as `err`, exit non-zero.
- A duplicate attribute in a manifest-listed file is caught (proves the
  `xmllint` pass is wired, since Go accepts it).
- A single module finishes well under a second, so a hook on every file write
  is not felt. *(Verified: `lint ccima_crm_reassign`, 0.03 s.)*
- `echo_cli lint --json | jq` gives one record per finding.
- With `xmllint` renamed out of `PATH`: the `--` comment is still caught, the
  duplicate attribute and the `t-translation` are not, a warning names both
  skipped passes, and exit code reflects only the Go pass.
