package config

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// ConnectSession is a previously minted Odoo web session cached locally so
// `connect <login>` can reuse the cookie instead of re-querying users and
// re-minting on every run. One entry per (target, login); SID is the Odoo
// `session_id` value that already lives in the container's session store.
type ConnectSession struct {
	Login    string    `toml:"login"`
	UID      int       `toml:"uid"`
	SID      string    `toml:"sid"`
	BaseURL  string    `toml:"base_url"`
	MintedAt time.Time `toml:"minted_at"`
}

// connectSessionsFile is the on-disk shape of a per-target cache file:
// `~/.config/echo/connect-sessions/<key>.toml`, a table of login → session.
type connectSessionsFile struct {
	Sessions map[string]ConnectSession `toml:"sessions"`
}

// connectSessionsPath returns the cache file path for the given target key
// (a hash of the connect destination identity, computed by the caller).
func connectSessionsPath(key string) (string, error) {
	root, err := configRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "connect-sessions", key+".toml"), nil
}

// LoadConnectSessions reads the cached sessions for a target. A missing or
// unparseable file yields an empty (non-nil) map and no error: the cache
// is a best-effort optimization, never a hard dependency.
func LoadConnectSessions(key string) map[string]ConnectSession {
	path, err := connectSessionsPath(key)
	if err != nil {
		return map[string]ConnectSession{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]ConnectSession{}
	}
	var f connectSessionsFile
	if _, err := toml.Decode(string(data), &f); err != nil || f.Sessions == nil {
		return map[string]ConnectSession{}
	}
	return f.Sessions
}

// SaveConnectSession upserts one session (keyed by login) into the target's
// cache file, preserving the other logins, and writes it atomically.
func SaveConnectSession(key string, s ConnectSession) error {
	path, err := connectSessionsPath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sessions := LoadConnectSessions(key)
	sessions[s.Login] = s

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(connectSessionsFile{Sessions: sessions}); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}
