package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

func testRSC() remoteShellContext {
	return remoteShellContext{
		sshHost:    "host",
		remotePath: "/srv/odoo",
		fromName:   "staging",
		target: connectTarget{
			remote:        true,
			composeCmd:    "docker compose",
			odooContainer: "odoo",
			dbContainer:   "db",
			dbName:        "mydb",
			stage:         "staging",
		},
		prof: config.RemoteProfile{DBName: "mydb", ComposeCmd: "docker compose", DBContainer: "db", OdooContainer: "odoo"},
		conn: odoo.Conn{DB: "mydb", User: "odoo"},
	}
}

func TestParseCheckpointArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantSub string
		check   func(t *testing.T, a checkpointArgs)
		wantErr bool
	}{
		{name: "default list", args: nil, wantSub: "list"},
		{name: "explicit list json", args: []string{"list", "--json"}, wantSub: "list",
			check: func(t *testing.T, a checkpointArgs) {
				if !a.jsonOut {
					t.Error("expected jsonOut")
				}
			}},
		{name: "create method dump", args: []string{"create", "--method", "dump"}, wantSub: "create",
			check: func(t *testing.T, a checkpointArgs) {
				if a.method != "dump" {
					t.Errorf("method = %q", a.method)
				}
			}},
		{name: "rm named", args: []string{"rm", "mydb__ckpt_x"}, wantSub: "rm",
			check: func(t *testing.T, a checkpointArgs) {
				if a.name != "mydb__ckpt_x" {
					t.Errorf("name = %q", a.name)
				}
			}},
		{name: "rm all force", args: []string{"rm", "--all", "--force"}, wantSub: "rm",
			check: func(t *testing.T, a checkpointArgs) {
				if !a.all || !a.force {
					t.Error("expected all+force")
				}
			}},
		{name: "from consumed not subcommand", args: []string{"--from", "prod"}, wantSub: "list",
			check: func(t *testing.T, a checkpointArgs) {
				if a.from != "prod" {
					t.Errorf("from = %q", a.from)
				}
			}},
		{name: "bad method", args: []string{"create", "--method", "zip"}, wantErr: true},
		{name: "bad subcommand", args: []string{"frobnicate"}, wantErr: true},
		{name: "unknown flag", args: []string{"--wat"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCheckpointArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.sub != tc.wantSub {
				t.Errorf("sub = %q, want %q", got.sub, tc.wantSub)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestParseDeployCheckpointFlags(t *testing.T) {
	t.Run("checkpoint dump sets method", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--checkpoint=dump"})
		if err != nil {
			t.Fatal(err)
		}
		if !p.checkpointSet || p.checkpoint != "dump" {
			t.Errorf("got set=%v method=%q", p.checkpointSet, p.checkpoint)
		}
	})
	t.Run("bare checkpoint sets flag, empty method", func(t *testing.T) {
		p, err := parseDeployArgs([]string{"--checkpoint"})
		if err != nil {
			t.Fatal(err)
		}
		if !p.checkpointSet || p.checkpoint != "" {
			t.Errorf("got set=%v method=%q", p.checkpointSet, p.checkpoint)
		}
	})
	t.Run("checkpoint and no-checkpoint conflict", func(t *testing.T) {
		if _, err := parseDeployArgs([]string{"--checkpoint", "--no-checkpoint"}); err == nil {
			t.Fatal("expected mutual-exclusion error")
		}
	})
	t.Run("rollback rejects selection", func(t *testing.T) {
		if _, err := parseDeployArgs([]string{"--rollback", "--auto"}); err == nil {
			t.Fatal("expected --rollback/--auto rejection")
		}
		if _, err := parseDeployArgs([]string{"--rollback", "--push"}); err == nil {
			t.Fatal("expected --rollback/--push rejection")
		}
	})
	t.Run("bad checkpoint method", func(t *testing.T) {
		if _, err := parseDeployArgs([]string{"--checkpoint=zip"}); err == nil {
			t.Fatal("expected invalid method error")
		}
	})
}

func TestResolveCheckpointMode(t *testing.T) {
	cfg := func(mode, method string) *config.Config {
		return &config.Config{CheckpointMode: mode, CheckpointMethod: method, CheckpointKeep: 2}
	}
	cases := []struct {
		name        string
		p           deployArgs
		cfg         *config.Config
		stage       string
		wantEnabled bool
		wantMethod  string
	}{
		{"auto staging on", deployArgs{}, cfg("auto", "db"), "staging", true, "db"},
		{"auto prod on", deployArgs{}, cfg("auto", "db"), "prod", true, "db"},
		{"auto dev off", deployArgs{}, cfg("auto", "db"), "dev", false, "db"},
		{"config on wins over dev", deployArgs{}, cfg("on", "db"), "dev", true, "db"},
		{"config off wins over prod", deployArgs{}, cfg("off", "db"), "prod", false, "db"},
		{"flag on overrides dev", deployArgs{checkpointSet: true}, cfg("auto", "db"), "dev", true, "db"},
		{"flag off overrides prod", deployArgs{noCheckpoint: true}, cfg("on", "db"), "prod", false, "db"},
		{"flag method overrides config", deployArgs{checkpointSet: true, checkpoint: "dump"}, cfg("auto", "db"), "staging", true, "dump"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, method := resolveCheckpointMode(tc.p, tc.cfg, tc.stage)
			if enabled != tc.wantEnabled || method != tc.wantMethod {
				t.Errorf("got (%v,%q), want (%v,%q)", enabled, method, tc.wantEnabled, tc.wantMethod)
			}
		})
	}
}

func TestRunFailureScanner(t *testing.T) {
	cases := []struct {
		name string
		line string
		hit  bool
	}{
		{"critical", "2026-07-10 10:00:00,000 1 CRITICAL mydb odoo: boom", true},
		{"traceback", "Traceback (most recent call last):", true},
		{"registry", "Failed to load registry", true},
		{"info clean", "2026-07-10 10:00:00,000 1 INFO mydb odoo: loading", false},
		{"warning clean", "2026-07-10 10:00:00,000 1 WARNING mydb odoo: deprecated", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &runFailureScanner{}
			s.scan(tc.line)
			if (s.hits > 0) != tc.hit {
				t.Errorf("hits=%d, want hit=%v", s.hits, tc.hit)
			}
		})
	}
	t.Run("forwards to inner", func(t *testing.T) {
		var got []string
		s := &runFailureScanner{inner: func(l string) { got = append(got, l) }}
		s.scan("hello")
		if len(got) != 1 || got[0] != "hello" {
			t.Errorf("inner got %v", got)
		}
	})
}

func TestCkptDBName(t *testing.T) {
	short := ckptDBName("mydb", "20260710_100000")
	if !strings.HasPrefix(short, "mydb__ckpt_") {
		t.Errorf("unexpected: %q", short)
	}
	long := ckptDBName(strings.Repeat("x", 80), "20260710_100000")
	if len(long) > 63 {
		t.Errorf("name exceeds 63 bytes: %d (%q)", len(long), long)
	}
	if !strings.Contains(long, "__ckpt_") {
		t.Errorf("truncated name lost suffix: %q", long)
	}
}

func TestHumanAge(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:             "30s",
		5 * time.Minute:              "5m",
		2 * time.Hour:                "2h",
		2*time.Hour + 20*time.Minute: "2h20m",
		50 * time.Hour:               "2d",
	}
	for d, want := range cases {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestCreateCheckpointDBMethod(t *testing.T) {
	var calls []string
	orig := ckptRunSSH
	defer func() { ckptRunSSH = orig }()
	ckptRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		calls = append(calls, remoteCmd)
		switch {
		case strings.Contains(remoteCmd, "pg_database_size"):
			return []byte("2048\n"), nil
		case strings.Contains(remoteCmd, "server_version_num"):
			return []byte("150004\n"), nil
		}
		return []byte(""), nil
	}

	entry, info, err := createCheckpoint(context.Background(), testRSC(), "db", []string{"deadbeef"}, nil, nil)
	if err != nil {
		t.Fatalf("createCheckpoint: %v", err)
	}
	if entry.Method != "db" || info.Method != "db" {
		t.Errorf("method = %q/%q", entry.Method, info.Method)
	}
	if !strings.Contains(entry.Name, "__ckpt_") {
		t.Errorf("name = %q", entry.Name)
	}

	iTerm := indexOfCall(calls, "pg_terminate_backend")
	iCreate := indexOfCall(calls, "CREATE DATABASE")
	if iTerm < 0 || iCreate < 0 {
		t.Fatalf("missing terminate/create calls: %v", calls)
	}
	if iTerm > iCreate {
		t.Errorf("terminate must precede create (term=%d create=%d)", iTerm, iCreate)
	}
	if !strings.Contains(calls[iCreate], "STRATEGY FILE_COPY") {
		t.Errorf("PG15 create should use STRATEGY FILE_COPY: %q", calls[iCreate])
	}
	if !strings.Contains(calls[iCreate], "TEMPLATE \"mydb\"") {
		t.Errorf("create should template from mydb: %q", calls[iCreate])
	}
}

func TestCreateCheckpointDBMethodOldPGNoStrategy(t *testing.T) {
	var calls []string
	orig := ckptRunSSH
	defer func() { ckptRunSSH = orig }()
	ckptRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		calls = append(calls, remoteCmd)
		if strings.Contains(remoteCmd, "server_version_num") {
			return []byte("140006\n"), nil
		}
		return []byte("0\n"), nil
	}
	if _, _, err := createCheckpoint(context.Background(), testRSC(), "db", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	iCreate := indexOfCall(calls, "CREATE DATABASE")
	if iCreate < 0 {
		t.Fatal("no create call")
	}
	if strings.Contains(calls[iCreate], "STRATEGY FILE_COPY") {
		t.Errorf("PG14 create must not use STRATEGY FILE_COPY: %q", calls[iCreate])
	}
}

func TestCreateCheckpointDumpMethod(t *testing.T) {
	var streamCmds []string
	origStream := ckptRunSSHStream
	origSSH := ckptRunSSH
	defer func() { ckptRunSSHStream = origStream; ckptRunSSH = origSSH }()
	ckptRunSSHStream = func(ctx context.Context, host, remoteCmd string, stdin []byte, onLine func(string)) error {
		streamCmds = append(streamCmds, remoteCmd)
		return nil
	}
	ckptRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		return []byte("4096\n"), nil // file size
	}

	entry, info, err := createCheckpoint(context.Background(), testRSC(), "dump", nil, nil, nil)
	if err != nil {
		t.Fatalf("createCheckpoint dump: %v", err)
	}
	if entry.Method != "dump" || info.Method != "dump" {
		t.Errorf("method = %q/%q", entry.Method, info.Method)
	}
	if !strings.HasPrefix(entry.DumpPath, checkpointDir+"/") {
		t.Errorf("dump path = %q", entry.DumpPath)
	}
	if len(streamCmds) != 1 || !strings.Contains(streamCmds[0], "pg_dump -Fc") {
		t.Fatalf("expected one pg_dump stream, got %v", streamCmds)
	}
	if !strings.Contains(streamCmds[0], "> "+shellQuote(entry.DumpPath)) {
		t.Errorf("dump should redirect to the file: %q", streamCmds[0])
	}
}

