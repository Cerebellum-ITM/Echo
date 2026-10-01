package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// codeCheckpointMethod marks a checkpoint entry that restores code only: a
// failed deploy without a DB checkpoint whose rollback was declined.
const codeCheckpointMethod = "code"

// codeSnapshotDir is the remote project-relative directory code snapshots
// live in, next to the dump checkpoints.
const codeSnapshotDir = "backups/code"

// codeSnapshot is a server-side copy of what a deploy is about to overwrite:
// the destination directory of every module it rsyncs, plus the deploy lock.
// Absent lists the paths that did not exist yet (a module being installed),
// which a restore deletes instead of extracting.
type codeSnapshot struct {
	Name   string            `json:"name"`
	Dests  map[string]string `json:"dests"`
	Absent []string          `json:"absent,omitempty"`
}

func codeSnapshotTarball(name string) string { return codeSnapshotDir + "/" + name + ".tar.gz" }
func codeSnapshotSidecar(name string) string { return codeSnapshotDir + "/" + name + ".json" }

// paths is every remote path the snapshot covers, sorted: the module
// directories and the lock file.
func (s codeSnapshot) paths(remotePath string) []string {
	out := make([]string, 0, len(s.Dests)+1)
	for _, d := range s.Dests {
		out = append(out, d)
	}
	sort.Strings(out)
	return append(out, lockFilePath(remotePath))
}

// checkSnapshotPaths refuses any path a restore could not safely `rm -rf`:
// relative, unclean, or shallower than two levels below the root.
func checkSnapshotPaths(paths []string) error {
	for _, p := range paths {
		if !path.IsAbs(p) || path.Clean(p) != p || strings.Count(p, "/") < 2 {
			return fmt.Errorf("code snapshot: refusing unsafe path %q", p)
		}
	}
	return nil
}

// createCodeSnapshot tars the given module destinations and the lock on the
// server, before the deploy writes any of them.
func createCodeSnapshot(ctx context.Context, rsc remoteShellContext, dests map[string]string, log logFn) (codeSnapshot, error) {
	db := rsc.prof.DBName
	snap := codeSnapshot{
		Name:  "code_" + sanitizeDBName(db) + "_" + time.Now().Format("20060102_150405"),
		Dests: dests,
	}
	paths := snap.paths(rsc.remotePath)
	if err := checkSnapshotPaths(paths); err != nil {
		return codeSnapshot{}, err
	}
	out, err := ckptRunSSH(ctx, rsc.sshHost, codeSnapshotScript(rsc.remotePath, snap.Name, paths), nil)
	if err != nil {
		return codeSnapshot{}, fmt.Errorf("code snapshot: %w", err)
	}
	snap.Absent = nonEmptyLines(string(out))
	body, _ := json.Marshal(snap)
	sidecar := "cd " + shellQuote(rsc.remotePath) + " && cat > " + shellQuote(codeSnapshotSidecar(snap.Name))
	if _, err := ckptRunSSH(ctx, rsc.sshHost, sidecar, body); err != nil {
		return codeSnapshot{}, fmt.Errorf("code snapshot: %w", err)
	}
	ckptLog(log, "INFO", "snapshot", "code snapshot taken", db,
		[2]string{"name", snap.Name}, [2]string{"modules", strconv.Itoa(len(dests))})
	return snap, nil
}

// codeSnapshotScript tars the existing paths (relative to / so a restore
// extracts them in place) and prints the absent ones. The snapshot directory
// ignores itself like the lock's does, so it never shows in a repository at
// remote_path.
func codeSnapshotScript(remotePath, name string, paths []string) string {
	dir := shellQuote(path.Join(remotePath, codeSnapshotDir))
	tarball := shellQuote(path.Join(remotePath, codeSnapshotTarball(name)))
	var list []string
	for _, p := range paths {
		list = append(list, shellQuote(strings.TrimPrefix(p, "/")))
	}
	return "set -e; mkdir -p " + dir +
		"; [ -f " + dir + "/.gitignore ] || printf '*\\n' > " + dir + "/.gitignore" +
		"; cd /; set --; for p in " + strings.Join(list, " ") +
		"; do if [ -e \"$p\" ]; then set -- \"$@\" \"$p\"; else printf '/%s\\n' \"$p\"; fi; done" +
		"; if [ $# -gt 0 ]; then tar -czf " + tarball + " \"$@\"; else tar -czf " + tarball + " -T /dev/null; fi"
}

// restoreCodeSnapshot deletes every path the snapshot covers and extracts it
// back, so files the failed run added are gone and the paths that did not
// exist before do not exist after. -P keeps bsdtar from refusing to extract
// through a symlinked parent (/var on macOS, a symlinked /home on a server);
// the member names are relative either way.
func restoreCodeSnapshot(ctx context.Context, rsc remoteShellContext, snap codeSnapshot, log logFn) error {
	paths := snap.paths(rsc.remotePath)
	if err := checkSnapshotPaths(paths); err != nil {
		return err
	}
	var list []string
	for _, p := range paths {
		list = append(list, shellQuote(strings.TrimPrefix(p, "/")))
	}
	script := "set -e; cd /; for p in " + strings.Join(list, " ") + "; do rm -rf -- \"$p\"; done" +
		"; tar -xPzf " + shellQuote(path.Join(rsc.remotePath, codeSnapshotTarball(snap.Name)))
	if _, err := ckptRunSSH(ctx, rsc.sshHost, script, nil); err != nil {
		return fmt.Errorf("restore code snapshot %s: %w", snap.Name, err)
	}
	ckptLog(log, "INFO", "rollback", "code restored", rsc.prof.DBName,
		[2]string{"snapshot", snap.Name}, [2]string{"modules", strconv.Itoa(len(snap.Dests))})
	return nil
}

// readCodeSnapshot loads a snapshot's sidecar, for a restore outside the run
// that took it (deploy --rollback).
func readCodeSnapshot(ctx context.Context, rsc remoteShellContext, name string) (codeSnapshot, error) {
	out, err := ckptRunSSH(ctx, rsc.sshHost,
		"cd "+shellQuote(rsc.remotePath)+" && cat "+shellQuote(codeSnapshotSidecar(name)), nil)
	if err != nil {
		return codeSnapshot{}, fmt.Errorf("read code snapshot %s: %w", name, err)
	}
	var snap codeSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		return codeSnapshot{}, fmt.Errorf("read code snapshot %s: %w", name, err)
	}
	return snap, nil
}

func destroyCodeSnapshot(ctx context.Context, rsc remoteShellContext, name string) error {
	_, err := ckptRunSSH(ctx, rsc.sshHost, "cd "+shellQuote(rsc.remotePath)+" && rm -f "+
		shellQuote(codeSnapshotTarball(name))+" "+shellQuote(codeSnapshotSidecar(name)), nil)
	return err
}
