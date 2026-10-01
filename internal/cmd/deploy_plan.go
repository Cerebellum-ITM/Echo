package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/project"
)

// ErrPlanStale is returned by `deploy --apply` when the saved plan no longer
// matches what the run would do; nothing was written on the server.
var ErrPlanStale = errors.New("plan is stale")

const planSchema = 1

// DeployPlan is what `deploy --dry-run --save-plan` resolved, so a later
// `deploy --apply` runs exactly that or refuses (Unit 129). It holds no
// secret: deploy actions appear by name and digest, never their run text.
type DeployPlan struct {
	Schema        int              `json:"schema"`
	CreatedAt     time.Time        `json:"created_at"`
	EchoVersion   string           `json:"echo_version"`
	By            string           `json:"by"`
	Root          string           `json:"root"`
	Target        planTarget       `json:"target"`
	Run           planRun          `json:"run"`
	Commits       []string         `json:"commits"`
	Modules       []planModule     `json:"modules"`
	BranchTip     string           `json:"branch_tip"`
	Dest          string           `json:"dest"`
	TestModules   []string         `json:"test_modules"`
	DeployActions planActions      `json:"deploy_actions"`
	Lock          string           `json:"lock"`
	Dependencies  []planDependency `json:"dependencies"`
}

type planTarget struct {
	Name       string `json:"name"`
	SSHHost    string `json:"ssh_host"`
	RemotePath string `json:"remote_path"`
	DB         string `json:"db"`
	Stage      string `json:"stage"`
}

// planRun holds the effective decisions of the planned run, not the flags
// typed for it: --apply pins each one, so a server-side policy change cannot
// alter what was reviewed.
type planRun struct {
	Push          bool   `json:"push"`
	Git           bool   `json:"git"`
	Checkpoint    string `json:"checkpoint"` // db | dump | off
	Test          bool   `json:"test"`
	I18nOverwrite bool   `json:"i18n_overwrite"`
	Actions       bool   `json:"actions"`
	Lint          bool   `json:"lint"`
	DepCheck      bool   `json:"dep_check"`
	Fetch         string `json:"fetch"` // auto | on | off
}

// planModule is one module of the plan. When the run pushes it carries the
// identity of what ships: sha and tree for committed content, the digest of
// the directory for a working-tree module.
type planModule struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
	SHA    string `json:"sha,omitempty"`
	Tree   string `json:"tree,omitempty"`
	Digest string `json:"digest,omitempty"`
}

type planActions struct {
	Names  []string `json:"names"`
	Digest string   `json:"digest,omitempty"`
}

type planDependency struct {
	Module string `json:"module"`
	Symbol string `json:"symbol"`
	Kind   string `json:"kind"`
	UsedBy string `json:"used_by"`
}

// PlanChange is one difference between a saved plan and the run --apply
// would make now.
type PlanChange struct {
	What    string `json:"what"`
	Module  string `json:"module,omitempty"`
	Planned string `json:"planned"`
	Now     string `json:"now"`
	ref     string
}

func (c PlanChange) fields() [][2]string {
	fields := [][2]string{{"what", c.What}}
	if c.Module != "" {
		fields = append(fields, [2]string{"module", c.Module})
	}
	fields = append(fields, [2]string{"planned", c.Planned}, [2]string{"now", c.Now})
	if c.ref != "" {
		fields = append(fields, [2]string{"ref", c.ref})
	}
	return fields
}

func newDeployPlan(ctx context.Context, root string) DeployPlan {
	by := ""
	if out, err := gitOutput(ctx, root, "config", "user.email"); err == nil {
		by = firstLine(string(out))
	}
	return DeployPlan{
		Schema:      planSchema,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
		EchoVersion: EchoVersion,
		By:          by,
		Root:        root,
	}
}

