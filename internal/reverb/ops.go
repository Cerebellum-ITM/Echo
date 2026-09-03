package reverb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// The action routes past resolve are addressed by the environment's
// numeric id, except the lifecycle verbs, which Reverb exposes by name.

// Overlay is what GET /environments/{id}/overlay reports: everything the
// dev overlay holds, and the subset that shadows the deployed git
// checkout. Shadowed is computed server-side against the deployed tree —
// a client has no local copy of it, and Odoo gives no signal at all when
// a module is shadowed.
type Overlay struct {
	Modules   []string `json:"modules"`
	Shadowed  []string `json:"shadowed"`
	SizeBytes int64    `json:"size_bytes"`
}

// GetOverlay reports the environment's overlay contents and shadowing.
func (c *Client) GetOverlay(ctx context.Context, envID int64) (Overlay, error) {
	var out Overlay
	if err := c.get(ctx, "/api/v1/environments/"+strconv.FormatInt(envID, 10)+"/overlay", &out); err != nil {
		return Overlay{}, err
	}
	return out, nil
}

// Snapshot is one row of GET /environments/{id}/snapshots. Reverb's
// snapshots are what Echo's checkpoints map to in Reverb mode — Echo
// keeps no parallel store for these targets.
type Snapshot struct {
	ID            string  `json:"id"`
	EnvironmentID int64   `json:"environment_id"`
	Kind          string  `json:"kind"`
	Note          *string `json:"note"`
	SizeBytes     *int64  `json:"size_bytes"`
	CreatedAt     string  `json:"created_at"`
}

// NoteValue returns the snapshot's note, or "" when unset.
func (s Snapshot) NoteValue() string {
	if s.Note == nil {
		return ""
	}
	return *s.Note
}

// Size returns the snapshot's size in bytes, or 0 when not measured yet.
func (s Snapshot) Size() int64 {
	if s.SizeBytes == nil {
		return 0
	}
	return *s.SizeBytes
}

// snapshotList is the listing envelope.
type snapshotList struct {
	Snapshots      []Snapshot `json:"snapshots"`
	TotalSizeBytes int64      `json:"total_size_bytes"`
}

// ListSnapshots returns the environment's snapshots (newest first) and
// their total size on disk.
func (c *Client) ListSnapshots(ctx context.Context, envID int64) ([]Snapshot, int64, error) {
	var out snapshotList
	if err := c.get(ctx, "/api/v1/environments/"+strconv.FormatInt(envID, 10)+"/snapshots", &out); err != nil {
		return nil, 0, err
	}
	return out.Snapshots, out.TotalSizeBytes, nil
}

// jobAccepted is the 202 body every enqueue returns.
type jobAccepted struct {
	JobID string `json:"job_id"`
}

// CreateSnapshot enqueues a manual snapshot and returns its job id. The
// work happens asynchronously — follow the job to report progress.
func (c *Client) CreateSnapshot(ctx context.Context, envID int64, note string) (string, error) {
	var out jobAccepted
	body := map[string]string{"note": note}
	if err := c.post(ctx, "/api/v1/environments/"+strconv.FormatInt(envID, 10)+"/snapshots", body, &out); err != nil {
		return "", err
	}
	return out.JobID, nil
}

// Lifecycle actions. Reverb reconciles desired vs observed state, so
// these go through the API rather than `ssh docker compose` — a compose
// command run behind its back shows up as drift in the UI.
const (
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
)

// EnvAction enqueues a lifecycle action and returns its job id. Unlike
// the snapshot/deploy routes, these are addressed by name.
func (c *Client) EnvAction(ctx context.Context, project, env, action string) (string, error) {
	var out jobAccepted
	path := "/api/v1/projects/" + url.PathEscape(project) +
		"/envs/" + url.PathEscape(env) + "/" + url.PathEscape(action)
	if err := c.post(ctx, path, map[string]any{}, &out); err != nil {
		return "", err
	}
	return out.JobID, nil
}

