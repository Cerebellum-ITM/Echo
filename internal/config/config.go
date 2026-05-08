package config

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Theme         string
	Logo          string
	OdooVersion   string
	OdooContainer string
	DBContainer   string
	DBName        string
	Stage         string
	ProjectPath   string
	ProjectKey    string
}

type globalFile struct {
	Theme string `toml:"theme"`
	Logo  string `toml:"logo"`
}

type projectFile struct {
	OdooVersion   string `toml:"odoo_version"`
	OdooContainer string `toml:"odoo_container"`
	DBContainer   string `toml:"db_container"`
	DBName        string `toml:"db_name"`
	Stage         string `toml:"stage"`
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

	var p projectFile
	if data, err := os.ReadFile(filepath.Join(root, "projects", cfg.ProjectKey+".toml")); err == nil {
		_ = toml.Unmarshal(data, &p)
	}
	cfg.OdooVersion = p.OdooVersion
	cfg.OdooContainer = p.OdooContainer
	cfg.DBContainer = p.DBContainer
	cfg.DBName = p.DBName
	cfg.Stage = p.Stage

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

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(globalFile{Theme: cfg.Theme, Logo: cfg.Logo}); err != nil {
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
		OdooVersion:   cfg.OdooVersion,
		OdooContainer: cfg.OdooContainer,
		DBContainer:   cfg.DBContainer,
		DBName:        cfg.DBName,
		Stage:         cfg.Stage,
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