// writePlan writes the plan through a temp file in the same directory and a
// rename, so a reader never sees half a plan; the file is 0600.
func writePlan(path string, plan DeployPlan) error {
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".echo-plan-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// loadPlan reads a saved plan for this project. Every refusal is ErrUsage:
// it happens before any SSH and is the caller's mistake.
func loadPlan(path, root string) (DeployPlan, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return DeployPlan{}, fmt.Errorf("%w: read the plan: %v", ErrUsage, err)
	}
	var plan DeployPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return DeployPlan{}, fmt.Errorf("%w: %s is not a deploy plan: %v", ErrUsage, path, err)
	}
	if plan.Schema != planSchema {
		return DeployPlan{}, fmt.Errorf("%w: %s has plan schema %d, this Echo reads schema %d", ErrUsage, path, plan.Schema, planSchema)
	}
	if !project.SameDir(plan.Root, root) {
		return DeployPlan{}, fmt.Errorf("%w: %s was planned in %s, not in this project (%s)", ErrUsage, path, plan.Root, root)
	}
	return plan, nil
}

// loadAppliedPlan loads the plan `--apply` names and turns it into the
// arguments of the run it describes, keeping the apply-time flags.
func loadAppliedPlan(opts DeployOpts, p deployArgs) (DeployPlan, deployArgs, error) {
	plan, err := loadPlan(p.apply, opts.Root)
	if err != nil {
		return DeployPlan{}, p, err
	}
	if p.from != "" && p.from != plan.Target.Name {
		return DeployPlan{}, p, fmt.Errorf("%w: %s was planned for target %q, not %q", ErrUsage, p.apply, plan.Target.Name, p.from)
	}
	if plan.EchoVersion != EchoVersion {
		opts.log("WARNING", "plan", "the plan was saved by another Echo version", "",
			[2]string{"planned", plan.EchoVersion}, [2]string{"now", EchoVersion})
	}
	applied, err := parseDeployArgs(planDeployArgs(plan, p))
	if err != nil {
		return DeployPlan{}, p, fmt.Errorf("%s: %w", p.apply, err)
	}
	applied.apply = p.apply
	return plan, applied, nil
}

// planDeployArgs rebuilds the deploy arguments of a plan. A module pinned to
// a ref is passed by its ref, so a ref that moved resolves anew and shows up
// as a change; every run decision is pinned with its per-run flag.
func planDeployArgs(plan DeployPlan, p deployArgs) []string {
	var args []string
	if len(plan.Commits) > 0 {
		args = append(args, "--commits", strings.Join(plan.Commits, ","))
	}
	var modules []string
	for _, m := range plan.Modules {
		switch m.Source {
		case lockSourceWorktree:
			modules = append(modules, m.Name)
		case lockSourceRef:
			modules = append(modules, m.Name+"@"+m.Ref)
		}
	}
	if len(modules) > 0 {
		args = append(args, "--modules", strings.Join(modules, ","))
	}
	if plan.Target.Name != "" {
		args = append(args, "--from", plan.Target.Name)
	}
	pick := func(on bool, yes, no string) string {
		if on {
			return yes
		}
		return no
	}
	run := plan.Run
	args = append(args,
		pick(run.Push, "--push", "--no-push"),
		pick(run.Test, "--test", "--no-test"),
		pick(run.I18nOverwrite, "--i18n", "--no-i18n"),
		pick(run.Checkpoint == "off", "--no-checkpoint", "--checkpoint="+run.Checkpoint))
	for _, off := range []struct {
		on   bool
		flag string
	}{{run.Git, "--no-git"}, {run.Actions, "--no-actions"}, {run.Lint, "--no-lint"}, {run.DepCheck, "--no-dep-check"}} {
		if !off.on {
			args = append(args, off.flag)
		}
	}
	switch run.Fetch {
	case "on":
		args = append(args, "--fetch")
	case "off":
		args = append(args, "--no-fetch")
	}
	if p.force {
		args = append(args, "--force")
	}
	if p.dryRun {
		args = append(args, "--dry-run")
	}
	if p.jsonOut {
		args = append(args, "--json")
	}
	if p.rollbackOnFail != nil {
		args = append(args, pick(*p.rollbackOnFail, "--rollback-on-fail", "--no-rollback-on-fail"))
	}
	return args
}

// planFetch records the fetch policy of the modules pinned to a ref.
func planFetch(p deployArgs) string {
	switch {
	case p.fetch:
		return "on"
	case p.noFetch:
		return "off"
	}
	return "auto"
}

