package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// ProjectAlias is one name → local-path entry, for display.
type ProjectAlias struct {
	Name string
	Path string
}

// ValidateAliasName rejects names that could be confused with a directory
// path or a flag, or that are otherwise unusable as a `-C` value.
func ValidateAliasName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("alias name is empty")
	case strings.ContainsAny(name, " \t/"):
		return fmt.Errorf("alias name %q may not contain spaces or slashes", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("alias name %q may not start with '-'", name)
	case name == "." || name == "..":
		return fmt.Errorf("alias name %q is reserved", name)
	}
	return nil
}

// isLocalDir reports whether path exists and is a directory on this machine.
func isLocalDir(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// SetProjectAlias inserts or replaces a name → absPath alias in the global
// config, preserving every other global field (mirrors SaveConnectTarget).
func SetProjectAlias(name, absPath string) error {
	if err := ValidateAliasName(name); err != nil {
		return err
	}
	cfg, err := LoadGlobal()
	if err != nil {
		return err
	}
	if cfg.ProjectAliases == nil {
		cfg.ProjectAliases = map[string]string{}
	}
	cfg.ProjectAliases[name] = absPath
	return SaveGlobal(cfg)
}

// RemoveProjectAlias deletes an alias, reporting whether it existed.
func RemoveProjectAlias(name string) (bool, error) {
	cfg, err := LoadGlobal()
	if err != nil {
		return false, err
	}
	if _, ok := cfg.ProjectAliases[name]; !ok {
		return false, nil
	}
	delete(cfg.ProjectAliases, name)
	return true, SaveGlobal(cfg)
}

// ResolveProjectAlias maps a `-C` value that isn't a directory to a local
// project path. It first checks project_aliases (source "alias"), then a
// connect target with the same name whose remote_path is a local directory
// (source "connect"). ok is false when nothing resolves locally.
func ResolveProjectAlias(name string) (path, source string, ok bool) {
	cfg, err := LoadGlobal()
	if err != nil {
		return "", "", false
	}
	if p, found := cfg.ProjectAliases[name]; found && p != "" {
		return p, "alias", true
	}
	for _, t := range cfg.ConnectTargets {
		if t.Name == name && isLocalDir(t.RemotePath) {
			return t.RemotePath, "connect", true
		}
	}
	return "", "", false
}

// MigrateConnectAliases backfills project aliases from connect targets
// whose remote_path resolves to a local directory and that aren't already
// aliased. Explicit and idempotent: re-running adds nothing new. Returns
// the names added and the names skipped (already-aliased or non-local).
func MigrateConnectAliases() (added, skipped []string, err error) {
	cfg, err := LoadGlobal()
	if err != nil {
		return nil, nil, err
	}
	if cfg.ProjectAliases == nil {
		cfg.ProjectAliases = map[string]string{}
	}
	for _, t := range cfg.ConnectTargets {
		if _, exists := cfg.ProjectAliases[t.Name]; exists {
			skipped = append(skipped, t.Name)
			continue
		}
		if !isLocalDir(t.RemotePath) {
			skipped = append(skipped, t.Name)
			continue
		}
		cfg.ProjectAliases[t.Name] = t.RemotePath
		added = append(added, t.Name)
	}
	sort.Strings(added)
	sort.Strings(skipped)
	if len(added) == 0 {
		return added, skipped, nil // nothing to persist
	}
	if err := SaveGlobal(cfg); err != nil {
		return nil, nil, err
	}
	return added, skipped, nil
}

// ProjectAliasList returns the aliases as a name-sorted slice for display.
func ProjectAliasList() ([]ProjectAlias, error) {
	cfg, err := LoadGlobal()
	if err != nil {
		return nil, err
	}
	out := make([]ProjectAlias, 0, len(cfg.ProjectAliases))
	for name, path := range cfg.ProjectAliases {
		out = append(out, ProjectAlias{Name: name, Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
