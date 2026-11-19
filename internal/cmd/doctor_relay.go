package cmd

import (
	"context"
	"strconv"
	"strings"
)

// runDoctorRelay checks a Relay env. It has no server profile, project
// directory, rsync, git deploy, lock or push destination, so the checks are
// the resolve, one SSH batch and the env's two containers.
func runDoctorRelay(ctx context.Context, opts DoctorOpts, host, remotePath, name string) (DoctorResult, error) {
	display := statusProjectName(opts.Cfg, true, remotePath, name)
	res := DoctorResult{Target: DoctorTarget{Name: display, Host: host, Path: remotePath}}
	rsc, resolveErr := resolveRelayShell(ctx, opts.Cfg, host, remotePath, name, nil)
	if resolveErr == nil {
		res.Target.Host = rsc.sshHost
		res.Target.Stage, res.Target.StageDeclared, res.Target.DB = rsc.target.stage, rsc.target.stageDeclared, rsc.target.dbName
	}
	opts.log("INFO", "", "target", "",
		[2]string{"target", display}, [2]string{"host", res.Target.Host}, [2]string{"path", remotePath})

	emit := func(c DoctorCheck) {
		res.Checks = append(res.Checks, c)
		fields := append([][2]string{{"status", c.Status}}, c.Fields...)
		if c.Reason != "" {
			fields = append(fields, [2]string{"reason", c.Reason})
		}
		opts.log(doctorLevel(c.Status), c.ID, c.Message, res.Target.DB, fields...)
	}
	skip := func(reason string, ids ...string) {
		for _, id := range ids {
			emit(DoctorCheck{ID: id, Status: doctorSkipped, Reason: reason, Message: "not checked"})
		}
	}

	if resolveErr != nil {
		emit(DoctorCheck{ID: "relay", Status: doctorFailed, Message: resolveErr.Error()})
		skip("unresolved", "ssh", "containers")
		return finishDoctor(opts, res), nil
	}
	re := rsc.relay.resolved
	relayCheck := DoctorCheck{ID: "relay", Status: doctorOK, Message: "relay env resolved",
		Fields: [][2]string{{"env", rsc.relay.spec()}, {"server", re.Server}, {"branch", re.Branch}}}
	if re.Release == nil {
		relayCheck.warn("the env has no release yet")
	} else {
		relayCheck.Fields = append(relayCheck.Fields,
			[2]string{"release", strconv.FormatInt(re.Release.DeployID, 10)}, [2]string{"sha", shortSHA(re.Release.AppSHA)})
	}
	emit(relayCheck)

	if _, err := lookPath("ssh"); err != nil {
		emit(DoctorCheck{ID: "ssh", Status: doctorFailed, Message: "ssh not found on PATH"})
		skip("unreachable", "containers")
		return finishDoctor(opts, res), nil
	}
	odooName, dbName := rsc.target.odooContainer, rsc.target.dbContainer
	batchCtx, cancel := context.WithTimeout(ctx, doctorBatchTimeout)
	defer cancel()
	out, err := doctorRunSSH(batchCtx, rsc.sshHost, doctorRelayScript(odooName, dbName), nil)
	if err != nil {
		emit(DoctorCheck{ID: "ssh", Status: doctorFailed, Message: lastNonEmptyLine([]byte(err.Error())),
			Fields: [][2]string{{"host", rsc.sshHost}}})
		skip("unreachable", "containers")
		return finishDoctor(opts, res), nil
	}
	emit(DoctorCheck{ID: "ssh", Status: doctorOK, Message: "reachable", Fields: [][2]string{{"host", rsc.sshHost}}})
	emit(evalRelayContainers(string(out), odooName, dbName))
	return finishDoctor(opts, res), nil
}

// doctorRelayScript inspects both containers in one read. The trailing true
// keeps a missing container from reading as a transport failure.
func doctorRelayScript(odoo, db string) string {
	return "docker inspect -f '{{.Name}} {{.State.Running}}' " + shellQuote(odoo) + " " + shellQuote(db) + " 2>&1; true"
}

// evalRelayContainers reads the inspect output: `/<name> true` is running,
// `/<name> false` is stopped, and a name with no line does not exist.
func evalRelayContainers(out string, odoo, db string) DoctorCheck {
	running := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		name, state, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && strings.HasPrefix(name, "/") {
			running[strings.TrimPrefix(name, "/")] = state == "true"
		}
	}
	c := DoctorCheck{ID: "containers", Status: doctorOK, Message: "running",
		Fields: [][2]string{{"odoo", odoo}, {"db", db}}}
	var problems []string
	for _, name := range []string{odoo, db} {
		isRunning, exists := running[name]
		switch {
		case !exists:
			problems = append(problems, name+" does not exist")
		case !isRunning:
			problems = append(problems, name+" is not running")
		}
	}
	if len(problems) > 0 {
		c.Status, c.Message = doctorFailed, strings.Join(problems, "; ")
	}
	return c
}
