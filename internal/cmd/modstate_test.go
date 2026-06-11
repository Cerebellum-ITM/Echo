package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
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
		{"positional", []string{"sale"}, "takes no arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RunModstate(context.Background(), ModstateOpts{
				Cfg:  cfg,
				Root: t.TempDir(),
				Args: tc.args,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
