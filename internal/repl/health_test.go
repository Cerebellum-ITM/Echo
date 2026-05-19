package repl

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthCacheRefreshAndTTL(t *testing.T) {
	var calls int32
	orig := dockerInspectFn
	t.Cleanup(func() { dockerInspectFn = orig })
	dockerInspectFn = func(ctx context.Context, odoo, db string, timeout time.Duration) (containerState, containerState) {
		atomic.AddInt32(&calls, 1)
		return stateRunning, stateStopped
	}

	h := newHealthCache("odoo", "db", 50*time.Millisecond)
	ctx := context.Background()

	snap := h.Read(ctx)
	if snap.Odoo != stateRunning || snap.DB != stateStopped {
		t.Fatalf("first Read snapshot = %+v", snap)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected 1 inspect call, got %d", calls)
	}

	// Within TTL — no new call.
	_ = h.Read(ctx)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("Read within TTL refreshed (calls=%d)", calls)
	}

	// After TTL — refreshes.
	time.Sleep(60 * time.Millisecond)
	_ = h.Read(ctx)
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("Read after TTL did not refresh (calls=%d)", calls)
	}

	// Invalidate forces refresh on next Read.
	h.Invalidate()
	_ = h.Read(ctx)
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("Invalidate did not force refresh (calls=%d)", calls)
	}
}

func TestParseState(t *testing.T) {
	cases := map[string]containerState{
		"running":    stateRunning,
		"restarting": stateRestarting,
		"exited":     stateStopped,
		"dead":       stateStopped,
		"paused":     stateStopped,
		"":           stateUnknown,
		"weird":      stateUnknown,
	}
	for in, want := range cases {
		if got := parseState(in); got != want {
			t.Errorf("parseState(%q) = %q, want %q", in, got, want)
		}
	}
}
