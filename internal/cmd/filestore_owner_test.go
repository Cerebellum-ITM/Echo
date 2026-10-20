package cmd

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOwnerTools puts a `stat` that answers `stat -c %u:%g <path>` from a
// fixed table (root for anything not listed, failing for a missing path) and
// a `chown` that only records its arguments first on PATH. The real tools
// would need root, and BSD stat has no -c.
func fakeOwnerTools(t *testing.T, owners map[string]string) (chownLog string) {
	t.Helper()
	bin := t.TempDir()
	chownLog = filepath.Join(bin, "chown.log")
	var table strings.Builder
	for path, owner := range owners {
		table.WriteString(`[ "$3" = "` + path + `" ] && { echo ` + owner + `; exit 0; }` + "\n")
	}
	stat := "#!/bin/sh\n[ -e \"$3\" ] || exit 1\n" + table.String() + "echo 0:0\n"
	chown := "#!/bin/sh\necho \"$*\" >> " + chownLog + "\n"
	for name, body := range map[string]string{"stat": stat, "chown": chown} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return chownLog
}

func chownCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("no chown ran: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func filestoreArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("attachment")
	if err := tw.WriteHeader(&tar.Header{Name: "source_db/ab/abcdef", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func runFilestoreScript(t *testing.T, script string, stdin []byte, args ...string) {
	t.Helper()
	c := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	c.Stdin = bytes.NewReader(stdin)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
}

// On a new instance the filestore root does not exist yet: it and the pushed
// database's tree must take the data_dir's owner, not root's.
func TestDBPushFilestoreScriptOwnsAMissingRootLikeDataDir(t *testing.T) {
	dataDir := t.TempDir()
	fs := filepath.Join(dataDir, "filestore")
	log := fakeOwnerTools(t, map[string]string{dataDir: "100:101"})

	runFilestoreScript(t, dbPushFilestoreScript, filestoreArchive(t), fs, "habitta", "20261020-120000")

	if _, err := os.Stat(filepath.Join(fs, "habitta", "ab", "abcdef")); err != nil {
		t.Fatalf("archive not extracted under the database name: %v", err)
	}
	want := []string{"100:101 " + fs, "-R 100:101 " + fs + "/habitta"}
	if got := chownCalls(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("chown calls = %q, want %q", got, want)
	}
}

// A root left owned by root (by an earlier push) is handed back too, and the
// previous database filestore is kept aside.
func TestDBPushFilestoreScriptRepairsARootOwnedRoot(t *testing.T) {
	dataDir := t.TempDir()
	fs := filepath.Join(dataDir, "filestore")
	if err := os.MkdirAll(filepath.Join(fs, "habitta"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := fakeOwnerTools(t, map[string]string{dataDir: "100:101", fs: "0:0"})

	runFilestoreScript(t, dbPushFilestoreScript, filestoreArchive(t), fs, "habitta", "20261020-120000")

	if _, err := os.Stat(filepath.Join(fs, "habitta.pre-push-20261020-120000")); err != nil {
		t.Errorf("previous filestore not kept: %v", err)
	}
	if got := chownCalls(t, log); got[0] != "100:101 "+fs || got[1] != "-R 100:101 "+fs+"/habitta" {
		t.Errorf("chown calls = %q, want the data_dir owner on the root and the database", got)
	}
}

func TestFilestoreChownScriptUsesTheDataDirOwner(t *testing.T) {
	dataDir := t.TempDir()
	fs := filepath.Join(dataDir, "filestore")
	dst := filepath.Join(fs, "restored")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	log := fakeOwnerTools(t, map[string]string{dataDir: "100:101", fs: "0:0"})

	runFilestoreScript(t, filestoreChownScript, nil, fs, dst)

	want := []string{"100:101 " + fs, "-R 100:101 " + dst}
	if got := chownCalls(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("chown calls = %q, want %q", got, want)
	}
}
