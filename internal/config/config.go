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
	Theme      string
	Logo       string
	Banner     string
	ComposeCmd string
	// Icons controls nerd-font file-type glyphs in rich output (e.g. the
	// push change tree, the modules list): "auto" (default — on for an
	// interactive terminal that isn't a known plain one), "on", or "off".
	// The ECHO_ICONS env var overrides it.
	Icons         string
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

	// AddonsMode selects how module listing resolves addons paths:
	// "host" (default) scans folders under the project root on the host;
	// "conf" reads `addons_path` from the instance's odoo.conf inside the
	// container and lists modules there. AddonsPaths holds host-relative
	// subpaths in host mode, absolute container paths in conf mode.
	AddonsMode string

	// ConfPath is the odoo.conf location inside the Odoo container, read
	// in conf mode to discover addons paths (default /etc/odoo/odoo.conf).
	ConfPath string

	// ScriptsDir is the directory `shell-run` lists `.py` scripts from
	// (top-level, non-recursive). Empty means the project root. A relative
	// value is resolved against the project root; an absolute one is used
	// as-is. Kept out of the addons tree so the picker doesn't scan the
	// thousands of modules' `.py`.
	ScriptsDir string

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

	// ProjectAliases map a short name to a local project directory, stored
	// globally (in global.toml under [project_aliases]). They let `-C
	// <alias>` stand in for `-C <dir>`. Distinct from ConnectTargets (which
	// are remote) though they may share names by convention.
	ProjectAliases map[string]string

	// Prompt segmentation (global).
	PromptSegments []string
	PromptNameMax  int
	HealthTTL      time.Duration

	// LogDBMax is the max display width (runes) of the database name in log
	// lines before it's middle-truncated, so a long name doesn't wrap the
	// rest of the line. Global; default 20. Display-only.
	LogDBMax int

	// Command-log history ([cmd_logs] in global.toml). Every dispatched
	// command's captured output is persisted as one JSON record per run
	// under ~/.config/echo/cmd-logs/<key>/ unless CmdLogsDisabled.
	// Retention is pruned opportunistically: CmdLogsRetentionDays age pass
	// (0 = keep forever) then CmdLogsMaxRuns count pass (0 = unlimited).
	// Defaults when the section is absent: 7 days, 500 runs, enabled.
	CmdLogsRetentionDays int
	CmdLogsMaxRuns       int
	CmdLogsDisabled      bool

	// Checkpoint ([checkpoint], global + per-project, project wins) — the
	// DB checkpoint/rollback behavior of `deploy` (Unit 89). Mode is
	// "auto" (checkpoint on staging/prod, off on dev), "on" (always) or
	// "off" (never). Method is "db" (CREATE DATABASE … TEMPLATE, fast
	// file-level copy) or "dump" (pg_dump kept server-side). Keep is how
	// many checkpoints to retain per target before the oldest are pruned.
	CheckpointMode   string
	CheckpointMethod string
	CheckpointKeep   int

	// Push ([push], global + per-project, project wins) — the explicit
	// destination directory for `push` (Unit 91). PushPath, when set,
	// overrides the auto-detected addons dir: every module lands at
	// <PushPath>/<module>. A relative path is joined under the target's
	// remotePath; an absolute one is used as-is. PushMkdir (pointer to
	// distinguish unset from explicit false) requests `mkdir -p` of the
	// destination before syncing.
	PushPath  string
	PushMkdir *bool
}

type globalFile struct {
	Theme          string                        `toml:"theme"`
	Logo           string                        `toml:"logo"`
	Banner         string                        `toml:"banner"`
	ComposeCmd     string                        `toml:"compose_cmd"`
	Icons          string                        `toml:"icons"`
	LogDBMax       int                           `toml:"log_db_max"`
	Prompt         *promptFile                   `toml:"prompt"`
	CmdLogs        *cmdLogsFile                  `toml:"cmd_logs"`
	Checkpoint     *checkpointConfig             `toml:"checkpoint"`
	Push           *pushConfig                   `toml:"push"`
	ConnectTargets map[string]*connectTargetFile `toml:"connect_targets"`
	ProjectAliases map[string]string             `toml:"project_aliases"`
}

// pushConfig is the [push] table, valid in both global.toml and a project
// profile. A nil pointer (section absent) leaves push on auto-detect; a
// present table declares the explicit destination, project over global.
// Mkdir is a pointer so an explicit `mkdir = false` is distinguishable from
// an absent key (which falls through to the other side of the merge).
type pushConfig struct {
	Path  string `toml:"path"`
	Mkdir *bool  `toml:"mkdir"`
}

// checkpointConfig is the [checkpoint] table, valid in both global.toml and a
// project profile. A nil pointer (section absent) leaves the built-in
// defaults; a present table overrides field by field, project over global.
type checkpointConfig struct {
	Mode   string `toml:"mode"`
	Method string `toml:"method"`
	Keep   int    `toml:"keep"`
}

