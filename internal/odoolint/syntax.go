package odoolint

import (
	"bytes"
	"encoding/xml"
	"html"
	"regexp"
	"strings"
)

// syntaxFindings runs the always-available well-formedness pass over one
// file: encoding/xml on the document itself, then on each sub-document it
// contains.
//
// encoding/xml is the floor rather than the authority. It catches every
// failure that has actually cost a deploy here — `--` in a comment,
// mismatched tags, a stray `<` from inline JS — and it is compiled in, so
// no missing binary can silently disable it. It is genuinely weaker than
// libxml2 (it accepts duplicate attributes, multiple roots, and a
// misplaced XML declaration, all fatal on the server), which is why the
// xmllint pass exists on top of it rather than beside it.
func syntaxFindings(file string, data []byte) []Finding {
	if f, ok := parseWellFormed(data); !ok {
		return []Finding{{
			File:    file,
			Line:    f.line,
			Rule:    RuleSyntax,
			Message: f.msg,
		}}
	}

	// The outer document parsed, so any remaining defect is hiding in a
	// sub-document the outer parse treated as opaque text.
	var out []Finding
	for _, sub := range subDocuments(data) {
		f, ok := parseWellFormed([]byte(sub.content))
		if ok {
			continue
		}
		out = append(out, Finding{
			File: file,
			// The parse failed at line f.line of the fragment; the user
			// edits the file, so report where the fragment starts plus
			// that offset.
			Line:    sub.startLine + f.line - 1,
			Rule:    RuleEmbedded,
			Message: sub.label() + f.msg,
		})
	}
	return out
}

// parseFailure is a well-formedness error located in whatever byte slice
// was parsed.
type parseFailure struct {
	line int
	msg  string
}

// parseWellFormed token-scans data to completion. It reports ok when the
// input is well formed.
//
// Note the token loop tolerates several roots and bare text, which is
// exactly what a decoded fragment looks like — so a fragment is only
// reported for a genuine syntax defect, not for not being a document.
func parseWellFormed(data []byte) (parseFailure, bool) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	for {
		_, err := d.Token()
		if err == nil {
			continue
		}
		if err.Error() == "EOF" {
			return parseFailure{}, true
		}
		if se, isSyntax := err.(*xml.SyntaxError); isSyntax {
			return parseFailure{line: se.Line, msg: se.Msg}, false
		}
		// A non-syntax decode error (an unsupported charset, say) is not
		// a markup defect we can pin to a line.
		return parseFailure{line: 1, msg: err.Error()}, false
	}
}

// subDoc is XML nested inside XML: the outer parse sees opaque text, the
// server later parses it for real.
type subDoc struct {
	content   string
	startLine int
	// field is the name attribute of the enclosing <field>.
	field string
}

// label prefixes a finding message with the sub-document's origin, so
// "invalid sequence" at file line 40 is not mistaken for a defect in the
// surrounding markup.
func (s subDoc) label() string {
	return "in field " + s.field + ": "
}

// isXMLField reports whether Odoo parses the field's content with the
// XML parser, which is the only case where checking it as XML is valid.
//
// arch and arch_db hold view definitions: the view loader parses them as
// XML at install/update time, so a defect there aborts the load exactly
// like one in the surrounding file.
//
// Everything else is deliberately excluded, and body_html is why. It
// holds HTML, which Odoo parses with lxml.html — an HTML parser accepts
// unclosed <p> and <br>, bare `%` template directives, and entities an
// XML parser rejects outright. Checking those as XML produced five false
// positives on the first real repo this ran against, against two genuine
// findings. A linter that cries wolf on every mail template is one people
// switch off.
func isXMLField(name string) bool {
	return name == "arch" || name == "arch_db"
}

var (
	// cdataOpen/cdataClose bound a CDATA section. The outer parse hands
	// the body back as character data without looking inside, so a
	// broken comment in there is invisible until the server parses it.
	cdataOpen  = []byte("<![CDATA[")
	cdataClose = []byte("]]>")

	// fieldOpenRe matches a <field …> start tag, capturing the whole tag
	// so the name attribute can be pulled out of it.
	fieldOpenRe = regexp.MustCompile(`(?is)<field\b[^>]*>`)

	// nameAttrRe pulls name="…" out of a start tag.
	nameAttrRe = regexp.MustCompile(`(?is)\bname\s*=\s*["']([^"']*)["']`)
)