func TestRestoreCheckpointDBMethodOrder(t *testing.T) {
	var calls []string
	orig := ckptRunSSH
	defer func() { ckptRunSSH = orig }()
	ckptRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		calls = append(calls, remoteCmd)
		return []byte(""), nil
	}
	entry := config.CheckpointEntry{Name: "mydb__ckpt_x", Method: "db", DB: "mydb"}
	consumed, err := restoreCheckpoint(context.Background(), testRSC(), entry, nil, nil)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !consumed {
		t.Error("db-method restore must consume the checkpoint")
	}
	iDrop := indexOfCall(calls, `DROP DATABASE IF EXISTS "mydb"`)
	iRename := indexOfCall(calls, "RENAME TO")
	if iDrop < 0 || iRename < 0 {
		t.Fatalf("missing drop/rename: %v", calls)
	}
	if iDrop > iRename {
		t.Errorf("drop must precede rename (drop=%d rename=%d)", iDrop, iRename)
	}
}

func TestRestoreCheckpointDumpMethod(t *testing.T) {
	var calls, streamCmds []string
	origSSH := ckptRunSSH
	origStream := ckptRunSSHStream
	defer func() { ckptRunSSH = origSSH; ckptRunSSHStream = origStream }()
	ckptRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		calls = append(calls, remoteCmd)
		return []byte(""), nil
	}
	ckptRunSSHStream = func(ctx context.Context, host, remoteCmd string, stdin []byte, onLine func(string)) error {
		streamCmds = append(streamCmds, remoteCmd)
		return nil
	}
	entry := config.CheckpointEntry{Name: "d.dump", Method: "dump", DB: "mydb", DumpPath: "backups/checkpoints/d.dump"}
	consumed, err := restoreCheckpoint(context.Background(), testRSC(), entry, nil, nil)
	if err != nil {
		t.Fatalf("restore dump: %v", err)
	}
	if consumed {
		t.Error("dump-method restore must NOT consume the checkpoint")
	}
	if indexOfCall(calls, `CREATE DATABASE "mydb"`) < 0 {
		t.Errorf("dump restore should recreate the DB: %v", calls)
	}
	if len(streamCmds) != 1 || !strings.Contains(streamCmds[0], "pg_restore") {
		t.Errorf("expected pg_restore stream, got %v", streamCmds)
	}
}

func indexOfCall(calls []string, substr string) int {
	for i, c := range calls {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}
