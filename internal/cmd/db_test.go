package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/odoo"
)

func TestParseDBArgsNeutralize(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		wantNeutralize bool
		wantForce      bool
		wantAs         string
		wantPos        []string
	}{
		{"bare", []string{"--neutralize"}, true, false, "", nil},
		{"with-positional", []string{"mydb", "--neutralize"}, true, false, "", []string{"mydb"}},
		{"mixed", []string{"--neutralize", "--force", "--as", "copy"}, true, true, "copy", nil},
		{"absent", []string{"--force"}, false, true, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, pos := parseDBArgs(c.args)
			if f.neutralize != c.wantNeutralize {
				t.Errorf("neutralize = %v, want %v", f.neutralize, c.wantNeutralize)
			}
			if f.force != c.wantForce {
				t.Errorf("force = %v, want %v", f.force, c.wantForce)
			}
			if f.asName != c.wantAs {
				t.Errorf("asName = %q, want %q", f.asName, c.wantAs)
			}
			if strings.Join(pos, ",") != strings.Join(c.wantPos, ",") {
				t.Errorf("positional = %v, want %v", pos, c.wantPos)
			}
		})
	}
}

func TestNeutralizeBuilder(t *testing.T) {
	got := odoo.Neutralize(odoo.Conn{DB: "mydb", Host: "db", Port: "5432", User: "odoo"})
	want := []string{"odoo", "neutralize", "-d", "mydb", "--db_host", "db", "--db_port", "5432", "--db_user", "odoo"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Neutralize() = %v, want %v", got, want)
	}
}

