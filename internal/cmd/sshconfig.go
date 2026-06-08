package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sshConfigHosts returns the concrete `Host` aliases declared in the
// user's ~/.ssh/config, skipping wildcard/negated patterns (`*`, `?`,
// `!`) which are match rules, not real destinations. Echo never edits
// this file — it only references the aliases so `ssh <alias>` resolves
// hostname/user/key/port through the user's own config.
func sshConfigHosts() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := map[string]bool{}
	var hosts []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if !strings.EqualFold(key, "host") {
			continue
		}
		for _, alias := range strings.Fields(rest) {
			if strings.ContainsAny(alias, "*?!") {
				continue
			}
			if !seen[alias] {
				seen[alias] = true
				hosts = append(hosts, alias)
			}
		}
	}
	sort.Strings(hosts)
	return hosts
}
