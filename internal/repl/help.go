package repl

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

type helpEntry struct{ cmd, desc string }
type helpSection struct {
	title string
	items []helpEntry
}

// helpPassthrough commands forward their arguments to a wrapped tool, so a
// `--help` past the first argument belongs to that tool, not to Echo.
var helpPassthrough = map[string]bool{
	"bash": true, "psql": true, "shell": true, "shell-run": true, "connect": true, "logs": true,
}

// HelpRequest reports whether a command line (name plus its arguments) asks
// for that command's help page: `--help` anywhere before a literal `--`, or
// `-h` as the sole argument. main.go uses it to answer before any project
// detection.
func HelpRequest(args []string) (string, bool) {
	if len(args) < 2 || args[0] == "help" {
		return "", false
	}
	name, rest := args[0], args[1:]
	if _, ok := docFor(name); !ok {
		return "", false
	}
	if len(rest) == 1 && rest[0] == "-h" {
		return name, true
	}
	if helpPassthrough[name] {
		if rest[0] == "--help" {
			return name, true
		}
		return "", false
	}
	for _, a := range rest {
		if a == "--" {
			break
		}
		if a == "--help" {
			return name, true
		}
	}
	return "", false
}

// runHelp routes `help [<command>|<topic>|<text>] [--json]`. Bare help opens
// the pager where it can (REPL, or a one-shot on a real terminal) and prints
// the index otherwise; command and topic pages are always flat.
func (sess *session) runHelp(args []string) {
	jsonOut := false
	var positional []string
	for _, a := range args {
		switch {
		case a == "--json":
			jsonOut = true
		case strings.HasPrefix(a, "-"):
			sess.print(Line{Kind: "warn", Text: "help: unknown flag " + a})
			sess.exitCode = exitUsage
			return
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 1 {
		sess.print(Line{Kind: "warn", Text: "help takes one command, topic or search text — try help " + positional[0]})
		sess.exitCode = exitUsage
		return
	}

	if len(positional) == 0 {
		switch {
		case jsonOut:
			sess.writeHelpJSON(helpIndexJSON())
		case sess.helpPagerEnabled():
			err := sess.runHelpPager()
			if err != nil && !sess.handleQuit(err) {
				sess.printHelpIndex()
			}
		default:
			sess.printHelpIndex()
		}
		return
	}

	query := positional[0]
	if d, ok := docFor(query); ok {
		if jsonOut {
			sess.writeHelpJSON(d.json())
		} else {
			sess.printCommandPage(d)
		}
		return
	}
	if t, ok := topicFor(query); ok {
		if jsonOut {
			sess.writeHelpJSON(t.json())
		} else {
			sess.printTopicPage(t)
		}
		return
	}

	matches := searchHelp(query)
	if jsonOut {
		sess.writeHelpJSON(helpSearchJSON{Query: query, Matches: matches})
	} else if len(matches) > 0 {
		sess.printHelpSearch(matches)
	} else {
		sess.print(Line{Kind: "warn", Text: fmt.Sprintf("no help matches %q; run help for the index", query)})
	}
	if len(matches) == 0 {
		sess.exitCode = exitUsage
	}
}

// helpPagerEnabled reports whether `help` should open the interactive pager.
// It runs in the live REPL, and also for a one-shot `echo help` when both
// stdin and stdout are real terminals — so running it straight from the
// shell gets the same paginated viewer. Recipe steps and TTY-less runs
// (pipes, redirects, CI) stay on the flat index.
func (sess *session) helpPagerEnabled() bool {
	if sess.recipe {
		return false
	}
	if sess.interactive {
		return true
	}
	return stdinIsTTY() && stdoutIsTTY()
}

func (sess *session) printHelpIndex() {
	for i, sec := range helpSections() {
		if i > 0 {
			sess.print(Line{Kind: "out", Text: ""})
		}
		sess.print(Line{Kind: "accent", Text: sec.title})
		var rows []helpEntry
		for _, d := range commandDocs {
			if d.section == sec.title {
				rows = append(rows, helpEntry{d.name, d.summary})
			}
		}
		sess.printHelpRows(rows)
	}
	sess.print(Line{Kind: "out", Text: ""})
	sess.print(Line{Kind: "accent", Text: "Topics"})
	rows := make([]helpEntry, len(helpTopics))
	for i, t := range helpTopics {
		rows[i] = helpEntry{t.name, t.title}
	}
	sess.printHelpRows(rows)
	sess.print(Line{Kind: "out", Text: ""})
	sess.print(Line{Kind: "faint", Text: "help <command> for flags and examples · help agents for headless use · help <text> to search"})
}

func (sess *session) printCommandPage(d commandDoc) {
	sess.print(Line{Kind: "accent", Text: d.name + " — " + d.summary})
	sess.print(Line{Kind: "out", Text: ""})
	sess.print(Line{Kind: "dim", Text: "Usage"})
	sess.print(Line{Kind: "out", Text: "  echo_cli " + d.usage})

	block := func(title string, items []helpItem) {
		if len(items) == 0 {
			return
		}
		sess.print(Line{Kind: "out", Text: ""})
		sess.print(Line{Kind: "dim", Text: title})
		rows := make([]helpEntry, len(items))
		for i, it := range items {
			rows[i] = helpEntry{it.label(), it.desc}
		}
		sess.printHelpRows(rows)
	}
	block("Subcommands", d.subs)
	block("Flags", d.flags)

	if len(d.notes) > 0 {
		sess.print(Line{Kind: "out", Text: ""})
		sess.print(Line{Kind: "dim", Text: "Notes"})
		for _, n := range d.notes {
			sess.print(Line{Kind: "out", Text: "  - " + n})
		}
	}
	if len(d.examples) > 0 {
		sess.print(Line{Kind: "out", Text: ""})
		sess.print(Line{Kind: "dim", Text: "Examples"})
		for _, ex := range d.examples {
			sess.print(Line{Kind: "info", Text: "  echo_cli " + ex.cmd})
			sess.print(Line{Kind: "faint", Text: "      " + ex.desc})
		}
	}
	if d.isRemote() || d.hasFlag("--json") {
		sess.print(Line{Kind: "out", Text: ""})
		sess.print(Line{Kind: "faint", Text: "  see also: help agents"})
	}
}

func (sess *session) printTopicPage(t helpTopic) {
	sess.print(Line{Kind: "accent", Text: t.title})
	sess.printHelpRows(topicEntries(t))
}

func (sess *session) printHelpSearch(matches []helpMatchJSON) {
	rows := make([]helpEntry, len(matches))
	for i, m := range matches {
		rows[i] = helpEntry{strings.TrimSpace(m.Command + " " + m.Item), m.Desc}
	}
	sess.printHelpRows(rows)
}

func (sess *session) printHelpRows(rows []helpEntry) {
	width := helpLabelWidth(rows)
	for _, r := range rows {
		rendered, plain := renderHelpEntry(sess.styles, r, width)
		sess.printStyled(rendered, plain, "out")
	}
}

// maxHelpLabelWidth caps the label column; a longer label pushes its own
// description right instead of widening every row.
const maxHelpLabelWidth = 30

func helpLabelWidth(rows []helpEntry) int {
	w := 0
	for _, r := range rows {
		if n := lipgloss.Width(r.cmd); n > w && n <= maxHelpLabelWidth {
			w = n
		}
	}
	return w + 2
}

// renderHelpEntry returns one two-column row styled (label in Info,
// description in Out) and plain, padded to width.
func renderHelpEntry(s theme.Styles, r helpEntry, width int) (rendered, plain string) {
	pad := width - lipgloss.Width(r.cmd)
	if pad < 2 {
		pad = 2
	}
	gap := strings.Repeat(" ", pad)
	return "  " + s.Info.Render(r.cmd) + gap + s.Out.Render(r.desc),
		"  " + r.cmd + gap + r.desc
}

func searchHelp(query string) []helpMatchJSON {
	q := strings.ToLower(query)
	has := func(texts ...string) bool {
		for _, t := range texts {
			if strings.Contains(strings.ToLower(t), q) {
				return true
			}
		}
		return false
	}
	matches := []helpMatchJSON{}
	for _, d := range commandDocs {
		if has(d.name, d.summary) {
			matches = append(matches, helpMatchJSON{Command: d.name, Desc: d.summary})
		}
	}
	for _, d := range commandDocs {
		for _, it := range append(append([]helpItem{}, d.subs...), d.flags...) {
			if has(it.name, it.desc) {
				matches = append(matches, helpMatchJSON{Command: d.name, Item: it.name, Desc: it.desc})
			}
		}
	}
	for _, t := range helpTopics {
		hit := has(t.name, t.title)
		for _, r := range t.rows {
			hit = hit || has(r.name, r.desc)
		}
		if hit {
			matches = append(matches, helpMatchJSON{Command: "help", Item: t.name, Desc: t.title})
		}
	}
	return matches
}

type helpItemJSON struct {
	Name string `json:"name"`
	Arg  string `json:"arg"`
	Desc string `json:"desc"`
}

type helpExampleJSON struct {
	Cmd  string `json:"cmd"`
	Desc string `json:"desc"`
}

type commandDocJSON struct {
	Name        string            `json:"name"`
	Section     string            `json:"section"`
	Usage       string            `json:"usage"`
	Summary     string            `json:"summary"`
	Subcommands []helpItemJSON    `json:"subcommands"`
	Flags       []helpItemJSON    `json:"flags"`
	Notes       []string          `json:"notes"`
	Examples    []helpExampleJSON `json:"examples"`
	Remote      bool              `json:"remote"`
	JSONOutput  bool              `json:"json_output"`
}

type helpSectionJSON struct {
	Title    string   `json:"title"`
	Commands []string `json:"commands"`
}

type helpIndexJSONDoc struct {
	Version  string            `json:"version"`
	Sections []helpSectionJSON `json:"sections"`
	Commands []commandDocJSON  `json:"commands"`
	Topics   []string          `json:"topics"`
}

type helpTopicJSON struct {
	Topic string         `json:"topic"`
	Title string         `json:"title"`
	Rows  []helpItemJSON `json:"rows"`
}

type helpMatchJSON struct {
	Command string `json:"command"`
	Item    string `json:"item"`
	Desc    string `json:"desc"`
}

type helpSearchJSON struct {
	Query   string          `json:"query"`
	Matches []helpMatchJSON `json:"matches"`
}

func itemsJSON(items []helpItem) []helpItemJSON {
	out := make([]helpItemJSON, len(items))
	for i, it := range items {
		out[i] = helpItemJSON{it.name, it.arg, it.desc}
	}
	return out
}

func (d commandDoc) json() commandDocJSON {
	examples := make([]helpExampleJSON, len(d.examples))
	for i, ex := range d.examples {
		examples[i] = helpExampleJSON{ex.cmd, ex.desc}
	}
	notes := append([]string{}, d.notes...)
	return commandDocJSON{
		Name:        d.name,
		Section:     d.section,
		Usage:       d.usage,
		Summary:     d.summary,
		Subcommands: itemsJSON(d.subs),
		Flags:       itemsJSON(d.flags),
		Notes:       notes,
		Examples:    examples,
		Remote:      d.isRemote(),
		JSONOutput:  d.hasFlag("--json"),
	}
}

func (t helpTopic) json() helpTopicJSON {
	return helpTopicJSON{Topic: t.name, Title: t.title, Rows: itemsJSON(t.rows)}
}

func helpIndexJSON() helpIndexJSONDoc {
	doc := helpIndexJSONDoc{Version: FullVersion()}
	for _, d := range commandDocs {
		if n := len(doc.Sections); n == 0 || doc.Sections[n-1].Title != d.section {
			doc.Sections = append(doc.Sections, helpSectionJSON{Title: d.section, Commands: []string{}})
		}
		sec := &doc.Sections[len(doc.Sections)-1]
		sec.Commands = append(sec.Commands, d.name)
		doc.Commands = append(doc.Commands, d.json())
	}
	for _, t := range helpTopics {
		doc.Topics = append(doc.Topics, t.name)
	}
	return doc
}

func (sess *session) writeHelpJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		emitOdooLogTo(os.Stderr, "ERROR", "echo.help", "encode failed",
			[]logField{{"err", err.Error()}}, sess.styles, sess.palette, sess.cfg.DBName)
		sess.exitCode = exitError
		return
	}
	os.Stdout.Write(b)
	os.Stdout.Write([]byte("\n"))
}

// helpCommandNames extracts the flat set of top-level command names
// referenced in the help table. Used by the registry consistency test.
// Skips flag rows (leading whitespace or starting with "-") and
// keyboard tokens like "Ctrl+D".
func helpCommandNames() []string {
	var out []string
	for _, sec := range helpSections() {
		for _, it := range sec.items {
			if strings.HasPrefix(it.cmd, " ") || strings.HasPrefix(it.cmd, "-") {
				continue
			}
			for _, part := range strings.Split(it.cmd, ",") {
				fields := strings.Fields(part)
				if len(fields) == 0 {
					continue
				}
				name := fields[0]
				if strings.ContainsAny(name, "+<[") {
					continue
				}
				out = append(out, name)
			}
		}
	}
	return out
}
