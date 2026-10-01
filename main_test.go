package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectlessOneShot(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
		want bool
	}{
		// Purely informational / remote-only commands: no compose project.
		{"help needs no project", "help", nil, true},
		{"i18n-pull always projectless", "i18n-pull", nil, true},
		{"deploy always projectless", "deploy", nil, true},
		// db-pull downloads a remote dump into ./backups/; --restore self-guards
		// downstream, so the classification is unconditional either way.
		{"db-pull download-only projectless", "db-pull", nil, true},
		{"db-pull --restore still projectless", "db-pull", []string{"--restore"}, true},

		// Remote-mode group: projectless only with a remote selector.
		{"update --remote", "update", []string{"sale", "--remote"}, true},
		{"update --from target", "update", []string{"--from", "prod"}, true},
		{"update --from=target", "update", []string{"--from=prod", "sale"}, true},
		{"update local needs a project", "update", []string{"sale"}, false},
		{"test --remote", "test", []string{"--remote"}, true},
		{"test local needs a project", "test", []string{"sale"}, false},
		{"db-admin --remote", "db-admin", []string{"--remote"}, true},
		{"db-admin --from target", "db-admin", []string{"--from", "prod"}, true},
		{"db-admin local needs a project", "db-admin", nil, false},
		// logview/report read the local history store keyed by cwd — always
		// projectless (remote flag switches the source, not the requirement).
		{"logview --remote", "logview", []string{"--remote"}, true},
		{"logview --from target", "logview", []string{"--from", "prod"}, true},
		{"logview local is projectless", "logview", nil, true},
		{"report local is projectless", "report", nil, true},

		// Local-only commands never qualify.
		{"install never projectless", "install", []string{"--remote"}, false},
		{"ps local", "ps", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectlessOneShot(tc.cmd, tc.args); got != tc.want {
				t.Errorf("projectlessOneShot(%q, %v) = %v, want %v", tc.cmd, tc.args, got, tc.want)
			}
		})
	}
}

func TestHasRemoteFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--remote"}, true},
		{[]string{"--from", "prod"}, true},
		{[]string{"--from=prod"}, true},
		{[]string{"sale", "account"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := hasRemoteFlag(c.args); got != c.want {
			t.Errorf("hasRemoteFlag(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestConfigParseErrorExits2 re-runs the test binary as echo_cli: with
// ECHO_MAIN_ARGS set, the child calls main() with those arguments.
func TestConfigParseErrorExits2(t *testing.T) {
	if args, ok := os.LookupEnv("ECHO_MAIN_ARGS"); ok {
		os.Args = append([]string{"echo_cli"}, strings.Fields(args)...)
		main()
		return
	}
	home := t.TempDir()
	global := filepath.Join(home, ".config/echo/global.toml")
	const broken = "theme = \"tokyo\"\nlogo = = \"echo\"\n"
	if err := os.MkdirAll(filepath.Dir(global), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range []string{"-C " + project + " ps", "-C " + project} {
		child := exec.Command(os.Args[0], "-test.run=^TestConfigParseErrorExits2$")
		child.Env = append(os.Environ(), "HOME="+home, "ECHO_MAIN_ARGS="+args)
		out, err := child.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("echo_cli %s: err = %v, want exit 2\n%s", args, err, out)
		}
		if !strings.Contains(string(out), "echo.config:") || !strings.Contains(string(out), "global.toml: line 2") {
			t.Errorf("echo_cli %s: output lacks the config error line:\n%s", args, out)
		}
		got, _ := os.ReadFile(global)
		if string(got) != broken {
			t.Fatalf("echo_cli %s rewrote global.toml:\n%s", args, got)
		}
	}
}
