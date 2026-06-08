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

	// Connect (`connect` command, per project). When ConnectSSHHost is
	// empty the session is minted locally; otherwise minting runs over
	// SSH against the remote host and the container/db mapping is read
	// from the server's own Echo profile at ConnectRemotePath — nothing
	// is re-declared locally. ConnectChromePath overrides Chrome
	// auto-detection for the local cookie injection.
	ConnectSSHHost    string
	ConnectRemotePath string
	ConnectChromePath string

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
	OdooVersion    string       `toml:"odoo_version"`
	OdooContainer  string       `toml:"odoo_container"`
	DBContainer    string       `toml:"db_container"`
	DBName         string       `toml:"db_name"`
	Stage          string       `toml:"stage"`
	AddonsPaths    []string     `toml:"addons_paths"`
	ComposeProject string       `toml:"compose_project"`
	Connect        *connectFile `toml:"connect"`
}

type connectFile struct {
	SSHHost    string `toml:"ssh_host"`
	RemotePath string `toml:"remote_path"`
	ChromePath string `toml:"chrome_path"`
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
	if p.Connect != nil {
		cfg.ConnectSSHHost = p.Connect.SSHHost
		cfg.ConnectRemotePath = p.Connect.RemotePath
		cfg.ConnectChromePath = p.Connect.ChromePath
	}

	applyDefaults(cfg)
	return cfg, nil
}

// RemoteProfile is the subset of a server-side Echo configuration the
// `connect` command needs to mint a session on that host: the global
// compose command plus the per-project container/db mapping. It is
// assembled from the remote `global.toml` and `projects/<key>.toml`
// read over SSH, so nothing has to be re-declared on the local side.
type RemoteProfile struct {
	ComposeCmd    string
	OdooContainer string
	DBContainer   string
	DBName        string
	Stage         string
}

// ParseRemoteProfile decodes a remote host's `global.toml` and
// `projects/<key>.toml` bytes into a RemoteProfile. Either input may be
// empty (missing file); ComposeCmd falls back to the same default as a
// local config when the global file is absent or omits it.
func ParseRemoteProfile(globalTOML, projectTOML []byte) RemoteProfile {
	var g globalFile
	if len(globalTOML) > 0 {
		_ = toml.Unmarshal(globalTOML, &g)
	}
	var p projectFile
	if len(projectTOML) > 0 {
		_ = toml.Unmarshal(projectTOML, &p)
	}
	compose := g.ComposeCmd
	if compose == "" {
		compose = "docker compose"
	}
	return RemoteProfile{
		ComposeCmd:    compose,
		OdooContainer: p.OdooContainer,
		DBContainer:   p.DBContainer,
		DBName:        p.DBName,
		Stage:         p.Stage,
	}
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
	if cfg.ConnectSSHHost != "" || cfg.ConnectRemotePath != "" ||
		cfg.ConnectChromePath != "" {
		p.Connect = &connectFile{
			SSHHost:    cfg.ConnectSSHHost,
			RemotePath: cfg.ConnectRemotePath,
			ChromePath: cfg.ConnectChromePath,
		}
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
