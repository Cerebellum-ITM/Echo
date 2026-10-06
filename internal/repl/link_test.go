package repl

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestLinkListJSONPrintsOnlyJSON(t *testing.T) {
	sess := cmdLogSession(t)
	wireCapture(t, sess)
	sess.cfg.ConnectTargets = []config.ConnectTarget{{Name: "stg", SSHHost: "stg.example", RemotePath: "/srv/odoo"}}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	sess.dispatchParsed(context.Background(), "link", []string{"--list", "--json"})
	os.Stdout = stdout
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if sess.exitCode != exitOK {
		t.Fatalf("exit = %d", sess.exitCode)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout has %d lines, want only the JSON: %q", len(lines), out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rows); err != nil || len(rows) != 1 || rows[0]["name"] != "stg" {
		t.Fatalf("stdout = %q (err %v)", out, err)
	}
	if metas, _ := config.ListCmdLogs(sess.projectDir); len(metas) != 0 {
		t.Fatalf("link --list --json left %d records", len(metas))
	}
}
