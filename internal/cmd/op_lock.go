package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path"
	"strings"
	"syscall"
	"time"
)

// The operation lock keeps two operations that change a target's code or
// database (db-push, deploy, a watch cycle, sideload, checkpoint create/rm)
// from running against the same target at once. Its directory is
// created with mkdir, which is atomic on the server, and holds one JSON file
// naming the holder. It sits under .echo/ next to the deploy lock, which is
// metadata and never blocks anything.
const (
	opLockDir    = lockDirName + "/op.lock.d"
	opLockFile   = opLockDir + "/op.lock"
	opLockMaxAge = 6 * time.Hour
)

// OpLock is the holder record of the operation lock.
type OpLock struct {
	Command    string    `json:"command"`
	PID        int       `json:"pid"`
	ClientHost string    `json:"client_host"`
	User       string    `json:"user"`
	Started    time.Time `json:"started"`
}

var opLockNow = time.Now

func newOpLock(command string) OpLock {
	host, _ := os.Hostname()
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return OpLock{Command: command, PID: os.Getpid(), ClientHost: host, User: name, Started: opLockNow().UTC().Truncate(time.Second)}
}

// errOpLockHeld is returned while another operation holds the lock.
type errOpLockHeld struct{ holder *OpLock }

func (e errOpLockHeld) Error() string {
	if e.holder == nil {
		return "target busy: the operation lock is held by an unknown holder (" + opLockDir + ")"
	}
	return fmt.Sprintf("target busy: %s from %s since %s", e.holder.Command, e.holder.ClientHost,
		e.holder.Started.Format(time.RFC3339))
}

func opLockAcquireScript(remotePath string) string {
	root := shellQuote(remotePath)
	dir := shellQuote(path.Join(remotePath, lockDirName))
	return "cd " + root + " && mkdir -p " + dir +
		" && { [ -f " + dir + "/.gitignore ] || printf '*\\n' > " + dir + "/.gitignore; }" +
		" && if mkdir " + opLockDir + " 2>/dev/null; then cat > " + opLockFile + " && echo acquired;" +
		" else cat " + opLockFile + " 2>/dev/null; echo; echo held; fi"
}

// acquireOpLock takes the target's operation lock for command. A held lock
// fails with errOpLockHeld; with force, a lock older than opLockMaxAge (or
// one whose holder cannot be read) is taken over and a WARNING names the old
// holder. A younger lock is never taken over.
func acquireOpLock(ctx context.Context, rsc remoteShellContext, command string, force bool, log logFn) (OpLock, error) {
	mine := newOpLock(command)
	body, _ := json.Marshal(mine)
	for attempt := 0; attempt < 2; attempt++ {
		out, err := ckptRunSSH(ctx, rsc.sshHost, opLockAcquireScript(rsc.remotePath), body)
		if err != nil {
			return OpLock{}, fmt.Errorf("operation lock: %w", err)
		}
		lines := nonEmptyLines(string(out))
		if len(lines) > 0 && lines[len(lines)-1] == "acquired" {
			return mine, nil
		}
		holder := parseOpLock(strings.Join(lines, "\n"))
		stale := holder == nil || opLockNow().Sub(holder.Started) > opLockMaxAge
		if !force || !stale || attempt > 0 {
			return OpLock{}, errOpLockHeld{holder}
		}
		fields := [][2]string{{"lock", opLockDir}}
		if holder != nil {
			fields = append(fields, [2]string{"command", holder.Command}, [2]string{"client_host", holder.ClientHost},
				[2]string{"started", holder.Started.Format(time.RFC3339)})
		}
		ckptLog(log, "WARNING", "lock", "taking over a stale operation lock", rsc.prof.DBName, fields...)
		if err := releaseOpLock(ctx, rsc); err != nil {
			return OpLock{}, fmt.Errorf("operation lock: %w", err)
		}
	}
	return OpLock{}, errOpLockHeld{}
}

// holdOpLock takes the operation lock for command and returns the context the
// locked work must run under plus the release to defer. Ctrl+C and SIGTERM
// cancel that context instead of killing the process, so the release always
// runs, on a fresh context; a failed release is only a WARNING.
func holdOpLock(ctx context.Context, rsc remoteShellContext, command string, force bool, log logFn) (context.Context, func(), error) {
	if _, err := acquireOpLock(ctx, rsc, command, force, log); err != nil {
		return ctx, nil, err
	}
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	release := func() {
		defer stopSignals()
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := releaseOpLock(rctx, rsc); err != nil {
			ckptLog(log, "WARNING", "lock", "operation lock not released", rsc.prof.DBName,
				[2]string{"lock", opLockDir}, [2]string{"err", err.Error()})
		}
	}
	return ctx, release, nil
}

// opLockHolderFields names a holder in a "target busy" log line.
func opLockHolderFields(holder *OpLock) [][2]string {
	if holder == nil {
		return [][2]string{{"lock", opLockDir}}
	}
	return [][2]string{{"command", holder.Command}, {"host", holder.ClientHost},
		{"since", holder.Started.Format(time.RFC3339)}}
}

func releaseOpLock(ctx context.Context, rsc remoteShellContext) error {
	_, err := ckptRunSSH(ctx, rsc.sshHost, "cd "+shellQuote(rsc.remotePath)+" && rm -rf "+opLockDir, nil)
	return err
}

// readOpLock reports the current holder, or nil when the lock is free.
// held is true for a lock directory whose holder file cannot be read.
func readOpLock(ctx context.Context, rsc remoteShellContext) (holder *OpLock, held bool, err error) {
	script := "cd " + shellQuote(rsc.remotePath) + " && if [ -d " + opLockDir + " ]; then cat " + opLockFile +
		" 2>/dev/null; echo; echo held; else echo free; fi"
	out, err := ckptRunSSH(ctx, rsc.sshHost, script, nil)
	if err != nil {
		return nil, false, err
	}
	lines := nonEmptyLines(string(out))
	if len(lines) == 0 || lines[len(lines)-1] != "held" {
		return nil, false, nil
	}
	return parseOpLock(strings.Join(lines[:len(lines)-1], "\n")), true, nil
}

func parseOpLock(out string) *OpLock {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var l OpLock
		if json.Unmarshal([]byte(line), &l) == nil && l.Command != "" {
			return &l
		}
	}
	return nil
}
