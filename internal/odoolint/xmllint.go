package odoolint

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// xmllintBatch caps how many files go into one xmllint invocation, so a
// repo-wide run does not build an argv the OS refuses.
const xmllintBatch = 128

// FindXmllint resolves the xmllint binary, returning "" when it is
// absent. Callers treat absence as degraded coverage, never as failure:
// a missing validator must not fail a run, and it must not let a run
// claim a file is clean either.
func FindXmllint() string {
	path, err := exec.LookPath("xmllint")
	if err != nil {
		return ""
	}
	return path
}

// runXmllint validates files through libxml2 — the same library Odoo's
// lxml uses, so its verdict, wording and line number are the ones the
// server will print.
//
// schema files get both passes in a single invocation (--relaxng reports
// well-formedness errors too); syntaxOnly files get well-formedness
// alone.
func runXmllint(bin string, grammar []byte, schema, syntaxOnly []string) ([]Finding, error) {
	var out []Finding

	if len(syntaxOnly) > 0 {
		fs, err := xmllintRun(bin, nil, syntaxOnly)
		if err != nil {
			return nil, err
		}
		out = append(out, fs...)
	}

	if len(schema) > 0 && grammar != nil {
		path, cleanup, err := writeGrammar(grammar)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		fs, rerr := xmllintRun(bin, []string{"--relaxng", path}, schema)
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, fs...)
	}

	return out, nil
}

// writeGrammar materialises the embedded grammar on disk, since xmllint
// takes a path rather than bytes. The returned cleanup removes it.
func writeGrammar(grammar []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "echo-odoolint-")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, "import_xml.rng")
	if werr := os.WriteFile(path, grammar, 0o600); werr != nil {
		os.RemoveAll(dir)
		return "", func() {}, werr
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

// xmllintRun invokes xmllint over files in batches and parses its
// diagnostics.
//
// A non-zero exit only means "found something to report", which is the
// normal case here, so the exit status is deliberately ignored: the
// findings come from stderr.
func xmllintRun(bin string, extra, files []string) ([]Finding, error) {
	var out []Finding
	for start := 0; start < len(files); start += xmllintBatch {
		end := start + xmllintBatch
		if end > len(files) {
			end = len(files)
		}
		args := append([]string{"--noout"}, extra...)
		args = append(args, files[start:end]...)

		cmd := exec.Command(bin, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		cmd.Stdout = nil
		if err := cmd.Run(); err != nil {
			// An ExitError is xmllint reporting defects. Anything else
			// (binary vanished mid-run, permission denied) is a real
			// failure the caller must see rather than read as "clean".
			if _, isExit := err.(*exec.ExitError); !isExit {
				return nil, err
			}
		}
		out = append(out, parseXmllint(stderr.String())...)
	}
	return out, nil
}

// xmllintDiagRe matches the `<file>:<line>: <message>` head of an
// xmllint diagnostic. Continuation lines (the offending source line and
// the caret) do not match and are skipped.
var xmllintDiagRe = regexp.MustCompile(`^(.*?):(\d+):\s*(.*)$`)

// parseXmllint turns xmllint's stderr into findings.
func parseXmllint(stderr string) []Finding {
	var out []Finding
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		m := xmllintDiagRe.FindStringSubmatch(line)
		if m == nil {
			// "<file> fails to validate" / "<file> validates" summaries,
			// the echoed source line, and the caret marker.
			continue
		}
		lineNo, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		out = append(out, Finding{
			File:        m[1],
			Line:        lineNo,
			Rule:        ruleFor(m[3]),
			Message:     cleanXmllintMessage(m[3]),
			fromLibxml2: true,
		})
	}
	return out
}

// ruleFor classifies a diagnostic: a RelaxNG complaint is a schema
// finding, everything else is well-formedness.
func ruleFor(msg string) string {
	if strings.Contains(msg, "Relax-NG") {
		return RuleSchema
	}
	return RuleSyntax
}

// cleanXmllintMessage strips libxml2's category prefixes so the message
// reads like the rest of Echo's output, while keeping the wording the
// server logs.
func cleanXmllintMessage(msg string) string {
	for _, prefix := range []string{"parser error : ", "error : ", "validity error : "} {
		if i := strings.Index(msg, prefix); i >= 0 {
			msg = msg[i+len(prefix):]
			break
		}
	}
	// "element template: Relax-NG validity error : Element odoo has …"
	if i := strings.Index(msg, "Relax-NG validity error : "); i >= 0 {
		msg = msg[i+len("Relax-NG validity error : "):]
	}
	return strings.TrimSpace(msg)
}
