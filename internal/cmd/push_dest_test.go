package cmd

import (
	"errors"
	"reflect"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParsePushArgsDest(t *testing.T) {
	t.Run("dest with value", func(t *testing.T) {
		p, err := parsePushArgs([]string{"sale", "--dest", "build/addons"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if p.dest != "build/addons" || !reflect.DeepEqual(p.modules, []string{"sale"}) {
			t.Errorf("got dest=%q modules=%v", p.dest, p.modules)
		}
	})
	t.Run("dest equals form", func(t *testing.T) {
		p, err := parsePushArgs([]string{"--dest=/srv/build"})
		if err != nil || p.dest != "/srv/build" {
			t.Fatalf("got dest=%q err=%v", p.dest, err)
		}
	})
	t.Run("pick-dest and mkdir", func(t *testing.T) {
		p, err := parsePushArgs([]string{"--pick-dest", "--mkdir"})
		if err != nil || !p.pickDest || !p.mkdir {
			t.Fatalf("got pick=%v mkdir=%v err=%v", p.pickDest, p.mkdir, err)
		}
	})
	t.Run("empty dest errors", func(t *testing.T) {
		if _, err := parsePushArgs([]string{"--dest"}); !errors.Is(err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage", err)
		}
		if _, err := parsePushArgs([]string{"--dest="}); !errors.Is(err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage", err)
		}
	})
	t.Run("dest + pick-dest mutually exclusive", func(t *testing.T) {
		if _, err := parsePushArgs([]string{"--dest", "x", "--pick-dest"}); !errors.Is(err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage", err)
		}
	})
	t.Run("set-dest", func(t *testing.T) {
		p, err := parsePushArgs([]string{"--set-dest", "--from", "prod"})
		if err != nil || !p.setDest || p.from != "prod" {
			t.Fatalf("got setDest=%v from=%q err=%v", p.setDest, p.from, err)
		}
	})
	t.Run("set-dest with dest", func(t *testing.T) {
		p, err := parsePushArgs([]string{"--set-dest", "--dest", "build/addons"})
		if err != nil || !p.setDest || p.dest != "build/addons" {
			t.Fatalf("got setDest=%v dest=%q err=%v", p.setDest, p.dest, err)
		}
	})
}

func TestResolvePushDest(t *testing.T) {
	tru := true
	tests := []struct {
		name      string
		p         pushArgs
		prof      config.RemoteProfile
		cfg       *config.Config
		wantDest  string
		wantSrc   string
		wantMkdir bool
	}{
		{"flag wins over all", pushArgs{dest: "build/addons", mkdir: true},
			config.RemoteProfile{PushPath: "srv"}, &config.Config{PushPath: "local"},
			"build/addons", "flag", true},
		{"server over local", pushArgs{},
			config.RemoteProfile{PushPath: "srv/addons", PushMkdir: &tru}, &config.Config{PushPath: "local"},
			"srv/addons", "server", true},
		{"local fallback", pushArgs{},
			config.RemoteProfile{}, &config.Config{PushPath: "local/addons", PushMkdir: &tru},
			"local/addons", "local", true},
		{"none → auto", pushArgs{},
			config.RemoteProfile{}, &config.Config{},
			"", "", false},
		{"flag mkdir enables even when server has none", pushArgs{mkdir: true},
			config.RemoteProfile{PushPath: "srv"}, nil,
			"srv", "server", true},
		{"nil cfg safe", pushArgs{}, config.RemoteProfile{}, nil, "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest, src, mkdir := resolvePushDest(tc.p, tc.prof, tc.cfg)
			if dest != tc.wantDest || src != tc.wantSrc || mkdir != tc.wantMkdir {
				t.Errorf("resolvePushDest = (%q, %q, %v), want (%q, %q, %v)",
					dest, src, mkdir, tc.wantDest, tc.wantSrc, tc.wantMkdir)
			}
		})
	}
}

func TestResolveDestPath(t *testing.T) {
	tests := []struct {
		remotePath, dest, want string
	}{
		{"/srv/odoo", "build/addons", "/srv/odoo/build/addons"},
		{"/srv/odoo", "./build/", "/srv/odoo/build"},
		{"/srv/odoo", "/abs/dir", "/abs/dir"},
		{"/srv/odoo", ".", ""},
		{"/srv/odoo", "", ""},
		{"/srv/odoo", "/", ""},
		{"/srv/odoo", "  ", ""},
	}
	for _, tc := range tests {
		if got := resolveDestPath(tc.remotePath, tc.dest); got != tc.want {
			t.Errorf("resolveDestPath(%q, %q) = %q, want %q", tc.remotePath, tc.dest, got, tc.want)
		}
	}
}

func TestDirPickerEntries(t *testing.T) {
	// At root: no "up" row.
	got := dirPickerEntries("/", []string{"srv", "etc"})
	want := []string{dirPickerUse, "srv", "etc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("root entries = %v, want %v", got, want)
	}
	// Below root: "up" present after "use".
	got = dirPickerEntries("/srv/odoo", []string{"addons", "custom"})
	want = []string{dirPickerUse, dirPickerUp, "addons", "custom"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nested entries = %v, want %v", got, want)
	}
}

func TestUnderPath(t *testing.T) {
	tests := []struct {
		base, p, wantRel string
		wantOK           bool
	}{
		{"/srv/odoo", "/srv/odoo/build/addons", "build/addons", true},
		{"/srv/odoo", "/srv/odoo/addons", "addons", true},
		{"/srv/odoo", "/srv/odoo", "", false},
		{"/srv/odoo", "/other/dir", "", false},
	}
	for _, tc := range tests {
		rel, ok := underPath(tc.base, tc.p)
		if rel != tc.wantRel || ok != tc.wantOK {
			t.Errorf("underPath(%q, %q) = (%q, %v), want (%q, %v)",
				tc.base, tc.p, rel, ok, tc.wantRel, tc.wantOK)
		}
	}
}