// Deploy enqueues a deploy of a revision already pushed to the project's
// Reverb repo, returning its job id.
func (c *Client) Deploy(ctx context.Context, envID int64, rev string) (string, error) {
	var out jobAccepted
	body := map[string]string{"rev": rev}
	if err := c.post(ctx, "/api/v1/environments/"+strconv.FormatInt(envID, 10)+"/deploy", body, &out); err != nil {
		return "", err
	}
	return out.JobID, nil
}

// Job is one unit of asynchronous work. Status is queued | running |
// succeeded | failed | canceled.
type Job struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	ProjectID     *int64  `json:"project_id"`
	EnvironmentID *int64  `json:"environment_id"`
	Status        string  `json:"status"`
	Error         *string `json:"error"`
	CreatedAt     string  `json:"created_at"`
	StartedAt     *string `json:"started_at"`
	FinishedAt    *string `json:"finished_at"`
}

// Job kinds Echo cares about: the two that make resolve answer 409.
const (
	KindEnvCreate = "env_create"
	KindEnvFork   = "env_fork"
)

// Done reports whether the job reached a terminal status.
func (j Job) Done() bool {
	switch j.Status {
	case "succeeded", "failed", "canceled":
		return true
	}
	return false
}

// ErrorValue returns the job's failure message, or "".
func (j Job) ErrorValue() string {
	if j.Error == nil {
		return ""
	}
	return *j.Error
}

// JobEvent is one progress line a job emitted. Seq is monotonic per job,
// which is what lets a poller print each event exactly once.
type JobEvent struct {
	Seq     int    `json:"seq"`
	Level   string `json:"level"`
	Message string `json:"message"`
	TS      string `json:"ts"`
}

// jobDetail is the GET /jobs/{id} envelope.
type jobDetail struct {
	Job    Job        `json:"job"`
	Events []JobEvent `json:"events"`
}

// GetJob returns a job together with every progress event it has emitted.
func (c *Client) GetJob(ctx context.Context, id string) (Job, []JobEvent, error) {
	var out jobDetail
	if err := c.get(ctx, "/api/v1/jobs/"+url.PathEscape(id), &out); err != nil {
		return Job{}, nil, err
	}
	return out.Job, out.Events, nil
}

// ListJobs returns the recent jobs of one environment, newest first.
func (c *Client) ListJobs(ctx context.Context, envID int64) ([]Job, error) {
	var out []Job
	if err := c.get(ctx, "/api/v1/jobs?environment_id="+strconv.FormatInt(envID, 10), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FollowJob polls a job until it reaches a terminal status, handing each
// progress event to onEvent exactly once (deduplicated by Seq, which is
// monotonic per job). It returns the final job — a failed job is NOT an
// error here; the caller decides what a failure means in its context.
func (c *Client) FollowJob(ctx context.Context, id string, poll time.Duration, onEvent func(JobEvent)) (Job, error) {
	lastSeq := -1
	for {
		job, events, err := c.GetJob(ctx, id)
		if err != nil {
			return Job{}, err
		}
		for _, e := range events {
			if e.Seq <= lastSeq {
				continue
			}
			lastSeq = e.Seq
			if onEvent != nil {
				onEvent(e)
			}
		}
		if job.Done() {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// ProvisioningJob returns the in-flight env_create/env_fork of an
// environment — the job a `409 not_ready` is waiting on — or false when
// none is running (the 409 is then either stale or about to clear).
func ProvisioningJob(jobs []Job) (Job, bool) {
	for _, j := range jobs {
		inFlight := j.Status == "queued" || j.Status == "running"
		provisioning := j.Kind == KindEnvCreate || j.Kind == KindEnvFork
		if inFlight && provisioning {
			return j, true
		}
	}
	return Job{}, false
}

// JobFailure turns a terminal non-success job into an error naming it and
// its message. A succeeded job returns nil.
func JobFailure(j Job) error {
	if j.Status == "succeeded" {
		return nil
	}
	if msg := j.ErrorValue(); msg != "" {
		return fmt.Errorf("reverb job %s (%s) %s: %s", j.ID, j.Kind, j.Status, msg)
	}
	return fmt.Errorf("reverb job %s (%s) %s", j.ID, j.Kind, j.Status)
}
