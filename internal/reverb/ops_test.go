package reverb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveDecodesIDAndSSHPort(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":48,"ssh_host":"deploy@10.0.0.5","ssh_port":1024}`))
	})
	env, err := c.Resolve(context.Background(), "acme", "main")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env.ID != 48 {
		t.Errorf("ID = %d, want 48", env.ID)
	}
	if env.SSHPort != 1024 {
		t.Errorf("SSHPort = %d, want 1024", env.SSHPort)
	}
}

// ssh_port is omitted when it is 22, so absent must decode to 0 and the
// caller reads that as "the default".
func TestResolveOmittedSSHPortIsZero(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"ssh_host":"deploy@h"}`))
	})
	env, _ := c.Resolve(context.Background(), "p", "e")
	if env.SSHPort != 0 {
		t.Errorf("SSHPort = %d, want 0 for an omitted port", env.SSHPort)
	}
}

func TestListEnvsCarriesID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":3,"project":"acme","env":"main"}]`))
	})
	refs, err := c.ListEnvs(context.Background())
	if err != nil || len(refs) != 1 || refs[0].ID != 3 {
		t.Fatalf("refs = %+v, err = %v", refs, err)
	}
}

func TestGetOverlay(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"modules":["a","b"],"shadowed":["b"],"size_bytes":4096}`))
	})
	ov, err := c.GetOverlay(context.Background(), 48)
	if err != nil {
		t.Fatalf("GetOverlay: %v", err)
	}
	if gotPath != "/api/v1/environments/48/overlay" {
		t.Errorf("path = %q", gotPath)
	}
	if len(ov.Modules) != 2 || len(ov.Shadowed) != 1 || ov.Shadowed[0] != "b" || ov.SizeBytes != 4096 {
		t.Errorf("overlay = %+v", ov)
	}
}

func TestListSnapshots(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"snapshots":[
		  {"id":"snap_1","environment_id":48,"kind":"manual","note":"before x","size_bytes":512,"created_at":"2026-07-28T10:00:00Z"},
		  {"id":"snap_2","environment_id":48,"kind":"pre_deploy","note":null,"size_bytes":null,"created_at":"2026-07-27T10:00:00Z"}
		],"total_size_bytes":512}`))
	})
	snaps, total, err := c.ListSnapshots(context.Background(), 48)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if gotPath != "/api/v1/environments/48/snapshots" {
		t.Errorf("path = %q", gotPath)
	}
	if len(snaps) != 2 || total != 512 {
		t.Fatalf("snaps = %+v total = %d", snaps, total)
	}
	if snaps[0].NoteValue() != "before x" || snaps[0].Size() != 512 {
		t.Errorf("snap[0] = %+v", snaps[0])
	}
	// A null note / size must degrade to the zero value, not panic.
	if snaps[1].NoteValue() != "" || snaps[1].Size() != 0 {
		t.Errorf("snap[1] = %+v", snaps[1])
	}
}

func TestCreateSnapshotPostsNote(t *testing.T) {
	var gotPath, gotMethod, gotBody, gotCT string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotCT = r.URL.Path, r.Method, r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"j_7"}`))
	})
	job, err := c.CreateSnapshot(context.Background(), 48, "before the update")
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if job != "j_7" {
		t.Errorf("job = %q", job)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/environments/48/snapshots" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	var body map[string]string
	_ = json.Unmarshal([]byte(gotBody), &body)
	if body["note"] != "before the update" {
		t.Errorf("body = %s", gotBody)
	}
}

func TestEnvActionIsAddressedByName(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"j_9"}`))
	})
	job, err := c.EnvAction(context.Background(), "acme", "feature-x", ActionRestart)
	if err != nil {
		t.Fatalf("EnvAction: %v", err)
	}
	if job != "j_9" {
		t.Errorf("job = %q", job)
	}
	if gotPath != "/api/v1/projects/acme/envs/feature-x/restart" {
		t.Errorf("path = %q", gotPath)
	}
}

