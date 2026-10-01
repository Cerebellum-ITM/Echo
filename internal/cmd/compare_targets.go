package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pascualchavez/echo/internal/config"
)

// CompareTargetsOpts configures `compare --targets <a>,<b>`.
type CompareTargetsOpts struct {
	Cfg  *config.Config
	Root string
	Args []string
	// Log emits the per-side lines under `echo.compare.targets`.
	Log func(level, sub, msg, db string, fields ...[2]string)
}

func (o CompareTargetsOpts) log(level, sub, msg, db string, fields ...[2]string) {
	if o.Log != nil {
		o.Log(level, sub, msg, db, fields...)
	}
}

// Row statuses of a target comparison.
const (
	targetSame    = "same"
	targetDiffers = "differs"
	targetOnly    = "only"
	targetUnknown = "unknown"
)

// Reasons an `unknown` row gives.
const (
	reasonDirty      = "dirty"
	reasonNoIdentity = "no-identity"
	reasonUnreadable = "unreadable"
)

// lockSourceBase marks the cell of a module a git-deploy side carries on its
// lock base without an entry of its own.
const lockSourceBase = "base"

// TargetSide is one target of the comparison and how its lock read ended.
type TargetSide struct {
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Path      string    `json:"path"`
	LockState string    `json:"lock"`
	Error     string    `json:"error"`
	Base      *LockBase `json:"base"`

	state lockState
	lock  DeployLock
}

// Failed reports whether the side's lock could not be read or parsed.
func (s TargetSide) Failed() bool {
	return s.state == lockUnreadable || s.state == lockCorrupt
}

// TargetRow is one module compared across both sides. A and B are nil when
// that side has no entry for it.
type TargetRow struct {
	Name   string      `json:"name"`
	Status string      `json:"status"`
	Only   string      `json:"only"`
	Newer  string      `json:"newer"`
	Reason string      `json:"reason"`
	A      *LockModule `json:"a"`
	B      *LockModule `json:"b"`
	// Named is set for modules given as positionals, which print even when same.
	Named bool `json:"-"`
}

// StatusLabel is the status column: `only dev` for an `only` row.
func (r TargetRow) StatusLabel() string {
	if r.Status == targetOnly {
		return targetOnly + " " + r.Only
	}
	return r.Status
}

// TargetCounts tallies the rows by status.
type TargetCounts struct {
	Same    int `json:"same"`
	Differs int `json:"differs"`
	Unknown int `json:"unknown"`
	OnlyA   int `json:"only_a"`
	OnlyB   int `json:"only_b"`
}

// CompareTargetsResult is the comparison of two targets' deploy locks.
type CompareTargetsResult struct {
	A       TargetSide   `json:"a"`
	B       TargetSide   `json:"b"`
	Modules []TargetRow  `json:"modules"`
	Counts  TargetCounts `json:"counts"`
	Copy    bool         `json:"-"`
	JSON    bool         `json:"-"`
}

// Failed reports whether either lock could not be read or parsed.
func (r CompareTargetsResult) Failed() bool {
	return r.A.Failed() || r.B.Failed()
}

// TargetCell renders a side's cell: the entry's label and version, `unv.`
// while Odoo has not run -u on it, `none` without an entry.
func TargetCell(side TargetSide, e *LockModule) string {
	if side.Failed() {
		return side.LockState
	}
	if e == nil {
		return "none"
	}
	cell := e.label()
	if e.Version != "" {
		cell += " " + e.Version
	}
	if !e.Verified && e.Source != lockSourceBase {
		cell += " unv."
	}
	return cell
}

// identityFn returns a lock entry's content identity (a git tree id, "" when
// it cannot be derived). present is false only when the entry's sha is in the
// local repository and the module is not in it.
type identityFn func(module string, e LockModule) (tree string, present bool)

// RunCompareTargets reads the deploy lock of two connect targets, one `cat`
// over SSH each, and compares them module by module. Read-only, never
// stage-gated, no server profile read. A side whose lock cannot be read or
// parsed logs an ERROR; the result still carries the other side.
func RunCompareTargets(ctx context.Context, opts CompareTargetsOpts) (CompareTargetsResult, error) {
	p, err := parseCompareArgs(opts.Args)
	if err != nil {
		return CompareTargetsResult{}, err
	}
	sides := make([]TargetSide, 2)
	for i, name := range p.targets {
		host, remotePath, err := resolvePullRemote(opts.Cfg, name)
		if err != nil {
			return CompareTargetsResult{}, fmt.Errorf("%w: %v", ErrUsage, err)
		}
		sides[i] = TargetSide{Name: name, Host: host, Path: remotePath}
	}
	if sides[0].Host == sides[1].Host && sides[0].Path == sides[1].Path {
		return CompareTargetsResult{}, fmt.Errorf("%w: %s and %s share one deploy lock (%s:%s)",
			ErrUsage, sides[0].Name, sides[1].Name, sides[0].Host, sides[0].Path)
	}

	for i := range sides {
		sides[i] = readTargetSide(ctx, sides[i])
		logTargetSide(opts, sides[i])
	}

	identity := func(module string, e LockModule) (string, bool) {
		return lockIdentity(ctx, opts.Cfg, opts.Root, module, e)
	}
	res := CompareTargetsResult{A: sides[0], B: sides[1], Copy: p.copy, JSON: p.json}
	res.Modules = diffLocks(res.A, res.B, p.modules, identity)
	res.Counts = countTargetRows(res.Modules)
	return res, nil
}

