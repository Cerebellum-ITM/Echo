package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/reverb"
)

// -E must normalize into the `env:` target reference so every command that
// already threads a `from` string reaches Reverb mode unchanged.
func TestRemoteFlagsInEnv(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantFrom string
		wantRem  bool
	}{
		{"E space", []string{"-E", "acme/main"}, "env:acme/main", false},
		{"E equals", []string{"-E=acme/main"}, "env:acme/main", false},
		{"env space", []string{"--env", "main"}, "env:main", false},
		{"env equals", []string{"--env=acme/main"}, "env:acme/main", false},
		{"with modules", []string{"my_mod", "-E", "acme/main"}, "env:acme/main", false},
		{"classic from untouched", []string{"--from", "muutrade"}, "muutrade", false},
		{"classic remote untouched", []string{"--remote"}, "", true},
		{"no target", []string{"my_mod"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, remote := remoteFlagsIn(tc.in)
			if from != tc.wantFrom || remote != tc.wantRem {
				t.Errorf("remoteFlagsIn(%v) = (%q, %v), want (%q, %v)",
					tc.in, from, remote, tc.wantFrom, tc.wantRem)
			}
		})
	}
}

// A module named like the flag's value must not be swallowed: -E consumes
// exactly one token.
func TestRemoteFlagsInEnvConsumesOneToken(t *testing.T) {
	from, _ := remoteFlagsIn([]string{"-E", "acme/main", "my_mod"})
	if from != "env:acme/main" {
		t.Fatalf("from = %q", from)
	}
}

func TestReverbRefIn(t *testing.T) {
	if spec, ok := reverbRefIn("env:acme/main"); !ok || spec != "acme/main" {
		t.Errorf("reverbRefIn(env:acme/main) = (%q, %v)", spec, ok)
	}
	if _, ok := reverbRefIn("muutrade"); ok {
		t.Error("a plain connect-target name must not read as a Reverb reference")
	}
	if _, ok := reverbRefIn(""); ok {
		t.Error("an empty reference must not read as a Reverb reference")
	}
}

func TestParseReverbSpec(t *testing.T) {
	cases := []struct {
		in                string
		wantProj, wantEnv string
		wantErr           bool
	}{
		{"acme/main", "acme", "main", false},
		{"main", "", "main", false},
		{"/acme/main/", "acme", "main", false},
		{"", "", "", true},
		{"   ", "", "", true},
		{"a/b/c", "", "", true},
	}
	for _, tc := range cases {
		proj, env, err := parseReverbSpec(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseReverbSpec(%q) = (%q,%q,nil), want an error", tc.in, proj, env)
			} else if !errors.Is(err, ErrUsage) {
				t.Errorf("parseReverbSpec(%q) err = %v, want ErrUsage", tc.in, err)
			}
			continue
		}
		if err != nil || proj != tc.wantProj || env != tc.wantEnv {
			t.Errorf("parseReverbSpec(%q) = (%q,%q,%v), want (%q,%q,nil)",
				tc.in, proj, env, err, tc.wantProj, tc.wantEnv)
		}
	}
}

// Using -E with no [reverb] section must name the section, not fail with a
// transport error.
func TestReverbClientNotConfigured(t *testing.T) {
	_, err := reverbClient(&config.Config{})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "[reverb]") {
		t.Errorf("err %q does not name the [reverb] section", err)
	}
}

func TestReverbComposeCmd(t *testing.T) {
	if got := reverbComposeCmd(&config.Config{}); got != "docker compose" {
		t.Errorf("default compose cmd = %q", got)
	}
	if got := reverbComposeCmd(&config.Config{ReverbComposeCmd: "docker-compose"}); got != "docker-compose" {
		t.Errorf("override compose cmd = %q", got)
	}
}

// The deferred commands must refuse a Reverb target with a usage error
// naming why — and must not touch a classic target.
func TestRequireNoReverb(t *testing.T) {
	for _, name := range []string{"deploy", "watch", "checkpoint", "up", "down", "stop", "restart"} {
		err := requireNoReverb(name, "env:acme/main")
		if !errors.Is(err, ErrUsage) {
			t.Errorf("%s: err = %v, want ErrUsage", name, err)
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err %q does not name the command", name, err)
		}
	}
	for _, from := range []string{"", "muutrade"} {
		if err := requireNoReverb("deploy", from); err != nil {
			t.Errorf("classic target %q must pass, got %v", from, err)
		}
	}
}

func TestReverbErrorExplainsConfigProblems(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{reverb.ErrUnauthorized, "token"},
		{reverb.ErrForbidden, "echo"},
		{reverb.ErrNotReady, "provisioning"},
	}
	for _, tc := range cases {
		got := reverbError(tc.err)
		if !strings.Contains(got.Error(), tc.want) {
			t.Errorf("reverbError(%v) = %q, want it to mention %q", tc.err, got, tc.want)
		}
		if !errors.Is(got, tc.err) {
			t.Errorf("reverbError(%v) drops the sentinel", tc.err)
		}
	}
	// Anything else passes through untouched.
	other := errors.New("boom")
	if got := reverbError(other); got != other {
		t.Errorf("reverbError(other) = %v, want it unchanged", got)
	}
}

