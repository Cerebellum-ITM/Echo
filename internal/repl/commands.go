package repl

import "strings"

// Registry is the canonical, ordered list of top-level command names
// recognised by the REPL. The order matches the help output and
// determines the order of the match list rendered on a double-Tab.
var Registry = []string{
	"init", "reset", "alias",
	"install", "update", "uninstall", "test", "modules", "modinfo", "view",
	"i18n-export", "i18n-update", "i18n-pull",
	"db-backup", "db-restore", "db-drop", "db-neutralize", "db-list",
	"bash", "psql", "shell", "connect",
	"up", "down", "stop", "restart", "ps", "logs",
	"copy-last", "report",
	"clear", "help", "exit", "quit",
}

// commandFlags maps each command to the user-facing flags it accepts.
// Internal flags Echo builds itself (e.g. `-e`, `--no-http`, chrome
// flags) are intentionally excluded. Commands absent from the map have
// no known flags. Powers flag highlighting and Tab flag completion.
var commandFlags = map[string][]string{
	"alias":         {"--list", "--rm", "--migrate"},
	"install":       {"--with-demo", "--level"},
	"update":        {"--all", "--last", "--level", "--i18n"},
	"uninstall":     {"--level"},
	"test":          {"--update", "--tags"},
	"modules":       {"--config"},
	"modinfo":       {"--copy", "--last"},
	"view":          {"--copy", "--last"},
	"i18n-export":   {"--out"},
	"i18n-update":   {"--force"},
	"i18n-pull":     {"--from", "--all", "--installed"},
	"db-backup":     {"--with-filestore"},
	"db-restore":    {"--as", "--force", "--neutralize"},
	"db-drop":       {"--force"},
	"db-neutralize": {"--force"},
	"down":          {"--force"},
	"logs":          {"-t", "--no-follow", "-c", "--copy", "--all"},
	"connect":       {"--all", "--force", "--fresh", "--new-window"},
	"copy-last":     {"--errors"},
	"report":        {"--step", "--level", "--min-level", "--copy"},
}

func init() {
	seen := map[string]bool{}
	for _, name := range Registry {
		if seen[name] {
			panic("repl: duplicate command in Registry: " + name)
		}
		seen[name] = true
	}
	for cmd := range commandFlags {
		if !seen[cmd] {
			panic("repl: commandFlags references unknown command: " + cmd)
		}
	}
}

// matchPrefix returns the entries in Registry that start with prefix,
// preserving Registry order. An empty prefix returns nil (Tab on an
// empty buffer is a no-op).
func matchPrefix(prefix string) []string {
	if prefix == "" {
		return nil
	}
	var out []string
	for _, name := range Registry {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out
}

// longestCommonPrefix returns the longest string that is a prefix of
// every entry in matches. Returns "" for an empty slice.
func longestCommonPrefix(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	prefix := matches[0]
	for _, s := range matches[1:] {
		n := 0
		for n < len(prefix) && n < len(s) && prefix[n] == s[n] {
			n++
		}
		prefix = prefix[:n]
		if prefix == "" {
			return ""
		}
	}
	return prefix
}
