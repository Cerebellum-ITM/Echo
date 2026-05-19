package repl

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type containerState string

const (
	stateRunning    containerState = "running"
	stateStopped    containerState = "stopped"
	stateRestarting containerState = "restarting"
	stateUnknown    containerState = "unknown"
)

type healthSnapshot struct {
	Odoo containerState
	DB   containerState
}

// dockerInspectFn is the seam used by tests to substitute the actual
// `docker inspect` call. It receives the container names and a
// per-call timeout and returns their parsed states in the same order.
var dockerInspectFn = realDockerInspect

type healthCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	snap      healthSnapshot
	expiresAt time.Time
	odooName  string
	dbName    string
}

func newHealthCache(odoo, db string, ttl time.Duration) *healthCache {
	return &healthCache{
		ttl:      ttl,
		odooName: odoo,
		dbName:   db,
		snap:     healthSnapshot{Odoo: stateUnknown, DB: stateUnknown},
	}
}

// Read returns the cached snapshot, refreshing it if expired.
// Refresh failures degrade gracefully to "unknown" per container.
func (h *healthCache) Read(ctx context.Context) healthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	if time.Now().Before(h.expiresAt) {
		return h.snap
	}

	odoo, db := dockerInspectFn(ctx, h.odooName, h.dbName, 500*time.Millisecond)
	h.snap = healthSnapshot{Odoo: odoo, DB: db}
	h.expiresAt = time.Now().Add(h.ttl)
	return h.snap
}

// Invalidate forces the next Read to refresh from docker.
func (h *healthCache) Invalidate() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expiresAt = time.Time{}
}

func realDockerInspect(parent context.Context, odoo, db string, timeout time.Duration) (containerState, containerState) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Status}}", odoo, db).Output()
	if err != nil {
		return stateUnknown, stateUnknown
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	odooState := stateUnknown
	dbState := stateUnknown
	if len(lines) > 0 {
		odooState = parseState(lines[0])
	}
	if len(lines) > 1 {
		dbState = parseState(lines[1])
	}
	return odooState, dbState
}

func parseState(raw string) containerState {
	switch strings.TrimSpace(raw) {
	case "running":
		return stateRunning
	case "restarting":
		return stateRestarting
	case "exited", "dead", "created", "paused", "removing":
		return stateStopped
	default:
		return stateUnknown
	}
}
