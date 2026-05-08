package config

var Defaults = Config{
	Theme:         "charm",
	Logo:          "echo",
	OdooVersion:   "18",
	OdooContainer: "odoo",
	DBContainer:   "db",
	DBName:        "odoo",
	Stage:         "dev",
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
}
