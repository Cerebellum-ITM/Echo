# Unit 36: loose-severity log fallback

## Goal

Reformat streamed subprocess lines that carry a bare severity prefix —
e.g. wkhtmltopdf's `Warn: Can't find .pfb for face 'Courier'` or
`Error: Failed loading page` — into Echo's Odoo log style, so they stop
appearing as raw, unstyled `out` text and instead read like every other
line (timestamp + level chip + logger + message). A loose `Warn:` counts
toward the run's warning total; a loose `Error:` is **not** counted as a
failure (it never flips a healthy run to ✗), so a noisy tool's stderr
can't fail an update that actually finished.

## Design

Echo already reformats foreign lines into its Odoo style for one case:
`docker compose` lifecycle progress (`parseComposeProgress` →
`emitOdooLog("docker.<resource>", …)`, wired inline in `runDocker`,
[repl.go:514](../../internal/repl/repl.go)). Lines that match neither the
Odoo format ([loglevel.go](../../internal/repl/loglevel.go) `odooLogPrefix`),
the loguru format, nor a compose progress line fall through to
`classifyOdooLog` → `"out"` and render in the default foreground —
which is exactly how the `Warn: …` line leaks through today.

This unit adds a second foreign-line reformatter, parallel to the compose
one, for **loose-severity** lines: a line whose first token is a bare
severity keyword followed by `:`. These come almost exclusively from the
PDF report engine (wkhtmltopdf / Qt) writing to stderr, so the
reformatted line uses a single synthetic logger, `report.wkhtmltopdf`,
which is honest about the dominant source and easy to retune later.

**Severity mapping** (case-insensitive first token, then `:`):

| Prefix token(s)              | Odoo level |
| ---------------------------- | ---------- |
| `Warn`, `Warning`            | `WARNING`  |
| `Err`, `Error`               | `ERROR`    |
| `Crit`, `Critical`, `Fatal`  | `CRITICAL` |
| `Info`                       | `INFO`     |
| `Debug`                      | `DEBUG`    |

**Traceback safety.** A bare `Error: …` can also be the tail of a Python
traceback that Echo currently groups via err-kind inheritance
(`classifyOdooLog`'s `previousKind` path). To avoid hijacking such a line
out of its traceback and misattributing it to `report.wkhtmltopdf`, the
reformat is **suppressed while inside an err/warn inheritance chain** —
i.e. when the previous classified line was `err`/`warn`, the line falls
through to the existing inheriting `classify` path instead.

**Counting.** Only a loose `WARNING` increments `runStats.warnings`; loose
`ERROR`/`CRITICAL` lines are reformatted and colored (red chip) but do
**not** increment `runStats.errors`, per the decision that subprocess
stderr must not fail an otherwise-successful run. This mirrors how the
compose path counts, added in `runStats.wrap`.

**Scope.** Wired into the two streaming paths where these lines actually
appear: `runModules` (update/install/uninstall/test) and `runDocker`
(`logs`, lifecycle). The compose reformat, currently inline in
`runDocker`, moves into a shared `sess.emitStreamLine` helper so both
paths apply both reformatters consistently. Other command streams (i18n,
db, shell, connect) are out of scope.

## Implementation

### `parseLooseSeverity` (`internal/repl/looselog.go`, new)

```go
// looseSeverity matches a line whose first token is a bare severity
// keyword followed by ':' — the shape wkhtmltopdf/Qt write to stderr
// (e.g. "Warn: Can't find .pfb for face 'Courier'"). Case-insensitive.
// Odoo/loguru lines start with a timestamp and never match.
var looseSeverity = regexp.MustCompile(
    `^(?i)(warning|warn|critical|crit|fatal|error|err|info|debug):\s*(.*)$`,
)

// looseSeverityLogger is the synthetic logger the reformatted line is
// attributed to. These loose lines come, in practice, from the PDF
// report engine's stderr; the name is honest about that dominant source
// and trivially changeable.
const looseSeverityLogger = "report.wkhtmltopdf"

type looseLine struct {
    level   string // mapped Odoo level: DEBUG/INFO/WARNING/ERROR/CRITICAL
    message string
}

func parseLooseSeverity(line string) (looseLine, bool) {
    m := looseSeverity.FindStringSubmatch(line)
    if m == nil {
        return looseLine{}, false
    }
    return looseLine{level: looseLevel(m[1]), message: m[2]}, true
}

