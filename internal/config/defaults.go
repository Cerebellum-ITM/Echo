package config

import "time"

var Defaults = Config{
	Theme:          "charm",
	Logo:           "echo",
	Banner:         "auto",
	OdooVersion:    "18",
	OdooContainer:  "odoo",
	DBContainer:    "db",
	DBName:         "odoo",
	Stage:          "dev",
	FilestorePath:  "/var/lib/odoo/filestore",
	ConfPath:       "/etc/odoo/odoo.conf",
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
	if cfg.Banner == "" {
		cfg.Banner = Defaults.Banner
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
	if cfg.FilestorePath == "" {
		cfg.FilestorePath = Defaults.FilestorePath
	}
	if cfg.ConfPath == "" {
		cfg.ConfPath = Defaults.ConfPath
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