// planModules lists the run's modules, update-set first, with the source each
// ships from. shipped is nil when the run does not push; otherwise each
// module also records the identity of its content.
func planModules(opts DeployOpts, update, install []string, archived map[string]moduleSource, branchMods []string, shipped map[string]LockModule) ([]planModule, error) {
	var out []planModule
	add := func(names []string, action string) error {
		for _, name := range names {
			m := planModule{Name: name, Action: action, Source: lockSourceWorktree}
			if src, ok := archived[name]; ok {
				m.Source, m.Ref = src.kind, src.ref
			} else if slices.Contains(branchMods, name) {
				m.Source = lockSourceBranch
			}
			if e, ok := shipped[name]; ok {
				if m.Source == lockSourceWorktree {
					dir, err := moduleSrcDir(opts.Cfg, opts.Root, name)
					if err != nil {
						return fmt.Errorf("module %s: %w", name, err)
					}
					if m.Digest, err = worktreeDigest(dir); err != nil {
						return fmt.Errorf("digest of %s: %w", name, err)
					}
				} else {
					m.SHA, m.Tree = e.SHA, e.Tree
				}
			}
			out = append(out, m)
		}
		return nil
	}
	if err := add(update, "update"); err != nil {
		return nil, err
	}
	if err := add(install, "install"); err != nil {
		return nil, err
	}
	return out, nil
}

// worktreeDigest identifies a module directory as rsync ships it: every path
// in lexical order with its type, and for a file its executable bit and the
// sha256 of its content, for a symlink its target; rsyncExcludes are left out.
func worktreeDigest(dir string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		if rsyncExcluded(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "l\x00%s\x00%s\x00", rel, target)
		case d.IsDir():
			fmt.Fprintf(h, "d\x00%s\x00", rel)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			content, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "f\x00%s\x00%t\x00%x\x00", rel, info.Mode()&0o111 != 0, sha256.Sum256(content))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// actionsDigest records the run's deploy actions by name, phase and where,
// with one digest over everything that defines them, run text included.
func actionsDigest(actions []config.DeployAction) planActions {
	out := planActions{Names: []string{}}
	if len(actions) == 0 {
		return out
	}
	h := sha256.New()
	for _, a := range actions {
		out.Names = append(out.Names, a.Name+"/"+a.Phase+"/"+a.Where)
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00", a.Name, a.Phase, a.Where, a.ExecPath, a.Run)
	}
	out.Digest = fmt.Sprintf("sha256:%x", h.Sum(nil))
	return out
}