func TestRestoreLineLogger(t *testing.T) {
	type event struct{ level, step, msg, db string }
	var got []event
	opts := DBOpts{Log: func(level, step, msg, db string, fields ...[2]string) {
		got = append(got, event{level, step, msg, db})
	}}
	fn := restoreLineLogger(opts, "mydb")

	fn("pg_restore: creating TABLE \"public\".\"res_users\"")
	fn("   ")                  // whitespace only → dropped
	fn("")                     // empty → dropped
	fn("pg_restore: processing data for table \"public\".\"ir_attachment\"")
	fn("plain line without prefix")

	want := []event{
		{"DEBUG", "restore", `creating TABLE "public"."res_users"`, "mydb"},
		{"DEBUG", "restore", `processing data for table "public"."ir_attachment"`, "mydb"},
		{"DEBUG", "restore", "plain line without prefix", "mydb"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("event %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// restoreLineLogger with a nil Log must not panic (commands run silent).
func TestRestoreLineLoggerNilLog(t *testing.T) {
	fn := restoreLineLogger(DBOpts{}, "mydb")
	fn("pg_restore: creating TABLE x") // should be a no-op, no panic
}

func TestValidateDBName(t *testing.T) {
	valid := []string{"mydb", "my_db_2", "habitta_prod", "a"}
	for _, s := range valid {
		if err := validateDBName(s); err != nil {
			t.Errorf("validateDBName(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{"", "   ", "has space", "tab\tname", "line\nbreak"}
	for _, s := range invalid {
		if err := validateDBName(s); err == nil {
			t.Errorf("validateDBName(%q) = nil, want error", s)
		}
	}
}

func TestDBNameFromBackup(t *testing.T) {
	cases := []struct {
		name, want string
	}{
		// Odoo database-manager download: <db>_YYYY-MM-DD_HH-MM-SS.zip
		{"habitta_prod_2026-06-08_23-42-53.zip", "habitta_prod"},
		{"mydb_2026-01-02_03-04-05.zip", "mydb"},
		// Echo's own backup: <db>_YYYYMMDD-HHMMSS.{dump,zip}
		{"habitta_prod_20260608-234253.dump", "habitta_prod"},
		{"odoo_20260101-120000.zip", "odoo"},
		// Underscores in the db name must survive.
		{"a_b_c_2026-06-08_23-42-53.zip", "a_b_c"},
		// No recognizable timestamp → basename without extension.
		{"plain.dump", "plain"},
		{"weird_name.zip", "weird_name"},
	}
	for _, c := range cases {
		if got := dbNameFromBackup(c.name); got != c.want {
			t.Errorf("dbNameFromBackup(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIsHexPrefix(t *testing.T) {
	valid := []string{"99", "ab", "0d", "ff", "7f"}
	for _, s := range valid {
		if !isHexPrefix(s) {
			t.Errorf("isHexPrefix(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "a", "abc", "AB", "g0", "habitta_prod", "z9"}
	for _, s := range invalid {
		if isHexPrefix(s) {
			t.Errorf("isHexPrefix(%q) = true, want false", s)
		}
	}
}

func TestParseDBArgsAdminFlags(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantPassword string
		wantInsecure bool
		wantPos      []string
	}{
		{"separate-value", []string{"--password", "s3cr3t"}, "s3cr3t", false, nil},
		{"equals-value", []string{"--password=s3cr3t"}, "s3cr3t", false, nil},
		{"value-with-spaces", []string{"--password", "foo bar"}, "foo bar", false, nil},
		{"insecure", []string{"mydb", "--insecure"}, "", true, []string{"mydb"}},
		{"conflict", []string{"--password", "x", "--insecure"}, "x", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, pos := parseDBArgs(c.args)
			if f.password != c.wantPassword {
				t.Errorf("password = %q, want %q", f.password, c.wantPassword)
			}
			if f.insecure != c.wantInsecure {
				t.Errorf("insecure = %v, want %v", f.insecure, c.wantInsecure)
			}
			if strings.Join(pos, ",") != strings.Join(c.wantPos, ",") {
				t.Errorf("positional = %v, want %v", pos, c.wantPos)
			}
		})
	}
}

func TestGenerateAdminPassword(t *testing.T) {
	first, err := generateAdminPassword()
	if err != nil {
		t.Fatalf("generateAdminPassword: %v", err)
	}
	if len(first) != adminPasswordLen {
		t.Errorf("length = %d, want %d", len(first), adminPasswordLen)
	}
	if strings.ContainsAny(first, "il1O0") {
		t.Errorf("password %q contains ambiguous glyphs", first)
	}
	second, err := generateAdminPassword()
	if err != nil {
		t.Fatalf("generateAdminPassword: %v", err)
	}
	if first == second {
		t.Error("two generated passwords are identical")
	}
}

func TestParseDBArgsRemoteSwitches(t *testing.T) {
	// The value of --from must not survive as a positional: that would be
	// read as the database name and reset the admin on the wrong DB.
	f, pos := parseDBArgs([]string{"--from", "muutrade", "--force"})
	if f.from != "muutrade" {
		t.Errorf("from = %q, want %q", f.from, "muutrade")
	}
	if len(pos) != 0 {
		t.Errorf("positional = %v, want none", pos)
	}
	f, pos = parseDBArgs([]string{"mydb", "--remote"})
	if !f.remote {
		t.Error("remote = false, want true")
	}
	if strings.Join(pos, ",") != "mydb" {
		t.Errorf("positional = %v, want [mydb]", pos)
	}
}

func TestResolveAdminCredential(t *testing.T) {
	forced := dbFlags{force: true}

	password, hash, err := resolveAdminCredential(DBOpts{}, forced, "mydb", "dev")
	if err != nil {
		t.Fatalf("resolveAdminCredential: %v", err)
	}
	if !strings.HasPrefix(hash, "$pbkdf2-sha512$") {
		t.Errorf("hash = %q, want a pbkdf2_sha512 hash", hash)
	}
	if strings.Contains(hash, password) {
		t.Error("the hash carries the plaintext password")
	}

	password, _, err = resolveAdminCredential(DBOpts{}, dbFlags{force: true, password: "s3cr3t"}, "mydb", "dev")
	if err != nil {
		t.Fatalf("resolveAdminCredential: %v", err)
	}
	if password != "s3cr3t" {
		t.Errorf("password = %q, want the explicit one", password)
	}

	password, _, err = resolveAdminCredential(DBOpts{}, dbFlags{force: true, insecure: true}, "mydb", "dev")
	if err != nil {
		t.Fatalf("resolveAdminCredential: %v", err)
	}
	if password != insecureAdminPassword {
		t.Errorf("password = %q, want %q", password, insecureAdminPassword)
	}
}

// Without --force the guard must fire on prod and on a known credential,
// and stay quiet otherwise. Tests run without a TTY, so a fired guard
// surfaces as ErrNonInteractive — which is exactly the signal we want.
func TestResolveAdminCredentialGuard(t *testing.T) {
	cases := []struct {
		name      string
		flags     dbFlags
		stage     string
		wantGuard bool
	}{
		{"prod", dbFlags{}, "prod", true},
		{"insecure-on-dev", dbFlags{insecure: true}, "dev", true},
		{"generated-on-dev", dbFlags{}, "dev", false},
		{"forced-prod", dbFlags{force: true}, "prod", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := resolveAdminCredential(DBOpts{}, c.flags, "mydb", c.stage)
			if got := errors.Is(err, ErrNonInteractive); got != c.wantGuard {
				t.Errorf("guard fired = %v, want %v (err = %v)", got, c.wantGuard, err)
			}
		})
	}
}

func TestParseDBArgsSaveFlags(t *testing.T) {
	f, pos := parseDBArgs([]string{"--save", "--vault", "Private"})
	if !f.save {
		t.Error("save = false, want true")
	}
	if f.vault != "Private" {
		t.Errorf("vault = %q, want Private", f.vault)
	}
	if len(pos) != 0 {
		t.Errorf("positional = %v, want none", pos)
	}
	if f, _ = parseDBArgs([]string{"--vault=Shared"}); f.vault != "Shared" {
		t.Errorf("vault = %q, want Shared", f.vault)
	}
}

// --vault names the destination of a save nobody asked for, and it must
// fail before the reset rather than after it.
func TestRunDBAdminRejectsVaultWithoutSave(t *testing.T) {
	err := RunDBAdmin(context.Background(), DBOpts{
		Cfg:  &config.Config{DBContainer: "db", DBName: "mydb"},
		Args: []string{"--vault", "Private", "--force"},
	})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage", err)
	}
}
