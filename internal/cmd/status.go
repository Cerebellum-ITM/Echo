package cmd

import (
	"path/filepath"

	"github.com/pascualchavez/echo/internal/config"
)

// EchoVersion is the Echo CLI version string (semver + build metadata, e.g.
// "0.10.0+abc1234.dirty"). It is set once from main.go because the cmd layer
// can't import internal/repl (where FullVersion lives) without a cycle. Empty
// in tests / bare builds, which the status line renders as "unknown".
var EchoVersion string

// statusProjectName resolves the project name shown in the system-status
// line. Remote: the chosen target's alias (fromName) when present, else the
// basename of the remote path. Local: the per-project compose override, else
// the basename of the project path. Falls back to "-" when nothing resolves.
func statusProjectName(cfg *config.Config, remote bool, remotePath, fromName string) string {
	if remote {
		if fromName != "" {
			return fromName
		}
		if remotePath != "" {
			return filepath.Base(remotePath)
		}
		return "-"
	}
	if cfg != nil {
		if cfg.ComposeProject != "" {
			return cfg.ComposeProject
		}
		if cfg.ProjectPath != "" {
			return filepath.Base(cfg.ProjectPath)
		}
	}
	return "-"
}

// statusFields builds the cli/odoo/project/db pairs for the system-status
// line, applying the "unknown"/"-" fallbacks so a missing value is loud
// rather than silently absent. The db password never rides here — only the
// four identity fields below.
func statusFields(odooVer, project, db string) [][2]string {
	cli := EchoVersion
	if cli == "" {
		cli = "unknown"
	}
	if odooVer == "" {
		odooVer = "unknown"
	}
	if project == "" {
		project = "-"
	}
	if db == "" {
		db = "-"
	}
	return [][2]string{
		{"cli", cli},
		{"odoo", odooVer},
		{"project", project},
		{"db", db},
	}
}
