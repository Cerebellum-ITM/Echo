package config

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/log"
)

type Config struct {
	Theme         string
	Logo          string
	ComposeCmd    string
	OdooVersion   string
	OdooContainer string
	DBContainer   string
	DBName        string
	Stage         string
	AddonsPaths   []string
	ProjectPath   string
	ProjectKey    string

	// Compose project name override (per project). When empty, the REPL
	// derives the name from COMPOSE_PROJECT_NAME or the project dir basename.
	ComposeProject string

	// Prompt segmentation (global).
	PromptSegments []string
	PromptNameMax  int
	HealthTTL      time.Duration
}

type globalFile struct {
	Theme      string      `toml:"theme"`
	Logo       string      `toml:"logo"`
	ComposeCmd string      `toml:"compose_cmd"`
	Prompt     *promptFile `toml:"prompt"`
}

type promptFile struct {
	Segments  []string `toml:"segments"`
	NameMax   int      `toml:"name_max"`
	HealthTTL string   `toml:"health_ttl"`
}

type projectFile struct {
	OdooVersion    string   `toml:"odoo_version"`
	OdooContainer  string   `toml:"odoo_container"`
	DBContainer    string   `toml:"db_container"`
	DBName         string   `toml:"db_name"`
	Stage          string   `toml:"stage"`
	AddonsPaths    []string `toml:"addons_paths"`
	ComposeProject string   `toml:"compose_project"`
}

// Load reads global + per-project config for the given project path.
// Missing files are silently treated as empty; defaults are applied.
func Load(projectPath string) (*Config, error) {
	root, err := configRoot()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		ProjectPath: projectPath,
		ProjectKey:  projectKey(projectPath),
	}

	var g globalFile
	if data, err := os.ReadFile(filepath.Join(root, "global.toml")); err == nil {
		_ = toml.Unmarshal(data, &g)
	}
	cfg.Theme = g.Theme
	cfg.Logo = g.Logo
	cfg.ComposeCmd = g.ComposeCmd
	if g.Prompt != nil {
		cfg.PromptSegments = g.Prompt.Segments
		cfg.PromptNameMax = g.Prompt.NameMax
		if g.Prompt.HealthTTL != "" {
			if d, err := time.ParseDuration(g.Prompt.HealthTTL); err == nil {
				cfg.HealthTTL = d
			} else {
				log.Warn("invalid prompt.health_ttl in global.toml — using default 5s",
					"value", g.Prompt.HealthTTL, "err", err)
			}
		}
	}

	var p projectFile
	if data, err := os.ReadFile(filepath.Join(root, "projects", cfg.ProjectKey+".toml")); err == nil {
		_ = toml.Unmarshal(data, &p)
	}
	cfg.OdooVersion = p.OdooVersion
	cfg.OdooContainer = p.OdooContainer
	cfg.DBContainer = p.DBContainer
	cfg.DBName = p.DBName
	cfg.Stage = p.Stage
	cfg.AddonsPaths = p.AddonsPaths
	cfg.ComposeProject = p.ComposeProject

	applyDefaults(cfg)
	return cfg, nil
}

// SaveGlobal writes theme and logo to global.toml atomically.
func SaveGlobal(cfg *Config) error {
	root, err := configRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}

	g := globalFile{
		Theme:      cfg.Theme,
		Logo:       cfg.Logo,
		ComposeCmd: cfg.ComposeCmd,
	}
	if len(cfg.PromptSegments) > 0 || cfg.PromptNameMax > 0 || cfg.HealthTTL > 0 {
		g.Prompt = &promptFile{
			Segments: cfg.PromptSegments,
			NameMax:  cfg.PromptNameMax,
		}
		if cfg.HealthTTL > 0 {
			g.Prompt.HealthTTL = cfg.HealthTTL.String()
		}
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(g); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(root, "global.toml"), buf.Bytes())
}

// SaveProject writes per-project fields to projects/<key>.toml atomically.
func SaveProject(cfg *Config) error {
	root, err := configRoot()
	if err != nil {
		return err
	}
	projDir := filepath.Join(root, "projects")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		return err
	}

	p := projectFile{
		OdooVersion:    cfg.OdooVersion,
		OdooContainer:  cfg.OdooContainer,
		DBContainer:    cfg.DBContainer,
		DBName:         cfg.DBName,
		Stage:          cfg.Stage,
		AddonsPaths:    cfg.AddonsPaths,
		ComposeProject: cfg.ComposeProject,
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(p); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(projDir, cfg.ProjectKey+".toml"), buf.Bytes())
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
