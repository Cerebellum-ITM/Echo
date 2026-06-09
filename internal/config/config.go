package config

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
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

	// FilestorePath is the Odoo filestore base dir inside the container
	// (default /var/lib/odoo/filestore). Used by db-backup/db-restore to
	// read/write the filestore via `docker cp`.
	FilestorePath string

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

	// ConnectTargets are named remote `connect` destinations stored
	// globally (in global.toml), used by the projectless `echo connect
	// <name>` direct mode. Sorted by Name.
	ConnectTargets []ConnectTarget

	// Prompt segmentation (global).
	PromptSegments []string
	PromptNameMax  int
	HealthTTL      time.Duration
}

type globalFile struct {
	Theme          string                        `toml:"theme"`
	Logo           string                        `toml:"logo"`
	ComposeCmd     string                        `toml:"compose_cmd"`
	Prompt         *promptFile                   `toml:"prompt"`
	ConnectTargets map[string]*connectTargetFile `toml:"connect_targets"`
}

type connectTargetFile struct {
	SSHHost    string `toml:"ssh_host"`
	RemotePath string `toml:"remote_path"`
	ChromePath string `toml:"chrome_path"`
	DBName     string `toml:"db_name"`
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
	ProjectPath    string       `toml:"project_path"`
	FilestorePath  string       `toml:"filestore_path"`
	Connect        *connectFile `toml:"connect"`
}

type connectFile struct {
	SSHHost    string `toml:"ssh_host"`
	RemotePath string `toml:"remote_path"`
	ChromePath string `toml:"chrome_path"`
}

// ConnectTarget is a named remote destination for the projectless
// `connect` direct mode. SSHHost is an alias from the user's
// ~/.ssh/config; RemotePath is the project dir on that server (used to
// locate its Echo profile and to `cd` before minting).
type ConnectTarget struct {
	Name       string
	SSHHost    string
	RemotePath string
	ChromePath string
	DBName     string // display only
}

// ProjectInfo is the subset of a project profile needed to present a
// server's Echo projects in the target-registration picker. Decoded
// from a single `projects/<key>.toml` read over SSH.
type ProjectInfo struct {
	ProjectPath   string
	DBName        string
	OdooContainer string
	Stage         string
}

// ParseProjectInfo decodes one project profile's TOML bytes. A profile
// without `project_path` (written by an older Echo) yields an empty
// ProjectPath and is unusable as a connect target.
func ParseProjectInfo(projectTOML []byte) ProjectInfo {
	var p projectFile
	if len(projectTOML) > 0 {
		_ = toml.Unmarshal(projectTOML, &p)
	}
	return ProjectInfo{
		ProjectPath:   p.ProjectPath,
		DBName:        p.DBName,
		OdooContainer: p.OdooContainer,
		Stage:         p.Stage,
	}
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
	cfg.ConnectTargets = sortedConnectTargets(g.ConnectTargets)
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
	if p.ProjectPath != "" {
		cfg.ProjectPath = p.ProjectPath
	}
	cfg.FilestorePath = p.FilestorePath
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
		Theme:          cfg.Theme,
		Logo:           cfg.Logo,
		ComposeCmd:     cfg.ComposeCmd,
		ConnectTargets: connectTargetsToFile(cfg.ConnectTargets),
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
		ProjectPath:    cfg.ProjectPath,
		FilestorePath:  cfg.FilestorePath,
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

// sortedConnectTargets converts the global file's target map into a
// name-sorted slice for stable display.
func sortedConnectTargets(m map[string]*connectTargetFile) []ConnectTarget {
	if len(m) == 0 {
		return nil
	}
	out := make([]ConnectTarget, 0, len(m))
	for name, t := range m {
		if t == nil {
			continue
		}
		out = append(out, ConnectTarget{
			Name:       name,
			SSHHost:    t.SSHHost,
			RemotePath: t.RemotePath,
			ChromePath: t.ChromePath,
			DBName:     t.DBName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func connectTargetsToFile(targets []ConnectTarget) map[string]*connectTargetFile {
	if len(targets) == 0 {
		return nil
	}
	m := make(map[string]*connectTargetFile, len(targets))
	for _, t := range targets {
		m[t.Name] = &connectTargetFile{
			SSHHost:    t.SSHHost,
			RemotePath: t.RemotePath,
			ChromePath: t.ChromePath,
			DBName:     t.DBName,
		}
	}
	return m
}

// LoadGlobal reads only the global config (no project), for the
// projectless `connect` direct mode. Missing file → defaults.
func LoadGlobal() (*Config, error) {
	root, err := configRoot()
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	var g globalFile
	if data, err := os.ReadFile(filepath.Join(root, "global.toml")); err == nil {
		_ = toml.Unmarshal(data, &g)
	}
	cfg.Theme = g.Theme
	cfg.Logo = g.Logo
	cfg.ComposeCmd = g.ComposeCmd
	cfg.ConnectTargets = sortedConnectTargets(g.ConnectTargets)
	applyDefaults(cfg)
	return cfg, nil
}

// SaveConnectTarget inserts or replaces a named connect target in the
// global config, preserving every other global field.
func SaveConnectTarget(t ConnectTarget) error {
	cfg, err := LoadGlobal()
	if err != nil {
		return err
	}
	replaced := false
	for i := range cfg.ConnectTargets {
		if cfg.ConnectTargets[i].Name == t.Name {
			cfg.ConnectTargets[i] = t
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.ConnectTargets = append(cfg.ConnectTargets, t)
	}
	return SaveGlobal(cfg)
}

// BackfillProjectPath silently migrates a pre-existing project profile
// that predates the `project_path` field: if a profile file exists but
// stored no path, it rewrites it with cfg.ProjectPath so the project
// becomes discoverable as a remote connect target. No-op when the
// profile is absent or already carries a path. Returns true if it wrote.
func BackfillProjectPath(cfg *Config) (bool, error) {
	root, err := configRoot()
	if err != nil {
		return false, err
	}
	path := filepath.Join(root, "projects", cfg.ProjectKey+".toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil // no profile yet → nothing to migrate
	}
	var p projectFile
	if err := toml.Unmarshal(data, &p); err != nil {
		return false, nil
	}
	if p.ProjectPath != "" {
		return false, nil // already migrated
	}
	if err := SaveProject(cfg); err != nil {
		return false, err
	}
	return true, nil
}
