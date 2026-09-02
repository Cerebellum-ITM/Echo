package repl

import "strings"

// Registry is the canonical, ordered list of top-level command names
// recognised by the REPL. The order matches the help output and
// determines the order of the match list rendered on a double-Tab.
var Registry = []string{
	"init", "reset", "alias", "link",
	"install", "update", "uninstall", "test", "modules", "modinfo", "modstate", "view", "compare", "lint",
	"i18n-export", "i18n-update", "i18n-pull",
	"db-admin", "db-backup", "db-restore", "db-pull", "db-drop", "db-neutralize", "db-list", "db-use",
	"bash", "psql", "shell", "shell-run", "connect",
	"up", "down", "stop", "restart", "ps", "logs", "push", "deploy", "watch", "checkpoint", "actions", "promote",
	"copy-last", "report", "logview", "sequence",
	"clear", "help", "exit", "quit",
}

// commandFlags maps each command to the user-facing flags it accepts.
// Internal flags Echo builds itself (e.g. `-e`, `--no-http`, chrome
// flags) are intentionally excluded. Commands absent from the map have
// no known flags. Powers flag highlighting and Tab flag completion.
var commandFlags = map[string][]string{
	"alias":         {"--list", "--rm", "--migrate"},
	"link":          {"--show", "--rm", "--next", "--list", "--json"},
	"install":       {"--with-demo", "--level"},
	"update":        {"--all", "--last", "--level", "--i18n", "--installed", "--from", "--remote", "-E", "--env"},
	"uninstall":     {"--level"},
	"test":          {"--update", "--tags", "--from", "--remote", "-E", "--env"},
	"modules":       {"--config", "--addons-path"},
	"modinfo":       {"--copy", "--last"},
	"modstate":      {"--all", "--json"},
	"lint":          {"--json"},
	"view":          {"--copy", "--last", "--from", "--remote", "-E", "--env"},
	"compare":       {"--all", "--copy", "--from", "--remote", "-E", "--env"},
	"i18n-export":   {"--out"},
	"i18n-update":   {"--force"},
	"i18n-pull":     {"--from", "--lang", "--all", "--installed", "--to-worktree"},
	"db-admin":      {"--force", "--password", "--insecure", "--from", "--remote", "-E", "--env"},
	"db-backup":     {"--with-filestore"},
	"db-restore":    {"--as", "--force", "--neutralize"},
	"db-pull":       {"--from", "--remote", "--as", "--neutralize", "--no-neutralize", "--filestore", "--force", "--restore", "-E", "--env"},
	"db-drop":       {"--force"},
	"db-neutralize": {"--force"},
	"up":            {"--from", "--remote", "-E", "--env"},
	"down":          {"--force", "-E", "--env"},
	"stop":          {"--from", "--remote", "--force", "-E", "--env"},
	"restart":       {"--from", "--remote", "--force", "-E", "--env"},
	"logs":          {"-t", "--no-follow", "-c", "--copy", "--all", "--from", "--remote", "-E", "--env"},
	"shell":         {"--from", "--remote", "--force", "-E", "--env"},
	"shell-run":     {"--no-copy", "--force", "--from", "--remote", "-E", "--env"},
	"connect":       {"--all", "--force", "--fresh", "--new-window"},
	"push":          {"--from", "--remote", "--dirty", "--dry-run", "--delete", "--force", "--dest", "--pick-dest", "--set-dest", "--mkdir", "--clean", "--all", "-E", "--env"},
	"deploy":        {"--from", "--limit", "--dry-run", "--force", "--i18n", "--no-i18n", "--commits", "--modules", "--auto", "--push", "--no-push", "--set-push", "--test", "--no-test", "--test-toggle", "--test-modules", "--test-add", "--test-rm", "--test-clear", "--json", "--checkpoint", "--no-checkpoint", "--set-checkpoint", "--set-checkpoint-method", "--set-checkpoint-keep", "--rollback", "--consume-checkpoint", "--rollback-on-fail", "--no-rollback-on-fail", "--no-actions", "--no-git", "--no-lint", "--restore-code", "--set-code", "--keep-overlay", "--with-local", "--fetch", "--no-fetch", "--set-git-branch", "--rename"},
	"watch":         {"--from", "--remote", "--interval", "--force", "--no-logs", "--no-checkpoint", "--no-actions"},
	"promote":       {"--dirty", "--commits", "--to", "--set-branch", "--show-branch", "--create-dest", "--dry-run", "--force", "--reset", "--discard", "--set-base", "--no-fetch"},
	"checkpoint":    {"--from", "--remote", "--method", "--all", "--force", "--json", "-E", "--env"},
	"actions":       {"--from", "--remote", "--json", "--force", "-E", "--env"},
	"copy-last":     {"--errors"},
	"report":        {"--step", "--level", "--min-level", "--copy"},
	"logview":       {"--list", "--last", "--clear", "--force", "--from", "--remote", "--json"},
	"sequence":      {"--remote", "--from", "--last", "--continue-on-error", "-E", "--env"},
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