// cmdLogsFile is the [cmd_logs] table. A nil pointer (section absent) means
// "use the built-in defaults"; a present table is honored verbatim, so an
// explicit 0 keeps records forever / unlimited rather than reverting to the
// default.
type cmdLogsFile struct {
	RetentionDays int  `toml:"retention_days"`
	MaxRuns       int  `toml:"max_runs"`
	Disabled      bool `toml:"disabled"`
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
	OdooVersion    string            `toml:"odoo_version"`
	OdooContainer  string            `toml:"odoo_container"`
	DBContainer    string            `toml:"db_container"`
	DBName         string            `toml:"db_name"`
	Stage          string            `toml:"stage"`
	AddonsPaths    []string          `toml:"addons_paths"`
	AddonsMode     string            `toml:"addons_mode"`
	ConfPath       string            `toml:"conf_path"`
	ScriptsDir     string            `toml:"scripts_dir"`
	ComposeProject string            `toml:"compose_project"`
	ProjectPath    string            `toml:"project_path"`
	FilestorePath  string            `toml:"filestore_path"`
	Connect        *connectFile      `toml:"connect"`
	Checkpoint     *checkpointConfig `toml:"checkpoint"`
	Push           *pushConfig       `toml:"push"`
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
	cfg.Banner = g.Banner
	cfg.ComposeCmd = g.ComposeCmd
	cfg.Icons = g.Icons
	cfg.LogDBMax = g.LogDBMax
	applyCmdLogs(cfg, g.CmdLogs)
	applyCheckpoint(cfg, g.Checkpoint)
	applyPush(cfg, g.Push)
	cfg.ConnectTargets = sortedConnectTargets(g.ConnectTargets)
	cfg.ProjectAliases = g.ProjectAliases
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
	cfg.AddonsMode = p.AddonsMode
	cfg.ConfPath = p.ConfPath
	cfg.ScriptsDir = p.ScriptsDir
	cfg.ComposeProject = p.ComposeProject
	if p.ProjectPath != "" {
		cfg.ProjectPath = p.ProjectPath
	}
	cfg.FilestorePath = p.FilestorePath
	// The project [checkpoint] overrides the global one field by field (a
	// blank/zero field leaves the global/default value untouched).
	if p.Checkpoint != nil {
		if p.Checkpoint.Mode != "" {
			cfg.CheckpointMode = p.Checkpoint.Mode
		}
		if p.Checkpoint.Method != "" {
			cfg.CheckpointMethod = p.Checkpoint.Method
		}
		if p.Checkpoint.Keep != 0 {
			cfg.CheckpointKeep = p.Checkpoint.Keep
		}
	}
	// The project [push] overrides the global one field by field (a blank
	// path / absent mkdir leaves the global/default value untouched).
	if p.Push != nil {
		if p.Push.Path != "" {
			cfg.PushPath = p.Push.Path
		}
		if p.Push.Mkdir != nil {
			cfg.PushMkdir = p.Push.Mkdir
		}
	}
	if p.Connect != nil {
		cfg.ConnectSSHHost = p.Connect.SSHHost
		cfg.ConnectRemotePath = p.Connect.RemotePath
		cfg.ConnectChromePath = p.Connect.ChromePath
	}

	applyDefaults(cfg)
	return cfg, nil
}