// looseLevel maps a bare severity keyword to an Odoo log level.
func looseLevel(kw string) string {
    switch strings.ToLower(kw) {
    case "warn", "warning":
        return "WARNING"
    case "err", "error":
        return "ERROR"
    case "crit", "critical", "fatal":
        return "CRITICAL"
    case "info":
        return "INFO"
    case "debug":
        return "DEBUG"
    }
    return "INFO"
}
```

### `sess.emitStreamLine` (`internal/repl/repl.go`)

A shared streamed-line renderer that folds in both foreign-line
reformatters, then the kind-based classifier:

```go
// emitStreamLine renders one streamed subprocess line. Foreign lines that
// Echo can normalize — docker compose progress, and loose-severity stderr
// (Warn:/Error: … from tools like wkhtmltopdf) — are reformatted into the
// Odoo log style; everything else goes through the kind classifier (which
// also keeps traceback continuations grouped via err/warn inheritance).
func (sess *session) emitStreamLine(lc *logColorer, line string) {
    if cl, ok := parseComposeProgress(line); ok {
        emitOdooLog(cl.level, "docker."+cl.resource, cl.state,
            []logField{{"name", cl.name}},
            sess.styles, sess.palette, sess.cfg.DBName)
        return
    }
    // Don't hijack a line out of an active traceback (err/warn inheritance).
    if lc.last != "err" && lc.last != "warn" {
        if ll, ok := parseLooseSeverity(line); ok {
            emitOdooLog(ll.level, looseSeverityLogger, ll.message, nil,
                sess.styles, sess.palette, sess.cfg.DBName)
            return
        }
    }
    sess.print(Line{Kind: lc.classify(line), Text: line})
}
```

- `runModules` StreamOut: replace
  `sess.print(Line{Kind: lc.classify(line), Text: line})` with
  `sess.emitStreamLine(lc, line)`.
- `runDocker` StreamOut: replace the inline `parseComposeProgress` block +
  `sess.print(...)` with `sess.emitStreamLine(lc, line)`.

### Counting (`internal/repl/loglevel.go`)

In `runStats.wrap`, after the compose branch, add a loose-severity branch
that counts **only** warnings:

```go
} else if ll, ok := parseLooseSeverity(line); ok {
    if ll.level == "WARNING" {
        s.warnings++
    }
}
```

(Loose `ERROR`/`CRITICAL` deliberately not counted — they must not fail
the run.)

### Tests

- `internal/repl/looselog_test.go` (new): table over `parseLooseSeverity`
  — `Warn:`/`Warning:`/`Error:`/`Err:`/`Critical:`/`Fatal:`/`Info:`/
  `Debug:` (incl. case-insensitive `warn:`/`ERROR:`), the exact
  `Warn: Can't find .pfb for face 'Courier'` example → `{WARNING, "Can't
  find .pfb for face 'Courier'"}`, and non-matches: a real Odoo line, a
  loguru line, a `ps` table row, prose without a leading severity token,
  and a blank line.
- Extend the `runStats.wrap` coverage (or add one) asserting a loose
  `Warn:` bumps `warnings` while a loose `Error:` leaves `errors` at 0.
- `dockerlog_test.go` stays green (compose unaffected).

### Docs

- `CHANGELOG.md` → `[Unreleased] / Added`: loose-severity stderr lines
  (e.g. wkhtmltopdf `Warn:`/`Error:`) now reformat into the Odoo log
  style; loose warnings count, loose errors don't fail the run.
- `context/progress-tracker.md` → mark Unit 36 done; record decisions
  (reformat to Odoo style; only WARNING counts).
- `context/specs/00-build-plan.md` → add the Unit 36 row.

## Dependencies

None new. Reuses `emitOdooLog`, the level-chip path, and the existing
`runStats`/`logColorer` plumbing.

## Verify when done

- [ ] `Warn: Can't find .pfb for face 'Courier'` renders as an Odoo-style
      line: timestamp + `WARN` chip + `report.wkhtmltopdf:` logger + the
      message, not raw default-fg text.
- [ ] A loose `Error: …` renders with the red `ERRO` chip but does **not**
      flip the run to ✗ or trigger auto-copy (errors stays 0).
- [ ] A loose `Warn: …` increments the warning count shown in the footer.
- [ ] A Python traceback whose tail is `SomeError: …` is **not** hijacked:
      while inside an err/warn chain, lines fall through to the inheriting
      classifier and stay grouped.
- [ ] Real Odoo and loguru log lines are unaffected (still rich-rendered
      by `formatOdooLine`); compose progress lines unaffected.
- [ ] Applies in both `update`/module output and `logs` output.
- [ ] `go build ./...`, `go vet ./...`, `go test ./internal/...` pass.
