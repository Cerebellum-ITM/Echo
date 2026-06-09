package cmd

import (
	"testing"
	"time"

	"github.com/pascualchavez/echo/internal/config"
)

func TestParseConnectArgsFresh(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		wantLogin     string
		wantInactive  bool
		wantFresh     bool
		wantNewWindow bool
	}{
		{"plain login", []string{"admin"}, "admin", false, false, false},
		{"fresh flag", []string{"admin", "--fresh"}, "admin", false, true, false},
		{"all + fresh", []string{"--all", "--fresh"}, "", true, true, false},
		{"fresh before login", []string{"--fresh", "jdoe"}, "jdoe", false, true, false},
		{"force unaffected", []string{"admin", "--force"}, "admin", false, false, false},
		{"new-window flag", []string{"admin", "--new-window"}, "admin", false, false, true},
		{"fresh + new-window", []string{"jdoe", "--fresh", "--new-window"}, "jdoe", false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			login, inactive, fresh, newWindow := parseConnectArgs(tc.args)
			if login != tc.wantLogin || inactive != tc.wantInactive || fresh != tc.wantFresh || newWindow != tc.wantNewWindow {
				t.Errorf("got (%q, %v, %v, %v), want (%q, %v, %v, %v)",
					login, inactive, fresh, newWindow, tc.wantLogin, tc.wantInactive, tc.wantFresh, tc.wantNewWindow)
			}
		})
	}
}

func TestConnectCacheKeyDistinct(t *testing.T) {
	local := ConnectOpts{Cfg: &config.Config{}, Root: "/srv/projectA"}
	localTarget := connectTarget{dbName: "db1"}

	remote := ConnectOpts{Cfg: &config.Config{
		ConnectSSHHost: "erp", ConnectRemotePath: "/opt/odoo",
	}}
	remoteTarget := connectTarget{remote: true, dbName: "db1"}

	kLocal := connectCacheKey(local, localTarget)
	kRemote := connectCacheKey(remote, remoteTarget)
	if kLocal == kRemote {
		t.Error("local and remote targets must not share a cache key")
	}

	// Same identity → stable key.
	if connectCacheKey(local, localTarget) != kLocal {
		t.Error("cache key must be deterministic for the same identity")
	}

	// Different db → different key.
	if connectCacheKey(local, connectTarget{dbName: "db2"}) == kLocal {
		t.Error("different db must yield a different cache key")
	}
}

func TestConnectSessionExpired(t *testing.T) {
	fresh := config.ConnectSession{MintedAt: time.Now().Add(-1 * time.Hour)}
	if connectSessionExpired(fresh) {
		t.Error("a 1h-old session should not be expired")
	}
	stale := config.ConnectSession{MintedAt: time.Now().Add(-(connectSessionTTL + time.Hour))}
	if !connectSessionExpired(stale) {
		t.Error("a session past the TTL should be expired")
	}
}
