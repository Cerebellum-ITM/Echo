package cmd

import (
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func markerProfile() config.RemoteProfile {
	return config.RemoteProfile{
		DBName:   "iza_staging",
		PushPath: "/srv/reverb/projects/iza/envs/staging/overlay",
		Reverb: &config.ReverbMarker{
			EnvID: 116, Project: "iza", Env: "staging",
			APIURL: "http://reverb.internal:8080",
		},
	}
}

func TestReverbEnvFromProfile(t *testing.T) {
	t.Run("no marker is not a Reverb target", func(t *testing.T) {
		env, why := reverbEnvFromProfile(&config.Config{}, config.RemoteProfile{}, "/srv/x")
		if env != nil || why != "" {
			t.Errorf("env = %+v, why = %q, want nil and no notice", env, why)
		}
	})

	t.Run("marker without a token stays classic", func(t *testing.T) {
		env, why := reverbEnvFromProfile(&config.Config{}, markerProfile(), "/srv/x")
		if env != nil {
			t.Errorf("env = %+v, want nil without credentials", env)
		}
		if why == "" {
			t.Error("want a reason to report")
		}
	})

	t.Run("marker plus token drives the API", func(t *testing.T) {
		cfg := &config.Config{ReverbToken: "t"}
		env, why := reverbEnvFromProfile(cfg, markerProfile(), "/srv/iza/staging")
		if env == nil {
			t.Fatalf("env is nil (%s)", why)
		}
		if env.id != 116 || env.project != "iza" || env.env != "staging" {
			t.Errorf("identity = %+v", env)
		}
		if env.apiURL != "http://reverb.internal:8080" {
			t.Errorf("apiURL = %q, want the profile's", env.apiURL)
		}
		if env.paths.Overlay != markerProfile().PushPath {
			t.Errorf("overlay = %q, want the profile's push path", env.paths.Overlay)
		}
		if env.paths.ComposeDir != "/srv/iza/staging" {
			t.Errorf("compose dir = %q", env.paths.ComposeDir)
		}
		if env.paths.Addons != "" {
			t.Errorf("addons = %q, want empty (unknown, so nothing is refused)", env.paths.Addons)
		}
	})

	t.Run("the local url fills in for a marker without one", func(t *testing.T) {
		prof := markerProfile()
		prof.Reverb.APIURL = ""
		cfg := &config.Config{ReverbURL: "http://localhost:9000", ReverbToken: "t"}
		env, why := reverbEnvFromProfile(cfg, prof, "/srv/x")
		if env == nil {
			t.Fatalf("env is nil (%s)", why)
		}
		if env.apiURL != "http://localhost:9000" {
			t.Errorf("apiURL = %q, want the local one", env.apiURL)
		}
	})

	t.Run("no url anywhere stays classic", func(t *testing.T) {
		prof := markerProfile()
		prof.Reverb.APIURL = ""
		env, why := reverbEnvFromProfile(&config.Config{ReverbToken: "t"}, prof, "/srv/x")
		if env != nil || why == "" {
			t.Errorf("env = %+v, why = %q", env, why)
		}
	})
}

func TestReverbClientForPrefersTheProfileURL(t *testing.T) {
	cfg := &config.Config{ReverbURL: "http://localhost:9000", ReverbToken: "t"}
	c, err := reverbClientFor(cfg, &reverbEnv{apiURL: "http://reverb.internal:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://reverb.internal:8080" {
		t.Errorf("base url = %q, want the environment's", c.BaseURL)
	}
}
