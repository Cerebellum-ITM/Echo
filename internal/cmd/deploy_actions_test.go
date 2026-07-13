package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestResolveDeployActions(t *testing.T) {
	server := []config.DeployAction{{Name: "build", Phase: "post_push", Where: "remote", Run: "./b.sh"}}
	local := []config.DeployAction{{Name: "notify", Phase: "post_deploy", Where: "local", Run: "echo hi"}}

	t.Run("no-actions skips all", func(t *testing.T) {
		got, src, err := resolveDeployActions(config.RemoteProfile{DeployActions: server}, &config.Config{DeployActions: local}, true)
		if err != nil || got != nil || src != "" {
			t.Fatalf("got (%v, %q, %v)", got, src, err)
		}
	})
	t.Run("server wins wholesale", func(t *testing.T) {
		got, src, err := resolveDeployActions(config.RemoteProfile{DeployActions: server}, &config.Config{DeployActions: local}, false)
		if err != nil || src != "server" || !reflect.DeepEqual(got, server) {
			t.Fatalf("got (%v, %q, %v), want server list", got, src, err)
		}
	})
	t.Run("local fallback", func(t *testing.T) {
		got, src, err := resolveDeployActions(config.RemoteProfile{}, &config.Config{DeployActions: local}, false)
		if err != nil || src != "local" || !reflect.DeepEqual(got, local) {
			t.Fatalf("got (%v, %q, %v), want local list", got, src, err)
		}
	})
	t.Run("none", func(t *testing.T) {
		got, src, err := resolveDeployActions(config.RemoteProfile{}, &config.Config{}, false)
		if err != nil || got != nil || src != "" {
			t.Fatalf("got (%v, %q, %v)", got, src, err)
		}
	})
	t.Run("invalid config errors", func(t *testing.T) {
		bad := []config.DeployAction{{Name: "x", Phase: "bogus", Where: "remote", Run: "y"}}
		if _, _, err := resolveDeployActions(config.RemoteProfile{DeployActions: bad}, nil, false); err == nil {
			t.Fatal("want validation error, got nil")
		}
	})
}

func TestActionsForPhase(t *testing.T) {
	actions := []config.DeployAction{
		{Name: "a", Phase: "post_push"},
		{Name: "b", Phase: "pre_deploy"},
		{Name: "c", Phase: "post_push"},
	}
	got := actionsForPhase(actions, "post_push")
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Errorf("actionsForPhase(post_push) = %+v, want [a c] in order", got)
	}
	if len(actionsForPhase(actions, "post_deploy")) != 0 {
		t.Error("post_deploy should have no actions")
	}
}

func TestRunDeployActionsFailFast(t *testing.T) {
	origLocal, origRemote := actionRunLocal, actionRunRemote
	defer func() { actionRunLocal, actionRunRemote = origLocal, origRemote }()

	var ran []string
	actionRunLocal = func(_ context.Context, _ string, a config.DeployAction, _ []string, _ func(string)) error {
		ran = append(ran, a.Name)
		if a.Name == "boom" {
			return errors.New("exit 1")
		}
		return nil
	}
	actionRunRemote = func(_ context.Context, _ remoteShellContext, a config.DeployAction, _ []string, _ func(string)) error {
		ran = append(ran, a.Name)
		return nil
	}

	actions := []config.DeployAction{
		{Name: "first", Phase: "post_push", Where: "local", Run: "x"},
		{Name: "boom", Phase: "post_push", Where: "local", Run: "y"},
		{Name: "never", Phase: "post_push", Where: "local", Run: "z"},
	}
	opts := DeployOpts{}
	err := runDeployActions(context.Background(), remoteShellContext{}, opts, actions, "post_push", actionEnv{})
	if err == nil {
		t.Fatal("want error from failing action")
	}
	var ae *deployActionError
	if !errors.As(err, &ae) || ae.name != "boom" {
		t.Fatalf("err = %v, want deployActionError for boom", err)
	}
	if !reflect.DeepEqual(ran, []string{"first", "boom"}) {
		t.Errorf("ran = %v, want [first boom] (fail-fast, never skipped)", ran)
	}
}

func TestActionEnvVars(t *testing.T) {
	env := actionEnv{stage: "prod", db: "erp", remotePath: "/srv/odoo", modules: "sale account"}
	got := actionEnvVars(env, "post_push")
	want := []string{
		"ECHO_STAGE=prod",
		"ECHO_DB=erp",
		"ECHO_REMOTE_PATH=/srv/odoo",
		"ECHO_MODULES=sale account",
		"ECHO_PHASE=post_push",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("actionEnvVars = %v, want %v", got, want)
	}
}

func TestEnvPrefix(t *testing.T) {
	got := envPrefix([]string{"ECHO_STAGE=prod", "ECHO_MODULES=sale account"})
	// Values must be quoted so a space in ECHO_MODULES doesn't split the command.
	if !strings.Contains(got, "ECHO_STAGE=") || !strings.Contains(got, "sale account") {
		t.Errorf("envPrefix = %q", got)
	}
	if strings.TrimSpace(got) == "" {
		t.Error("non-empty env should produce a prefix")
	}
	if envPrefix(nil) != "" {
		t.Error("nil env should produce empty prefix")
	}
}

func TestValidateDeployActions(t *testing.T) {
	ok := []config.DeployAction{{Name: "a", Phase: "pre_push", Where: "local", Run: "x"}}
	if err := config.ValidateDeployActions(ok); err != nil {
		t.Errorf("valid actions errored: %v", err)
	}
	cases := [][]config.DeployAction{
		{{Name: "", Phase: "pre_push", Where: "local", Run: "x"}},          // empty name
		{{Name: "a", Phase: "nope", Where: "local", Run: "x"}},             // bad phase
		{{Name: "a", Phase: "pre_push", Where: "nope", Run: "x"}},          // bad where
		{{Name: "a", Phase: "pre_push", Where: "local", Run: ""}},          // empty run
		{{Name: "a", Phase: "pre_push", Where: "local", Run: "x"}, {Name: "a", Phase: "post_push", Where: "local", Run: "y"}}, // dup name
	}
	for i, c := range cases {
		if err := config.ValidateDeployActions(c); err == nil {
			t.Errorf("case %d: want error, got nil", i)
		}
	}
}
