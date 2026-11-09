package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// requirementsFile is the project's Python requirements at the repo root. An
// image recipe builds from a folder that mirrors the repo, so the file ships
// to the root of the push destination, next to the modules.
const requirementsFile = "requirements.txt"

// deployRequirements is what a deploy does with requirements.txt: the content
// it ships, where it comes from, and how it compares with the server's copy.
type deployRequirements struct {
	source string // lockSourceCommit, lockSourceRef or lockSourceWorktree
	sha    string // the commit the content is read at; empty for the working tree
	body   []byte
	local  string // sha256 hex of body
	remote string // sha256 hex of the server's file; empty when it has none
	dest   string // absolute server path; empty when the run has nowhere to put it
}

func (r *deployRequirements) changed() bool { return r != nil && r.dest != "" && r.local != r.remote }

func (r *deployRequirements) label() string {
	if r.sha == "" {
		return r.source
	}
	return r.source + "@" + shortSHA(r.sha)
}

// DeployRequirements reports the requirements.txt of a deploy under --json.
type DeployRequirements struct {
	Path    string `json:"path"`
	Source  string `json:"source"`
	SHA     string `json:"sha,omitempty"`
	SHA256  string `json:"sha256"`
	Remote  string `json:"remote_sha256,omitempty"`
	Changed bool   `json:"changed"`
	Synced  bool   `json:"synced"`
}

func pathsTouchRequirements(paths []string) bool {
	for _, p := range paths {
		if filepath.ToSlash(p) == requirementsFile {
			return true
		}
	}
	return false
}

// requirementsSourceFor picks the tree the run's requirements come from, the
// same way modules choose theirs: the newest selected commit, else the one
// ref every module is pinned to, else the working tree. A selection that is
// not one line of history falls back to the working tree.
func requirementsSourceFor(ctx context.Context, opts DeployOpts, commits []string, refs map[string]moduleSource, dirty []dirtyModule) (source, sha string) {
	if len(commits) > 0 {
		tip, err := resolveGitTip(ctx, opts.Root, commits)
		if err == nil {
			return lockSourceCommit, tip
		}
		opts.log("WARNING", "", "requirements: selected commits are not one line of history — using the working tree's", "")
		return lockSourceWorktree, ""
	}
	if len(dirty) == 0 && len(refs) > 0 {
		var pinned string
		for _, src := range refs {
			if pinned != "" && src.sha != pinned {
				return lockSourceWorktree, ""
			}
			pinned = src.sha
		}
		return lockSourceRef, pinned
	}
	return lockSourceWorktree, ""
}

// readRequirements returns the file at sha, or on disk when sha is empty; nil
// when that tree has none.
func readRequirements(ctx context.Context, root, sha string) (*deployRequirements, error) {
	var body []byte
	if sha == "" {
		b, err := os.ReadFile(filepath.Join(root, requirementsFile))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		body = b
	} else {
		listed, err := gitOutput(ctx, root, "ls-tree", "--name-only", sha, "--", requirementsFile)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(listed)) == "" {
			return nil, nil
		}
		if body, err = gitOutput(ctx, root, "show", sha+":"+requirementsFile); err != nil {
			return nil, err
		}
	}
	return &deployRequirements{sha: sha, body: body, local: sha256Hex(body)}, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// requirementsDelta lists the lines one side has and the other does not,
// ignoring blanks and comments, so the plan names the packages that move.
func requirementsDelta(from, to []byte) (added, removed []string) {
	lines := func(b []byte) map[string]bool {
		set := map[string]bool{}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				set[l] = true
			}
		}
		return set
	}
	a, b := lines(from), lines(to)
	for l := range b {
		if !a[l] {
			added = append(added, l)
		}
	}
	for l := range a {
		if !b[l] {
			removed = append(removed, l)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// readRemoteRequirements and writeRequirementsFile are the SSH seams of the
// requirements sync, overridable in tests.
var (
	readRemoteRequirements = func(ctx context.Context, host, file string) (body []byte, found bool, err error) {
		q := shellQuote(file)
		out, err := runSSH(ctx, host, "if [ -f "+q+" ]; then echo present; cat "+q+"; else echo absent; fi", nil)
		if err != nil {
			return nil, false, err
		}
		status, rest, _ := strings.Cut(string(out), "\n")
		if strings.TrimSpace(status) != "present" {
			return nil, false, nil
		}
		return []byte(rest), true, nil
	}
	writeRequirementsFile = func(ctx context.Context, host, file string, body []byte) error {
		tmp := shellQuote(file + ".echo-tmp")
		_, err := runSSH(ctx, host, "cat > "+tmp+" && mv -f "+tmp+" "+shellQuote(file), body)
		return err
	}
)

// planRequirements reads the server's copy under the explicit push
// destination and logs the plan line.
func planRequirements(ctx context.Context, opts DeployOpts, rsc remoteShellContext, req *deployRequirements, destBase string) error {
	req.dest = path.Join(destBase, requirementsFile)
	remote, found, err := readRemoteRequirements(ctx, rsc.sshHost, req.dest)
	if err != nil {
		return fmt.Errorf("read the server's %s: %w", requirementsFile, err)
	}
	if found {
		req.remote = sha256Hex(remote)
	}
	fields := [][2]string{{"changed", "no"}, {"ship", req.label()}}
	if req.changed() {
		added, removed := requirementsDelta(remote, req.body)
		fields = [][2]string{{"changed", "yes"}, {"ship", req.label()},
			{"added", strings.Join(added, ",")}, {"removed", strings.Join(removed, ",")}}
	}
	fields = append(fields, [2]string{"local", shortSHA(req.local)},
		[2]string{"remote", orNone(shortSHA(req.remote))}, [2]string{"dest", req.dest})
	opts.log("INFO", "plan", "requirements", rsc.prof.DBName, fields...)
	return nil
}

func (r *deployRequirements) result(synced bool) *DeployRequirements {
	if r == nil {
		return nil
	}
	return &DeployRequirements{
		Path: requirementsFile, Source: r.source, SHA: r.sha, SHA256: r.local,
		Remote: r.remote, Changed: r.changed(), Synced: synced,
	}
}

// planRequirementsEntry is the requirements.txt a saved plan ships.
type planRequirementsEntry struct {
	Source string `json:"source"`
	SHA    string `json:"sha,omitempty"`
	SHA256 string `json:"sha256"`
	Remote string `json:"remote_sha256,omitempty"`
	Dest   string `json:"dest,omitempty"`
}

func (r *deployRequirements) planEntry() *planRequirementsEntry {
	if r == nil {
		return nil
	}
	return &planRequirementsEntry{Source: r.source, SHA: r.sha, SHA256: r.local, Remote: r.remote, Dest: r.dest}
}