func TestReverbEnvRef(t *testing.T) {
	r := &reverbEnv{project: "acme", env: "main"}
	if got := r.ref(); got != "env:acme/main" {
		t.Errorf("ref() = %q", got)
	}
}

// --- push destination ------------------------------------------------

// reverbCtx is a resolved Reverb target with the three contract paths.
func reverbCtx() remoteShellContext {
	return remoteShellContext{
		sshHost:    "deploy@host",
		remotePath: "/data/acme/envs/main",
		fromName:   "env:acme/main",
		reverb: &reverbEnv{
			project: "acme", env: "main",
			paths: reverb.Paths{
				Addons:     "/data/acme/envs/main/addons",
				Overlay:    "/data/acme/envs/main/overlay",
				ComposeDir: "/data/acme/envs/main",
			},
		},
	}
}

func TestIsUnderAddons(t *testing.T) {
	addons := "/data/acme/envs/main/addons"
	cases := []struct {
		dest string
		want bool
	}{
		{"/data/acme/envs/main/addons", true},
		{"/data/acme/envs/main/addons/my_mod", true},
		{"/data/acme/envs/main/addons/", true},
		{"/data/acme/envs/main/overlay", false},
		{"/data/acme/envs/main/addons_extra", false},
		{"/elsewhere", false},
	}
	for _, tc := range cases {
		if got := isUnderAddons(addons, tc.dest); got != tc.want {
			t.Errorf("isUnderAddons(%q) = %v, want %v", tc.dest, got, tc.want)
		}
	}
	if isUnderAddons("", "/anything") {
		t.Error("an empty addons path must refuse nothing")
	}
}

// The default destination is the overlay, never the per-module auto-detect.
func TestReverbPushDestinationDefaultsToOverlay(t *testing.T) {
	restore := stubRemoteDirExists(t, true)
	defer restore()

	rsc := reverbCtx()
	got, err := reverbPushDestination(context.Background(), rsc,
		PushOpts{Cfg: &config.Config{}}, pushArgs{})
	if err != nil {
		t.Fatalf("reverbPushDestination: %v", err)
	}
	if got != "/data/acme/envs/main/overlay" {
		t.Errorf("dest = %q, want the overlay", got)
	}
}

// An explicit destination under addons is refused: the next deploy would
// destroy the code.
func TestReverbPushDestinationRefusesAddons(t *testing.T) {
	rsc := reverbCtx()
	for _, dest := range []string{"addons", "/data/acme/envs/main/addons", "addons/my_mod"} {
		_, err := reverbPushDestination(context.Background(), rsc,
			PushOpts{Cfg: &config.Config{}}, pushArgs{dest: dest})
		if !errors.Is(err, ErrUsage) {
			t.Errorf("dest %q: err = %v, want ErrUsage", dest, err)
		}
		if err == nil || !strings.Contains(err.Error(), "overlay") {
			t.Errorf("dest %q: err %q does not point at the overlay", dest, err)
		}
	}
}

// A [push] path pointing at addons is refused the same way a --dest is.
func TestReverbPushDestinationRefusesConfiguredAddonsPath(t *testing.T) {
	rsc := reverbCtx()
	_, err := reverbPushDestination(context.Background(), rsc,
		PushOpts{Cfg: &config.Config{PushPath: "addons"}}, pushArgs{})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// The picker/setter flags don't apply — the payload names the destination.
func TestReverbPushDestinationRejectsPickers(t *testing.T) {
	rsc := reverbCtx()
	for _, p := range []pushArgs{{pickDest: true}, {setDest: true}} {
		if _, err := reverbPushDestination(context.Background(), rsc,
			PushOpts{Cfg: &config.Config{}}, p); !errors.Is(err, ErrUsage) {
			t.Errorf("%+v: err = %v, want ErrUsage", p, err)
		}
	}
}

// A daemon that predates the overlay contract must fail clearly.
func TestReverbPushDestinationNeedsOverlay(t *testing.T) {
	rsc := reverbCtx()
	rsc.reverb.paths.Overlay = ""
	_, err := reverbPushDestination(context.Background(), rsc,
		PushOpts{Cfg: &config.Config{}}, pushArgs{})
	if err == nil || !strings.Contains(err.Error(), "paths.overlay") {
		t.Fatalf("err = %v, want it to name paths.overlay", err)
	}
}

func TestModuleSetAndIntersect(t *testing.T) {
	present := []string{"a", "b", "c"}
	got := intersectModules(present, moduleSet([]string{"c", "a", "zz"}))
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Errorf("intersectModules = %v, want [a c] in listing order", got)
	}
}

// stubRemoteDirExists swaps the SSH probe used by the destination
// resolution so the test never shells out.
func stubRemoteDirExists(t *testing.T, exists bool) func() {
	t.Helper()
	orig := remoteDirExists
	remoteDirExists = func(ctx context.Context, sshHost, dir string) bool { return exists }
	return func() { remoteDirExists = orig }
}