func readTargetSide(ctx context.Context, side TargetSide) TargetSide {
	rsc := remoteShellContext{sshHost: side.Host, remotePath: side.Path, fromName: side.Name}
	raw, state, err := fetchDeployLock(ctx, rsc)
	if state == lockFound {
		side.lock, state, err = parseDeployLock(raw)
	}
	side.state, side.LockState = state, state.String()
	if err != nil {
		side.Error = err.Error()
	}
	side.Base = side.lock.Base
	return side
}

func logTargetSide(opts CompareTargetsOpts, side TargetSide) {
	target := [2]string{"target", side.Name}
	switch side.state {
	case lockAbsent:
		opts.log("INFO", "targets", "no deploy lock", "", target)
	case lockUnreadable:
		opts.log("ERROR", "targets", "could not read the deploy lock", "", target, [2]string{"reason", side.Error})
	case lockCorrupt:
		opts.log("ERROR", "targets", "deploy lock is corrupt", "", target, [2]string{"reason", side.Error})
	default:
		fields := append([][2]string{target}, lockSummaryFields(side.lock)...)
		if b := side.lock.Base; b != nil {
			fields = append(fields, [2]string{"base", b.Branch + "@" + shortSHA(b.SHA)})
		}
		opts.log("INFO", "targets", "lock", "", fields...)
	}
}

// diffLocks compares two sides module by module: the named modules when
// given, else the union of both locks, sorted. Both sides failing yields no
// rows.
func diffLocks(a, b TargetSide, named []string, identity identityFn) []TargetRow {
	if a.Failed() && b.Failed() {
		return []TargetRow{}
	}
	names := named
	if len(names) == 0 {
		seen := map[string]bool{}
		for _, side := range []TargetSide{a, b} {
			for name := range side.lock.Modules {
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
		}
		sort.Strings(names)
	}
	rows := make([]TargetRow, 0, len(names))
	for _, name := range names {
		ea, ta := sideCell(a, name, identity)
		eb, tb := sideCell(b, name, identity)
		row := TargetRow{Name: name, A: ea, B: eb, Named: len(named) > 0}
		row.Status, row.Only, row.Reason = targetStatus(a, b, ea, eb, ta, tb)
		row.Newer = newerSide(a.Name, b.Name, ea, eb)
		rows = append(rows, row)
	}
	return rows
}

// sideCell is the side's entry for a module and its identity. A git-deploy
// side without an entry falls back to its lock base, unless the module is
// not in the base commit at all.
func sideCell(side TargetSide, name string, identity identityFn) (*LockModule, string) {
	if side.Failed() {
		return nil, ""
	}
	if e, ok := side.lock.Modules[name]; ok {
		tree, _ := identity(name, e)
		return &e, tree
	}
	if b := side.lock.Base; b != nil {
		e := LockModule{Source: lockSourceBase, SHA: b.SHA, At: b.At}
		if tree, present := identity(name, e); present {
			return &e, tree
		}
	}
	return nil, ""
}

func targetStatus(a, b TargetSide, ea, eb *LockModule, ta, tb string) (status, only, reason string) {
	switch {
	case a.Failed() || b.Failed():
		return targetUnknown, "", reasonUnreadable
	case ea == nil && eb == nil:
		return targetUnknown, "", reasonNoIdentity
	case eb == nil:
		return targetOnly, a.Name, ""
	case ea == nil:
		return targetOnly, b.Name, ""
	case ea.Dirty || eb.Dirty:
		return targetUnknown, "", reasonDirty
	case ta != "" && tb != "":
		if ta == tb {
			return targetSame, "", ""
		}
		return targetDiffers, "", ""
	case ea.SHA != "" && ea.SHA == eb.SHA:
		return targetSame, "", ""
	}
	return targetUnknown, "", reasonNoIdentity
}

// newerSide names the side whose manifest version is higher; empty when the
// versions are equal or either is missing.
func newerSide(nameA, nameB string, ea, eb *LockModule) string {
	if ea == nil || eb == nil || ea.Version == "" || eb.Version == "" {
		return ""
	}
	switch compareVersions(ea.Version, eb.Version) {
	case 1:
		return nameA
	case -1:
		return nameB
	}
	return ""
}

func countTargetRows(rows []TargetRow) TargetCounts {
	var c TargetCounts
	for _, r := range rows {
		switch {
		case r.Status == targetSame:
			c.Same++
		case r.Status == targetDiffers:
			c.Differs++
		case r.Status == targetOnly && r.A != nil:
			c.OnlyA++
		case r.Status == targetOnly:
			c.OnlyB++
		default:
			c.Unknown++
		}
	}
	return c
}

// lockIdentity is an entry's content identity: its recorded tree, or the
// tree of the module at the entry's sha in the local repository. A dirty
// entry has none (its content existed only on someone's disk); neither has an
// entry whose sha is not in the local repository.
func lockIdentity(ctx context.Context, cfg *config.Config, root, module string, e LockModule) (tree string, present bool) {
	switch {
	case e.Dirty:
		return "", true
	case e.Tree != "":
		return e.Tree, true
	case e.SHA == "":
		return "", true
	}
	repoPath, err := moduleRepoPath(cfg, root, module)
	if err != nil {
		return "", true
	}
	if _, err := gitOutput(ctx, root, "rev-parse", "--verify", "--quiet", e.SHA+"^{commit}"); err != nil {
		return "", true
	}
	out, err := gitOutput(ctx, root, "rev-parse", "--verify", "--quiet", e.SHA+":"+repoPath)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}
