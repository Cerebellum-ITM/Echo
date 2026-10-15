package config

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// RemoteInitFields are the project-profile fields `init --remote` owns. Every
// other key of an existing server profile is carried over untouched.
type RemoteInitFields struct {
	OdooVersion   string
	Stage         string
	OdooContainer string
	DBContainer   string
	DBName        string
	ProjectPath   string
}

// ErrReverbProfile marks a server profile carrying the [reverb] marker:
// Reverb writes that file and a client must not rewrite it.
var ErrReverbProfile = errors.New("profile is owned by Reverb; edit it there")

// UnknownKeysError reports keys of a server profile this Echo cannot decode
// (written by a newer Echo). Re-encoding would silently drop them.
type UnknownKeysError struct {
	Keys []string
}

func (e *UnknownKeysError) Error() string {
	return "server profile has keys this Echo does not know (" + strings.Join(e.Keys, ", ") +
		"); update Echo before rewriting it"
}

// DecodeRemoteProject reads an existing server project profile for
// `init --remote`, refusing the two cases RenderRemoteProject could not
// rewrite without loss. Empty input yields a zero profile.
func DecodeRemoteProject(existing []byte) (RemoteInitFields, error) {
	p, err := decodeRemoteProject(existing)
	if err != nil {
		return RemoteInitFields{}, err
	}
	return RemoteInitFields{
		OdooVersion:   p.OdooVersion,
		Stage:         p.Stage,
		OdooContainer: p.OdooContainer,
		DBContainer:   p.DBContainer,
		DBName:        p.DBName,
		ProjectPath:   p.ProjectPath,
	}, nil
}

// RenderRemoteProject returns the server project profile with f applied on
// top of existing (empty for a fresh file).
func RenderRemoteProject(existing []byte, f RemoteInitFields) ([]byte, error) {
	p, err := decodeRemoteProject(existing)
	if err != nil {
		return nil, err
	}
	p.OdooVersion = f.OdooVersion
	p.Stage = f.Stage
	p.OdooContainer = f.OdooContainer
	p.DBContainer = f.DBContainer
	p.DBName = f.DBName
	p.ProjectPath = f.ProjectPath
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DeclaredComposeCmd returns the compose_cmd a server global.toml declares,
// or "" when it declares none (ParseRemoteProfile hides that by defaulting).
func DeclaredComposeCmd(globalTOML []byte) (string, error) {
	var g globalFile
	if err := decodeTOML(GlobalFileName, globalTOML, &g); err != nil {
		return "", err
	}
	return g.ComposeCmd, nil
}

// RenderRemoteGlobalCompose is the whole server global.toml written when the
// host only has docker-compose v1 and no global.toml exists yet.
func RenderRemoteGlobalCompose(composeCmd string) []byte {
	return []byte(fmt.Sprintf("compose_cmd = %q\n", composeCmd))
}

func decodeRemoteProject(existing []byte) (projectFile, error) {
	var p projectFile
	if len(bytes.TrimSpace(existing)) == 0 {
		return p, nil
	}
	md, err := toml.Decode(string(existing), &p)
	if err != nil {
		return p, &ParseError{Path: projectProfileName, Err: err}
	}
	if p.Reverb != nil {
		return p, ErrReverbProfile
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return p, &UnknownKeysError{Keys: keys}
	}
	return p, nil
}
