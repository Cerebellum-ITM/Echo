package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pascualchavez/echo/internal/config"
)

const (
	leadWithPromo = `from odoo import fields, models


class CrmLead(models.Model):
    _inherit = "crm.lead"

    def _get_promotion_from_sale_order(self, order):
        return order.promotion_id

    def create(self, vals):
        return super().create(vals)
`
	leadWithoutPromo = `from odoo import fields, models


class CrmLead(models.Model):
    _inherit = "crm.lead"
`
	mailFlowCaller = `from odoo import models


class MailFlow(models.Model):
    _name = "mail.flow"

    def _notify(self, lead, order):
        promo = lead._get_promotion_from_sale_order(order)
        self.env["crm.lead"].create({"name": promo.name})
        return self.env.ref("ccima_crm_reassign.view_promo")
`
	promoView = `<odoo>
  <record id="view_promo" model="ir.ui.view">
    <field name="name">promo</field>
  </record>
</odoo>
`
)

// depLog records every deploy log line as "LEVEL sub: msg k=v ...".
type depLog struct{ lines []string }

func (l *depLog) log(level, sub, msg, db string, fields ...[2]string) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s", level, sub, msg)
	for _, f := range fields {
		fmt.Fprintf(&b, " %s=%s", f[0], f[1])
	}
	l.lines = append(l.lines, b.String())
}

func (l *depLog) find(prefix string) string {
	for _, line := range l.lines {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// depFixture is the 2026-09-30 incident: the server runs ccima_crm_reassign
// with _get_promotion_from_sale_order, ccima_flow_mail (staying) calls it,
// and the local commit `without` drops it.
type depFixture struct {
	remote  *fakeRemote
	repo    *sourceRepo
	without string
	server  map[string]string
}

func newDepFixture(t *testing.T) *depFixture {
	t.Helper()
	remote := newFakeRemote(t)
	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = origTTY })

	r := newSourceRepo(t)
	r.write("addons/ccima_crm_reassign/__manifest__.py", "{'name': 'reassign', 'version': '18.0.1.0'}")
	r.write("addons/ccima_crm_reassign/models/lead.py", leadWithPromo)
	r.commit("[ADD] ccima_crm_reassign: base")
	r.write("addons/ccima_crm_reassign/models/lead.py", leadWithoutPromo)
	without := r.commit("[REF] ccima_crm_reassign: drop the promotion lookup")

	server := map[string]string{
		"addons/ccima_crm_reassign/__manifest__.py":   "{'name': 'reassign', 'version': '18.0.1.0'}",
		"addons/ccima_crm_reassign/models/lead.py":    leadWithPromo,
		"addons/ccima_flow_mail/__manifest__.py":      "{'name': 'flow mail'}",
		"addons/ccima_flow_mail/models/mail_flow.py":  mailFlowCaller,
		"addons/ccima_flow_mail/static/description.x": "not searched",
	}
	for rel, content := range server {
		mustWrite(t, filepath.Join(remote.dir, rel), content)
	}
	return &depFixture{remote: remote, repo: r, without: without, server: server}
}

func (f *depFixture) setStage(t *testing.T, stage string) {
	t.Helper()
	mustWrite(t, filepath.Join(os.Getenv("HOME"), ".config/echo/projects", config.ProjectKey(f.remote.dir)+".toml"),
		"stage = \""+stage+"\"\ndb_name = \"stg\"\nodoo_version = \"18\"\n")
}

func (f *depFixture) deploy(t *testing.T, args ...string) (DeployResult, *depLog, error) {
	t.Helper()
	logs := &depLog{}
	args = append(args, "--from", "stg", "--push", "--no-lint", "--no-checkpoint")
	res, err := RunDeploy(context.Background(), DeployOpts{Cfg: f.remote.cfg(), Root: f.repo.root, Args: args, Log: logs.log})
	return res, logs, err
}

