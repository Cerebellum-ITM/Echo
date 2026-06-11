package cmd

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// connectSessionTTL bounds how long a cached session is even considered for
// reuse. It sits below Odoo's default 7-day session GC so an expired cookie
// is re-minted before the server would have garbage-collected it. The HTTP
// probe is the real validity check; this is just a cheap pre-filter that
// skips probing obviously-stale entries.
const connectSessionTTL = 5 * 24 * time.Hour

// fetchAllLabel is the sentinel row appended to the recent-sessions picker
// that drops back to querying the full user list.
const fetchAllLabel = "↻  Fetch all users…"

// connectCacheKey derives the per-target cache filename stem from the
// connection identity: the SSH host + remote path for a remote target, or
// the local project root for a local one, always scoped by db name. Two
// distinct destinations never share a cache file.
func connectCacheKey(opts ConnectOpts, target connectTarget) string {
	var id string
	if target.remote {
		id = "ssh:" + opts.Cfg.ConnectSSHHost + ":" + opts.Cfg.ConnectRemotePath + ":" + target.dbName
	} else {
		id = "local:" + opts.Root + ":" + target.dbName
	}
	return config.ProjectKey(id)
}

// connectSessionExpired reports whether a cached session is past the TTL.
func connectSessionExpired(s config.ConnectSession) bool {
	return time.Since(s.MintedAt) > connectSessionTTL
}

// probeSession checks that the cached cookie still authenticates by hitting
// `<baseURL>/odoo` with it and not following redirects: a logged-in Odoo
// answers 2xx, an expired/invalid session redirects (303) to the login
// page. Any transport error counts as invalid so the caller re-mints.
func probeSession(ctx context.Context, baseURL, sid string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, baseURL+"/odoo", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Cookie", "session_id="+sid)
	client := &http.Client{
		Timeout:       6 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// pickRecentSessions shows the cached logins (most recently minted first)
// plus a "fetch all" row. It returns the chosen login, or fetchAll=true
// when the user wants the full list instead.
func pickRecentSessions(cache map[string]config.ConnectSession, palette theme.Palette, stage string) (login string, fetchAll bool, err error) {
	entries := make([]config.ConnectSession, 0, len(cache))
	for _, e := range cache {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].MintedAt.After(entries[j].MintedAt)
	})

	maxLogin := 0
	for _, e := range entries {
		if len(e.Login) > maxLogin {
			maxLogin = len(e.Login)
		}
	}
	labels := make([]string, 0, len(entries)+1)
	byLabel := make(map[string]string, len(entries))
	for _, e := range entries {
		lbl := fmt.Sprintf("  %-*s  (cached)", maxLogin, e.Login)
		labels = append(labels, lbl)
		byLabel[lbl] = e.Login
	}
	labels = append(labels, fetchAllLabel)

	chosen, err := runSingleFuzzyPickerStaged("Reconnect as (recent) — or fetch all", labels, palette, stage)
	if err != nil {
		return "", false, err
	}
	if chosen == fetchAllLabel {
		return "", true, nil
	}
	return byLabel[chosen], false, nil
}
