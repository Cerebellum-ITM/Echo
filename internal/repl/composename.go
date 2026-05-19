package repl

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/pascualchavez/echo/internal/config"
)

// resolveComposeProject derives the docker-compose project name used in
// the prompt. Priority:
//  1. COMPOSE_PROJECT_NAME env var
//  2. cfg.ComposeProject (per-project TOML override)
//  3. filepath.Base(cfg.ProjectPath), normalized like docker-compose does
//
// When the result of normalization is empty, the first 8 chars of the
// project SHA-256 key are used as a last-resort identifier.
func resolveComposeProject(cfg *config.Config) string {
	if v := strings.TrimSpace(os.Getenv("COMPOSE_PROJECT_NAME")); v != "" {
		return v
	}
	if v := strings.TrimSpace(cfg.ComposeProject); v != "" {
		return v
	}
	if cfg.ProjectPath != "" {
		if n := normalizeProjectName(filepath.Base(cfg.ProjectPath)); n != "" {
			return n
		}
	}
	if len(cfg.ProjectKey) >= 8 {
		return cfg.ProjectKey[:8]
	}
	return cfg.ProjectKey
}

// normalizeProjectName mirrors the lowercase / strip / collapse rules
// docker-compose applies to the project directory name. Returns an
// empty string if no allowed runes remain.
func normalizeProjectName(raw string) string {
	raw = strings.ToLower(raw)
	var b strings.Builder
	b.Grow(len(raw))
	lastSep := false
	for _, r := range raw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastSep = false
		case r == '-' || r == '_':
			if !lastSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			lastSep = true
		default:
			// drop
		}
	}
	out := b.String()
	out = strings.Trim(out, "_-")
	return out
}

// truncateName cuts name to at most max runes, replacing the trailing
// rune with an ellipsis when truncation actually happens. max values
// below 4 are silently clamped to 4 so the ellipsis stays useful.
func truncateName(name string, max int) string {
	if max < 4 {
		max = 4
	}
	if utf8.RuneCountInString(name) <= max {
		return name
	}
	runes := []rune(name)
	return string(runes[:max-1]) + "…"
}