func TestDeployDependencyCheckIncident(t *testing.T) {
	f := newDepFixture(t)
	res, logs, err := f.deploy(t, "--modules", "ccima_crm_reassign@"+f.without[:7], "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	want := "WARNING plan: dependency removed=_get_promotion_from_sale_order kind=method from=ccima_crm_reassign " +
		"used_by=ccima_flow_mail at=ccima_flow_mail/models/mail_flow.py:8"
	if got := logs.find("WARNING plan: dependency removed=_get_promotion"); got != want {
		t.Errorf("warning = %q\nwant      %q\nlog:\n%s", got, want, strings.Join(logs.lines, "\n"))
	}
	wantFindings := []DependencyFinding{{
		Module: "ccima_crm_reassign", Symbol: "_get_promotion_from_sale_order", Kind: "method",
		UsedBy: "ccima_flow_mail", File: "ccima_flow_mail/models/mail_flow.py", Line: 8,
	}}
	if !reflect.DeepEqual(res.Dependencies, wantFindings) {
		t.Errorf("Dependencies = %+v", res.Dependencies)
	}
	if strings.Contains(strings.Join(logs.lines, "\n"), "removed=create") {
		t.Error("a dropped create override was reported")
	}
}

func TestDeployDependencyCheckBlocking(t *testing.T) {
	t.Run("staging without a TTY fails closed", func(t *testing.T) {
		f := newDepFixture(t)
		f.setStage(t, "staging")
		_, _, err := f.deploy(t, "--modules", "ccima_crm_reassign@"+f.without)
		if !errors.Is(err, ErrNonInteractive) || !strings.Contains(err.Error(), "still use") {
			t.Fatalf("err = %v, want the dependency gate's ErrNonInteractive", err)
		}
		if got := treeFiles(t, f.remote.dir); !reflect.DeepEqual(got, f.server) || f.remote.composeLog() != "" {
			t.Error("a blocked deploy changed the server")
		}
	})

	t.Run("staging with --force proceeds", func(t *testing.T) {
		f := newDepFixture(t)
		f.setStage(t, "staging")
		if _, _, err := f.deploy(t, "--modules", "ccima_crm_reassign@"+f.without, "--force"); err != nil {
			t.Fatalf("deploy: %v", err)
		}
		if got := treeFiles(t, filepath.Join(f.remote.dir, "addons/ccima_crm_reassign")); got["models/lead.py"] != leadWithoutPromo {
			t.Error("the forced deploy did not ship")
		}
	})

	t.Run("dev warns and proceeds", func(t *testing.T) {
		f := newDepFixture(t)
		_, logs, err := f.deploy(t, "--modules", "ccima_crm_reassign@"+f.without)
		if err != nil {
			t.Fatalf("deploy: %v", err)
		}
		if logs.find("WARNING plan: dependency removed=") == "" {
			t.Error("no dependency warning on dev")
		}
	})
}

func TestDeployDependencyCheckMovedMethod(t *testing.T) {
	f := newDepFixture(t)
	f.repo.write("addons/ccima_promo/__manifest__.py", "{'name': 'promo'}")
	f.repo.write("addons/ccima_promo/models/lead.py", leadWithPromo)
	moved := f.repo.commit("[MOV] ccima_promo: take the promotion lookup")

	res, logs, err := f.deploy(t, "--modules", "ccima_crm_reassign,ccima_promo", "--at", moved, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(res.Dependencies) != 0 {
		t.Errorf("a method moved to another shipped module was reported: %+v", res.Dependencies)
	}
	if got := logs.find("INFO plan: dependency check clean"); got != "INFO plan: dependency check clean modules=1 removed=0" {
		t.Errorf("clean line = %q", got)
	}
}

func TestDeployDependencyCheckXMLID(t *testing.T) {
	f := newDepFixture(t)
	mustWrite(t, filepath.Join(f.remote.dir, "addons/ccima_crm_reassign/views/promo.xml"), promoView)
	createOverride := "    def create(self, vals):\n        return super().create(vals)\n"
	f.repo.write("addons/ccima_crm_reassign/models/lead.py", strings.Replace(leadWithPromo, createOverride, "", 1))
	sha := f.repo.commit("[REM] ccima_crm_reassign: drop the create override")

	res, _, err := f.deploy(t, "--modules", "ccima_crm_reassign@"+sha, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	want := []DependencyFinding{{
		Module: "ccima_crm_reassign", Symbol: "ccima_crm_reassign.view_promo", Kind: "xmlid",
		UsedBy: "ccima_flow_mail", File: "ccima_flow_mail/models/mail_flow.py", Line: 10,
	}}
	if !reflect.DeepEqual(res.Dependencies, want) {
		t.Errorf("Dependencies = %+v\nwant %+v", res.Dependencies, want)
	}
}

func TestDeployDependencyCheckInstallAndOptOut(t *testing.T) {
	f := newDepFixture(t)
	f.repo.write("addons/ccima_new/__manifest__.py", "{'name': 'new'}")
	f.repo.write("addons/ccima_new/models/x.py", leadWithoutPromo)
	sha := f.repo.commit("[ADD] ccima_new: first version")

	res, logs, err := f.deploy(t, "--modules", "ccima_new@"+sha, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(res.Dependencies) != 0 || logs.find("INFO plan: dependency check clean modules=0 removed=0") == "" {
		t.Errorf("an installing module: %+v\n%s", res.Dependencies, strings.Join(logs.lines, "\n"))
	}

	calls := 0
	orig := depRunSSH
	depRunSSH = func(ctx context.Context, host, remoteCmd string, stdin []byte) ([]byte, error) {
		calls++
		return orig(ctx, host, remoteCmd, stdin)
	}
	t.Cleanup(func() { depRunSSH = orig })
	res, logs, err = f.deploy(t, "--modules", "ccima_crm_reassign@"+f.without, "--dry-run", "--no-dep-check")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if calls != 0 || len(res.Dependencies) != 0 {
		t.Errorf("--no-dep-check ran the check: calls=%d findings=%+v", calls, res.Dependencies)
	}
	if logs.find("WARNING plan: dependency check skipped flag=--no-dep-check") == "" {
		t.Errorf("the skip was not logged:\n%s", strings.Join(logs.lines, "\n"))
	}
}

func TestDeployDependencyCheckTransportFailureWarns(t *testing.T) {
	f := newDepFixture(t)
	orig := depRunSSH
	depRunSSH = func(context.Context, string, string, []byte) ([]byte, error) {
		return nil, errors.New("connection reset")
	}
	t.Cleanup(func() { depRunSSH = orig })
	res, logs, err := f.deploy(t, "--modules", "ccima_crm_reassign@"+f.without, "--dry-run")
	if err != nil {
		t.Fatalf("a check that cannot run must not fail the deploy: %v", err)
	}
	if len(res.Dependencies) != 0 || !strings.Contains(logs.find("WARNING plan: dependency check skipped"), "connection reset") {
		t.Errorf("findings=%+v\n%s", res.Dependencies, strings.Join(logs.lines, "\n"))
	}
}

func TestDeployDependencyCheckGitBranch(t *testing.T) {
	f := newDepFixture(t)
	if err := os.RemoveAll(f.remote.dir); err != nil {
		t.Fatal(err)
	}
	base := f.repo.git("rev-list", "--max-parents=0", "HEAD")
	if out, err := exec.Command("git", "clone", "-q", f.repo.root, f.remote.dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	srv := &sourceRepo{t: t, root: f.remote.dir}
	srv.git("reset", "-q", "--hard", base)
	mustWrite(t, filepath.Join(f.remote.dir, "addons/ccima_flow_mail/__manifest__.py"), "{'name': 'flow mail'}")
	mustWrite(t, filepath.Join(f.remote.dir, "addons/ccima_flow_mail/models/mail_flow.py"), mailFlowCaller)

	cfg := f.remote.cfg()
	cfg.ConnectTargets[0].GitDeploy = true
	logs := &depLog{}
	args := []string{"--commits", f.without, "--from", "stg", "--push", "--no-lint", "--dry-run"}
	res, err := RunDeploy(context.Background(), DeployOpts{Cfg: cfg, Root: f.repo.root, Args: args, Log: logs.log})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Symbol != "_get_promotion_from_sale_order" {
		t.Errorf("a module riding the branch: %+v\n%s", res.Dependencies, strings.Join(logs.lines, "\n"))
	}
}