// applyPush maps the global [push] table onto the config. A nil table
// (section absent) leaves push on auto-detect (empty path, nil mkdir); a
// present table is honored verbatim. The project profile refines this
// further in Load.
func applyPush(cfg *Config, f *pushConfig) {
	if f == nil {
		return
	}
	cfg.PushPath = f.Path
	cfg.PushMkdir = f.Mkdir
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
	OdooVersion   string

	// Addons discovery (conf-mode), used by i18n-pull to list the remote
	// project's own modules rather than every installed module.
	AddonsMode  string
	AddonsPaths []string
	ConfPath    string

	// Checkpoint policy declared on the SERVER ([checkpoint] in the remote
	// global.toml + project profile, project wins). Empty/zero when the
	// server doesn't declare it — the client then falls back to its own
	// local [checkpoint] (Unit 90). No defaults are applied here.
	CheckpointMode   string
	CheckpointMethod string
	CheckpointKeep   int

	// Push destination declared on the SERVER ([push] in the remote
	// global.toml + project profile, project wins). Empty PushPath / nil
	// PushMkdir when the server doesn't declare it — the client then falls
	// back to its own local [push] (Unit 91). No defaults are applied here.
	PushPath  string
	PushMkdir *bool
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
	// Server-side [checkpoint] policy: global table, then project override
	// field-by-field. No defaults — an absent section stays empty/zero so the
	// client falls back to its own local config.
	var cp checkpointConfig
	if g.Checkpoint != nil {
		cp = *g.Checkpoint
	}
	if p.Checkpoint != nil {
		if p.Checkpoint.Mode != "" {
			cp.Mode = p.Checkpoint.Mode
		}
		if p.Checkpoint.Method != "" {
			cp.Method = p.Checkpoint.Method
		}
		if p.Checkpoint.Keep != 0 {
			cp.Keep = p.Checkpoint.Keep
		}
	}
	// Server-side [push] destination: global table, then project override
	// field-by-field. No defaults — an absent section stays empty so the
	// client falls back to its own local config.
	var push pushConfig
	if g.Push != nil {
		push = *g.Push
	}
	if p.Push != nil {
		if p.Push.Path != "" {
			push.Path = p.Push.Path
		}
		if p.Push.Mkdir != nil {
			push.Mkdir = p.Push.Mkdir
		}
	}
	return RemoteProfile{
		ComposeCmd:       compose,
		OdooContainer:    p.OdooContainer,
		DBContainer:      p.DBContainer,
		DBName:           p.DBName,
		Stage:            p.Stage,
		OdooVersion:      p.OdooVersion,
		AddonsMode:       p.AddonsMode,
		AddonsPaths:      p.AddonsPaths,
		ConfPath:         p.ConfPath,
		CheckpointMode:   cp.Mode,
		CheckpointMethod: cp.Method,
		CheckpointKeep:   cp.Keep,
		PushPath:         push.Path,
		PushMkdir:        push.Mkdir,
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
		Banner:         cfg.Banner,
		ComposeCmd:     cfg.ComposeCmd,
		LogDBMax:       cfg.LogDBMax,
		ConnectTargets: connectTargetsToFile(cfg.ConnectTargets),
		ProjectAliases: cfg.ProjectAliases,
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
	// Preserve a non-default [cmd_logs] section across rewrites; a pure
	// default config leaves it out so global.toml stays clean.
	if cfg.CmdLogsDisabled ||
		cfg.CmdLogsRetentionDays != Defaults.CmdLogsRetentionDays ||
		cfg.CmdLogsMaxRuns != Defaults.CmdLogsMaxRuns {
		g.CmdLogs = &cmdLogsFile{
			RetentionDays: cfg.CmdLogsRetentionDays,
			MaxRuns:       cfg.CmdLogsMaxRuns,
			Disabled:      cfg.CmdLogsDisabled,
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
		AddonsMode:     cfg.AddonsMode,
		ConfPath:       cfg.ConfPath,
		ScriptsDir:     cfg.ScriptsDir,
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
	// Persist a declared [push] destination so a picked/configured path
	// survives across sessions; a pure auto-detect config leaves it out.
	if cfg.PushPath != "" || cfg.PushMkdir != nil {
		p.Push = &pushConfig{Path: cfg.PushPath, Mkdir: cfg.PushMkdir}
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
	cfg.Banner = g.Banner
	cfg.ComposeCmd = g.ComposeCmd
	cfg.Icons = g.Icons
	cfg.LogDBMax = g.LogDBMax
	applyCmdLogs(cfg, g.CmdLogs)
	applyCheckpoint(cfg, g.Checkpoint)
	cfg.ConnectTargets = sortedConnectTargets(g.ConnectTargets)
	cfg.ProjectAliases = g.ProjectAliases
	applyDefaults(cfg)
	return cfg, nil
}

// applyCmdLogs maps the [cmd_logs] table onto the config. A nil table
// (section absent) applies the built-in defaults (7 days, 500 runs,
// enabled); a present table is honored verbatim so an explicit 0 means
// "keep forever" / "unlimited".
func applyCmdLogs(cfg *Config, f *cmdLogsFile) {
	if f == nil {
		cfg.CmdLogsRetentionDays = Defaults.CmdLogsRetentionDays
		cfg.CmdLogsMaxRuns = Defaults.CmdLogsMaxRuns
		cfg.CmdLogsDisabled = false
		return
	}
	cfg.CmdLogsRetentionDays = f.RetentionDays
	cfg.CmdLogsMaxRuns = f.MaxRuns
	cfg.CmdLogsDisabled = f.Disabled
}

// applyCheckpoint maps the global [checkpoint] table onto the config. A nil
// table applies the built-in defaults (auto / db / keep 2); a present table
// overrides only its non-blank/non-zero fields, so a partial section still
// inherits the rest. The project profile refines this further in Load.
func applyCheckpoint(cfg *Config, f *checkpointConfig) {
	cfg.CheckpointMode = Defaults.CheckpointMode
	cfg.CheckpointMethod = Defaults.CheckpointMethod
	cfg.CheckpointKeep = Defaults.CheckpointKeep
	if f == nil {
		return
	}
	if f.Mode != "" {
		cfg.CheckpointMode = f.Mode
	}
	if f.Method != "" {
		cfg.CheckpointMethod = f.Method
	}
	if f.Keep != 0 {
		cfg.CheckpointKeep = f.Keep
	}
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
