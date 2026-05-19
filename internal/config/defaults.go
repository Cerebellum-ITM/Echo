package config

import "time"

var Defaults = Config{
	Theme:          "charm",
	Logo:           "echo",
	OdooVersion:    "18",
	OdooContainer:  "odoo",
	DBContainer:    "db",
	DBName:         "odoo",
	Stage:          "dev",
	PromptSegments: []string{"name", "version_db", "stage", "health"},
	PromptNameMax:  18,
	HealthTTL:      5 * time.Second,
}

func applyDefaults(cfg *Config) {
	if cfg.Theme == "" {
		cfg.Theme = Defaults.Theme
	}
	if cfg.Logo == "" {
		cfg.Logo = Defaults.Logo
	}
	if cfg.OdooVersion == "" {
		cfg.OdooVersion = Defaults.OdooVersion
	}
	if cfg.OdooContainer == "" {
		cfg.OdooContainer = Defaults.OdooContainer
	}
	if cfg.DBContainer == "" {
		cfg.DBContainer = Defaults.DBContainer
	}
	if cfg.DBName == "" {
		cfg.DBName = Defaults.DBName
	}
	if cfg.Stage == "" {
		cfg.Stage = Defaults.Stage
	}
	if len(cfg.PromptSegments) == 0 {
		cfg.PromptSegments = append([]string(nil), Defaults.PromptSegments...)
	}
	if cfg.PromptNameMax <= 0 {
		cfg.PromptNameMax = Defaults.PromptNameMax
	}
	if cfg.HealthTTL <= 0 {
		cfg.HealthTTL = Defaults.HealthTTL
	}
}
