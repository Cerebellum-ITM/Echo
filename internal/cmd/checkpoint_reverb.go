package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/reverb"
)

// runCheckpointReverb maps `checkpoint` onto Reverb's snapshots.
//
// Reverb already has snapshots with exactly the intent Echo's checkpoints
// have, and its own deploy takes a pre_deploy one. Keeping a parallel
// local store for these targets would duplicate state and confuse
// rollback, so this path touches neither `config.LoadCheckpoints` nor
// `SaveCheckpoint` — the server is the only record.
func runCheckpointReverb(ctx context.Context, opts CheckpointOpts, rsc remoteShellContext, p checkpointArgs) (CheckpointResult, error) {
	if rsc.reverb.id == 0 {
		return CheckpointResult{}, fmt.Errorf(
			"reverb resolved %s without an environment id — the daemon predates the id-addressed routes",
			rsc.reverb.ref())
	}
	client, err := reverbClientFor(opts.Cfg, rsc.reverb)
	if err != nil {
		return CheckpointResult{}, err
	}

	switch p.sub {
	case "create":
		return runSnapshotCreate(ctx, opts, rsc, client)
	case "rm":
		// Deleting a snapshot throws state away, so the contract keeps it
		// admin-scoped: an echo token gets a 403. Refuse with the reason
		// rather than surfacing a bare scope error.
		return CheckpointResult{}, fmt.Errorf(
			"%w: checkpoint rm is not available on a Reverb target — deleting a snapshot is admin-scoped "+
				"(it throws state away), so it lives in the Reverb UI; `checkpoint list` still works", ErrUsage)
	default:
		return runSnapshotList(ctx, opts, rsc, client, p)
	}
}

// runSnapshotList renders the environment's snapshots through the same
// table `checkpoint list` uses for a classic target.
func runSnapshotList(ctx context.Context, opts CheckpointOpts, rsc remoteShellContext, client *reverb.Client, p checkpointArgs) (CheckpointResult, error) {
	snaps, total, err := client.ListSnapshots(ctx, rsc.reverb.id)
	if err != nil {
		return CheckpointResult{}, reverbError(err)
	}
	rows := make([]CheckpointRow, 0, len(snaps))
	for _, s := range snaps {
		rows = append(rows, snapshotRow(s))
	}
	res := CheckpointResult{
		Sub: "list", Rows: rows, DB: rsc.prof.DBName,
		DiskFreeBytes: total, DiskFree: humanBytes(total),
		JSON: p.jsonOut,
	}
	if !res.JSON {
		renderCheckpointTable(opts, res)
	}
	return res, nil
}

// snapshotRow maps one Reverb snapshot onto the checkpoint table's shape:
// the snapshot id is the name, its kind (manual / pre_deploy / …) stands
// in for the local store's method, and the age comes from created_at.
func snapshotRow(s reverb.Snapshot) CheckpointRow {
	row := CheckpointRow{
		Name: s.ID, Method: s.Kind, Status: "ok",
		SizeBytes: s.Size(), Size: humanBytes(s.Size()),
		Age: "—",
	}
	if t, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
		age := time.Since(t)
		row.AgeSeconds = int64(age.Seconds())
		row.Age = humanAge(age)
	}
	// The note is the operator's own label — the closest analogue to the
	// local store's deploy SHAs, and the column already exists.
	if n := s.NoteValue(); n != "" {
		row.DeploySHAs = []string{n}
	}
	return row
}

// runSnapshotCreate enqueues a manual snapshot and follows its job, so the
// user sees the same progress a local checkpoint prints instead of silence.
func runSnapshotCreate(ctx context.Context, opts CheckpointOpts, rsc remoteShellContext, client *reverb.Client) (CheckpointResult, error) {
	if err := confirmRemoteProd(opts.Palette, "checkpoint create", rsc, opts.Args); err != nil {
		return CheckpointResult{}, err
	}
	note := "echo checkpoint"
	jobID, err := client.CreateSnapshot(ctx, rsc.reverb.id, note)
	if err != nil {
		return CheckpointResult{}, reverbError(err)
	}
	opts.log("INFO", "snapshot", "snapshot requested", rsc.prof.DBName,
		[2]string{"env", rsc.reverb.ref()}, [2]string{"job", jobID})

	if err := followReverbJob(ctx, client, jobID, "snapshot",
		func(level, sub, msg, db string, fields ...[2]string) { opts.log(level, sub, msg, db, fields...) },
		rsc.prof.DBName); err != nil {
		return CheckpointResult{}, err
	}
	opts.log("INFO", "snapshot", "snapshot created", rsc.prof.DBName, [2]string{"job", jobID})
	return CheckpointResult{Sub: "create", DB: rsc.prof.DBName}, nil
}

// followReverbJob streams an enqueued job's events as Odoo-style log lines
// and turns a non-success terminal status into an error.
func followReverbJob(ctx context.Context, client *reverb.Client, jobID, sub string, log func(level, sub, msg, db string, fields ...[2]string), db string) error {
	done, err := client.FollowJob(ctx, jobID, reverbJobPoll, func(e reverb.JobEvent) {
		log(reverbEventLevel(e.Level), sub, e.Message, db)
	})
	if err != nil {
		return reverbError(err)
	}
	return reverb.JobFailure(done)
}

// runReverbEnvAction drives one lifecycle verb through the API instead of
// `ssh docker compose`. Reverb reconciles desired vs observed state, so a
// compose command run behind its back shows up as drift in its UI.
func runReverbEnvAction(ctx context.Context, opts DockerOpts, rsc remoteShellContext, verb string) error {
	client, err := reverbClientFor(opts.Cfg, rsc.reverb)
	if err != nil {
		return err
	}
	action, note := reverbActionFor(verb)
	if err := confirmRemoteProd(opts.Palette, verb, rsc, opts.Args); err != nil {
		return err
	}
	log := func(level, sub, msg, db string, fields ...[2]string) {
		if opts.Log != nil {
			opts.Log(level, sub, msg, db, fields...)
		}
	}
	if note != "" {
		log("INFO", "reverb", note, rsc.prof.DBName)
	}
	if svcs := remoteServiceArgs(opts.Args); len(svcs) > 0 {
		log("WARNING", "reverb", "a Reverb action targets the whole environment — ignoring the named services",
			rsc.prof.DBName, [2]string{"services", strings.Join(svcs, ",")})
	}

	jobID, err := client.EnvAction(ctx, rsc.reverb.project, rsc.reverb.env, action)
	if err != nil {
		return reverbError(err)
	}
	log("INFO", "reverb", verb+" requested", rsc.prof.DBName,
		[2]string{"env", rsc.reverb.ref()}, [2]string{"job", jobID})
	if err := followReverbJob(ctx, client, jobID, "reverb", log, rsc.prof.DBName); err != nil {
		return err
	}
	log("INFO", "reverb", verb+" complete", rsc.prof.DBName, [2]string{"job", jobID})
	return nil
}

// reverbActionFor maps Echo's four compose verbs onto Reverb's three
// lifecycle actions. `down` has no counterpart: Reverb models a DESIRED
// state, so the containers stay defined and simply stop — which is worth
// saying out loud rather than silently doing something else.
func reverbActionFor(verb string) (action, note string) {
	switch verb {
	case "up":
		return reverb.ActionStart, ""
	case "restart":
		return reverb.ActionRestart, ""
	case "down":
		return reverb.ActionStop, "reverb has no compose-style `down` — it tracks a desired state, so this stops " +
			"the environment and leaves it defined"
	default:
		return reverb.ActionStop, ""
	}
}