// subDocuments finds the XML nested inside data, in the two shapes the
// outer parse cannot see through.
//
// There are three shapes of nested arch in Odoo data files, but only two
// need work here: inline markup (<field name="arch"><form>…) is already
// parsed as part of the outer document, so a defect in it is caught by
// the outer parse and would only be double-reported here.
func subDocuments(data []byte) []subDoc {
	var out []subDoc
	out = append(out, cdataSections(data)...)
	out = append(out, escapedFieldBodies(data)...)
	return out
}

// cdataSections returns every CDATA body, tagged with the field that
// encloses it when there is one.
func cdataSections(data []byte) []subDoc {
	var out []subDoc
	from := 0
	for {
		rel := bytes.Index(data[from:], cdataOpen)
		if rel < 0 {
			return out
		}
		start := from + rel + len(cdataOpen)
		relEnd := bytes.Index(data[start:], cdataClose)
		if relEnd < 0 {
			return out // unterminated CDATA: the outer parse already failed
		}
		end := start + relEnd
		body := string(data[start:end])
		field := enclosingField(data, from+rel)
		if isXMLField(field) && looksLikeMarkup(body) {
			out = append(out, subDoc{
				content:   body,
				startLine: lineAt(data, start),
				field:     field,
			})
		}
		from = end + len(cdataClose)
	}
}

// escapedFieldBodies returns the bodies of <field> elements whose content
// is entity-escaped XML (`&lt;form&gt;…`) rather than inline markup —
// the third arch shape. The body is unescaped before it is parsed,
// because that is what the server does with it.
func escapedFieldBodies(data []byte) []subDoc {
	var out []subDoc
	for _, loc := range fieldOpenRe.FindAllIndex(data, -1) {
		bodyStart := loc[1]
		rel := bytes.Index(data[bodyStart:], []byte("</field>"))
		if rel < 0 {
			continue
		}
		raw := string(data[bodyStart : bodyStart+rel])
		// Escaped-XML bodies carry &lt; and no real tags; a body with
		// real tags is inline markup the outer parse already checked.
		if !strings.Contains(raw, "&lt;") || strings.Contains(raw, "<") {
			continue
		}
		field := attrName(string(data[loc[0]:loc[1]]))
		if !isXMLField(field) {
			continue
		}
		body := html.UnescapeString(raw)
		if !looksLikeMarkup(body) {
			continue
		}
		out = append(out, subDoc{
			content:   body,
			startLine: lineAt(data, bodyStart),
			field:     field,
		})
	}
	return out
}

// looksLikeMarkup reports whether s is worth parsing as XML at all. A
// CDATA block holding a SQL snippet or a plain sentence is not a
// sub-document and must not be reported as broken XML.
func looksLikeMarkup(s string) bool {
	i := strings.IndexByte(s, '<')
	if i < 0 {
		return false
	}
	rest := strings.TrimLeft(s[i+1:], " \t\r\n")
	if rest == "" {
		return false
	}
	c := rest[0]
	return c == '!' || c == '?' || c == '/' || c == '_' || c == ':' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// enclosingField returns the name of the <field> that opens closest
// before offset, or "" when the offset is not inside one. It is a
// backwards scan rather than a parse because the caller already has the
// raw bytes and only needs the nearest tag.
func enclosingField(data []byte, offset int) string {
	head := data[:offset]
	open := bytes.LastIndex(bytes.ToLower(head), []byte("<field"))
	if open < 0 {
		return ""
	}
	// A </field> after that start tag means the offset is outside it.
	if closed := bytes.LastIndex(bytes.ToLower(head), []byte("</field>")); closed > open {
		return ""
	}
	tagEnd := bytes.IndexByte(data[open:], '>')
	if tagEnd < 0 {
		return ""
	}
	return attrName(string(data[open : open+tagEnd+1]))
}

// attrName extracts the name attribute from a start tag.
func attrName(tag string) string {
	m := nameAttrRe.FindStringSubmatch(tag)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// lineAt returns the 1-based line number of byte offset in data.
func lineAt(data []byte, offset int) int {
	if offset > len(data) {
		offset = len(data)
	}
	return 1 + bytes.Count(data[:offset], []byte("\n"))
}

// rootElement returns the name of the document's first element, or "".
// Used to decide whether the data-file grammar applies.
func rootElement(data []byte) string {
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}
