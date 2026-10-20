package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/docker"
)

func TestRunModstateNoDB(t *testing.T) {
	// Missing db_container/db_name is a configuration error, returned before
	// any subprocess runs.
	_, err := RunModstate(context.Background(), ModstateOpts{
		Cfg:  &config.Config{},
		Root: t.TempDir(),
		Args: []string{"--json"},
	})
	if !errors.Is(err, ErrNoDB) {
		t.Fatalf("err = %v, want ErrNoDB", err)
	}
}

func TestRunModstateBadArgs(t *testing.T) {
	cfg := &config.Config{DBName: "db", DBContainer: "pg"}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--nope"}, "unknown flag"},
		{"from without target", []string{"--from"}, "--from needs a target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RunModstate(context.Background(), ModstateOpts{
				Cfg:  cfg,
				Root: t.TempDir(),
				Args: tc.args,
			})
			if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrUsage containing %q", err, tc.want)
			}
		})
	}
}

func TestParseModstateArgs(t *testing.T) {
	p, err := parseModstateArgs([]string{"sale", "--from", "stg", "stock", "--all", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	want := modstateArgs{modules: []string{"sale", "stock"}, from: "stg", all: true, json: true}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("parsed %+v, want %+v", p, want)
	}
	p, err = parseModstateArgs([]string{"--from=stg", "--remote"})
	if err != nil || p.from != "stg" || !p.remote || p.modules != nil {
		t.Fatalf("parsed %+v (%v), want from=stg remote, no modules", p, err)
	}
}

func TestFilterModuleRows(t *testing.T) {
	rows := []docker.ModuleStateRow{
		{Name: "sale", State: "installed", Version: "18.0.1.0"},
		{Name: "stock", State: "installed", Version: "18.0.1.0"},
	}
	if got := filterModuleRows(rows, nil, false); !reflect.DeepEqual(got, rows) {
		t.Errorf("no names = %v, want every row", got)
	}
	if got := filterModuleRows(rows, []string{"stock", "ghost"}, false); len(got) != 1 || got[0].Name != "stock" {
		t.Errorf("installed-only filter = %v, want stock alone", got)
	}
	got := filterModuleRows(rows, []string{"stock", "ghost"}, true)
	want := []docker.ModuleStateRow{
		{Name: "ghost", State: "uninstalled", VersionNull: true},
		{Name: "stock", State: "installed", Version: "18.0.1.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("--all filter = %v, want %v", got, want)
	}
}

func TestRunModstateRemote(t *testing.T) {
	remote := newFakeRemote(t)
	t.Setenv("FAKE_MODULE_STATES", "account|uninstalled|")
	run := func(args ...string) ModstateResult {
		t.Helper()
		res, err := RunModstate(context.Background(), ModstateOpts{Cfg: remote.cfg(), Root: t.TempDir(), Args: args})
		if err != nil {
			t.Fatalf("modstate %v: %v", args, err)
		}
		return res
	}

	res := run("--from", "stg")
	if res.DB != "stg" {
		t.Errorf("db = %q, want the remote profile's stg", res.DB)
	}
	want := []docker.ModuleStateRow{
		{Name: "sale", State: "installed", Version: "18.0.1.0"},
		{Name: "stock", State: "installed", Version: "18.0.1.0"},
	}
	if !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("installed rows = %v, want %v", res.Rows, want)
	}
	if !strings.Contains(remote.sshLog(), "ir_module_module") {
		t.Errorf("the module query never went over ssh:\n%s", remote.sshLog())
	}

	res = run("account", "sale", "--all", "--from", "stg")
	want = []docker.ModuleStateRow{
		{Name: "account", State: "uninstalled", VersionNull: true},
		{Name: "sale", State: "installed", Version: "18.0.1.0"},
	}
	if !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("filtered --all rows = %v, want %v", res.Rows, want)
	}
}
