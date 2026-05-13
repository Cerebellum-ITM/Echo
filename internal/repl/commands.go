package repl

import "strings"

// Registry is the canonical, ordered list of top-level command names
// recognised by the REPL. The order matches the help output and
// determines the order of the match list rendered on a double-Tab.
var Registry = []string{
	"init", "reset",
	"install", "update", "uninstall", "modules",
	"i18n-export", "i18n-update",
	"db-backup", "db-restore", "db-drop", "db-list",
	"bash", "psql", "shell",
	"up", "down", "restart", "ps", "logs",
	"clear", "help", "exit", "quit",
}

func init() {
	seen := map[string]bool{}
	for _, name := range Registry {
		if seen[name] {
			panic("repl: duplicate command in Registry: " + name)
		}
		seen[name] = true
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