// An echo token may not restore or delete a snapshot; the 403 must reach
// the caller as ErrForbidden so it can explain the scope.
func TestEnvActionForbiddenSurfaces(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_scope","message":"admin only"}}`))
	})
	if _, err := c.EnvAction(context.Background(), "p", "e", ActionStop); err == nil ||
		!strings.Contains(err.Error(), "admin only") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeployPostsRev(t *testing.T) {
	var gotPath, gotBody string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"j_1"}`))
	})
	if _, err := c.Deploy(context.Background(), 48, "abc123"); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if gotPath != "/api/v1/environments/48/deploy" || !strings.Contains(gotBody, "abc123") {
		t.Errorf("%s %s", gotPath, gotBody)
	}
}

// FollowJob must print each event exactly once even though every poll
// returns the full event list.
func TestFollowJobDeduplicatesEventsBySeq(t *testing.T) {
	var polls int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&polls, 1)
		switch n {
		case 1:
			_, _ = w.Write([]byte(`{"job":{"id":"j_1","kind":"env_create","status":"running"},
			  "events":[{"seq":1,"level":"info","message":"cloning"}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"job":{"id":"j_1","kind":"env_create","status":"running"},
			  "events":[{"seq":1,"level":"info","message":"cloning"},
			            {"seq":2,"level":"info","message":"starting"}]}`))
		default:
			_, _ = w.Write([]byte(`{"job":{"id":"j_1","kind":"env_create","status":"succeeded"},
			  "events":[{"seq":1,"level":"info","message":"cloning"},
			            {"seq":2,"level":"info","message":"starting"},
			            {"seq":3,"level":"info","message":"done"}]}`))
		}
	})
	var seen []string
	job, err := c.FollowJob(context.Background(), "j_1", time.Millisecond, func(e JobEvent) {
		seen = append(seen, e.Message)
	})
	if err != nil {
		t.Fatalf("FollowJob: %v", err)
	}
	if job.Status != "succeeded" {
		t.Errorf("status = %q", job.Status)
	}
	want := []string{"cloning", "starting", "done"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v (each exactly once)", seen, want)
	}
}

// A failed job is not a transport error — the caller decides. FollowJob
// returns it, and JobFailure turns it into the message.
func TestFollowJobReturnsFailedJob(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"job":{"id":"j_2","kind":"env_create","status":"failed","error":"disk full"},"events":[]}`))
	})
	job, err := c.FollowJob(context.Background(), "j_2", time.Millisecond, nil)
	if err != nil {
		t.Fatalf("FollowJob returned a transport error: %v", err)
	}
	if !job.Done() || job.Status != "failed" {
		t.Fatalf("job = %+v", job)
	}
	ferr := JobFailure(job)
	if ferr == nil || !strings.Contains(ferr.Error(), "disk full") || !strings.Contains(ferr.Error(), "j_2") {
		t.Errorf("JobFailure = %v", ferr)
	}
	if JobFailure(Job{Status: "succeeded"}) != nil {
		t.Error("a succeeded job must not be a failure")
	}
}

func TestListJobsFiltersByEnvironment(t *testing.T) {
	var gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"id":"j_1","kind":"env_create","status":"running"}]`))
	})
	jobs, err := c.ListJobs(context.Background(), 48)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v err = %v", jobs, err)
	}
	if gotQuery != "environment_id=48" {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestProvisioningJob(t *testing.T) {
	// Only an in-flight create/fork counts — that is what a 409 waits on.
	jobs := []Job{
		{ID: "j_1", Kind: "env_deploy", Status: "running"},
		{ID: "j_2", Kind: KindEnvCreate, Status: "succeeded"},
		{ID: "j_3", Kind: KindEnvFork, Status: "queued"},
	}
	got, ok := ProvisioningJob(jobs)
	if !ok || got.ID != "j_3" {
		t.Errorf("ProvisioningJob = (%+v, %v), want j_3", got, ok)
	}
	if _, ok := ProvisioningJob(jobs[:2]); ok {
		t.Error("no in-flight create/fork must report none")
	}
}

func TestJobDone(t *testing.T) {
	for _, s := range []string{"succeeded", "failed", "canceled"} {
		if !(Job{Status: s}).Done() {
			t.Errorf("%s must be terminal", s)
		}
	}
	for _, s := range []string{"queued", "running"} {
		if (Job{Status: s}).Done() {
			t.Errorf("%s must not be terminal", s)
		}
	}
}

func TestUpdateModulesPostsListAndCheckpoint(t *testing.T) {
	var gotPath, gotBody string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"j_u"}`))
	})
	id, err := c.UpdateModules(context.Background(), 116, []string{"crm_iza", "sale_iza"}, false)
	if err != nil || id != "j_u" {
		t.Fatalf("UpdateModules: %v (%q)", err, id)
	}
	if gotPath != "/api/v1/environments/116/update" ||
		!strings.Contains(gotBody, `"modules":["crm_iza","sale_iza"]`) || !strings.Contains(gotBody, `"snapshot":false`) {
		t.Errorf("%s %s", gotPath, gotBody)
	}
}