// lockDigest identifies the target's lock as read: the sha256 of its raw
// bytes, or "absent".
func lockDigest(raw []byte, state lockState) string {
	if state == lockAbsent {
		return lockAbsent.String()
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

// planDependencies keeps the dependency findings without file and line: one
// entry per symbol and staying module, sorted.
func planDependencies(findings []DependencyFinding) []planDependency {
	out := []planDependency{}
	for _, f := range findings {
		d := planDependency{Module: f.Module, Symbol: f.Symbol, Kind: f.Kind, UsedBy: f.UsedBy}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Module+"\x00"+a.Symbol+"\x00"+a.UsedBy < b.Module+"\x00"+b.Symbol+"\x00"+b.UsedBy
	})
	return out
}

// diffPlans lists what differs between a saved plan and a fresh one built by
// the same run. Commits, author and timestamps are inputs, not compared.
func diffPlans(saved, fresh DeployPlan) []PlanChange {
	var changes []PlanChange
	differ := func(what, module, planned, now string, show func(string) string) {
		if planned != now {
			changes = append(changes, PlanChange{What: what, Module: module, Planned: show(planned), Now: show(now)})
		}
	}
	plain := func(v string) string { return orNone(v) }
	sha := func(v string) string { return orNone(shortSHA(v)) }
	digest := func(v string) string { return orNone(shortDigest(v)) }

	differ("host", "", saved.Target.SSHHost, fresh.Target.SSHHost, plain)
	differ("path", "", saved.Target.RemotePath, fresh.Target.RemotePath, plain)
	differ("db", "", saved.Target.DB, fresh.Target.DB, plain)
	differ("stage", "", saved.Target.Stage, fresh.Target.Stage, plain)

	savedMods := map[string]planModule{}
	for _, m := range saved.Modules {
		savedMods[m.Name] = m
	}
	freshMods := map[string]planModule{}
	for _, m := range fresh.Modules {
		freshMods[m.Name] = m
	}
	var names []string
	for name := range savedMods {
		names = append(names, name)
	}
	for name := range freshMods {
		if _, ok := savedMods[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		a, inSaved := savedMods[name]
		b, inFresh := freshMods[name]
		if !inSaved || !inFresh {
			differ("module", name, a.Action, b.Action, plain)
			continue
		}
		differ("action", name, a.Action, b.Action, plain)
		if a.Source != b.Source {
			differ("source", name, a.Source, b.Source, plain)
			continue
		}
		if a.SHA != b.SHA {
			c := PlanChange{What: "sha", Module: name, Planned: sha(a.SHA), Now: sha(b.SHA)}
			if a.Source == lockSourceRef {
				c.What, c.ref = "ref", b.Ref
			}
			changes = append(changes, c)
		}
		differ("tree", name, a.Tree, b.Tree, sha)
		differ("disk", name, a.Digest, b.Digest, digest)
	}

	differ("branch_tip", "", saved.BranchTip, fresh.BranchTip, sha)
	differ("dest", "", saved.Dest, fresh.Dest, plain)
	differ("test_modules", "", strings.Join(saved.TestModules, ","), strings.Join(fresh.TestModules, ","), plain)
	if saved.DeployActions.Digest != fresh.DeployActions.Digest {
		planned, now := strings.Join(saved.DeployActions.Names, ","), strings.Join(fresh.DeployActions.Names, ",")
		if planned == now {
			planned, now = shortDigest(saved.DeployActions.Digest), shortDigest(fresh.DeployActions.Digest)
		}
		changes = append(changes, PlanChange{What: "actions", Planned: orNone(planned), Now: orNone(now)})
	}
	differ("lock", "", saved.Lock, fresh.Lock, digest)
	differ("dependencies", "", dependencyLabel(saved.Dependencies), dependencyLabel(fresh.Dependencies), plain)

	s, f := saved.Run, fresh.Run
	for _, r := range []struct{ key, planned, now string }{
		{"push", strconv.FormatBool(s.Push), strconv.FormatBool(f.Push)},
		{"git", strconv.FormatBool(s.Git), strconv.FormatBool(f.Git)},
		{"checkpoint", s.Checkpoint, f.Checkpoint},
		{"test", strconv.FormatBool(s.Test), strconv.FormatBool(f.Test)},
		{"i18n_overwrite", strconv.FormatBool(s.I18nOverwrite), strconv.FormatBool(f.I18nOverwrite)},
		{"actions", strconv.FormatBool(s.Actions), strconv.FormatBool(f.Actions)},
		{"lint", strconv.FormatBool(s.Lint), strconv.FormatBool(f.Lint)},
		{"dep_check", strconv.FormatBool(s.DepCheck), strconv.FormatBool(f.DepCheck)},
		{"fetch", s.Fetch, f.Fetch},
	} {
		differ("run."+r.key, "", r.planned, r.now, plain)
	}
	return changes
}

// planStaleError is the error of a refused --apply.
func planStaleError(path string, saved DeployPlan, changes int) error {
	noun := "changes"
	if changes == 1 {
		noun = "change"
	}
	return fmt.Errorf("%w: %d %s since %s was saved at %s — re-plan with deploy --dry-run --save-plan",
		ErrPlanStale, changes, noun, path, saved.CreatedAt.Format(time.RFC3339))
}

func dependencyLabel(deps []planDependency) string {
	labels := make([]string, len(deps))
	for i, d := range deps {
		labels[i] = d.Module + "." + d.Symbol + ">" + d.UsedBy
	}
	return strings.Join(labels, ",")
}

// shortDigest shortens a "sha256:<hex>" value to seven hex digits.
func shortDigest(d string) string {
	if hex, ok := strings.CutPrefix(d, "sha256:"); ok {
		return shortSHA(hex)
	}
	return d
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}
