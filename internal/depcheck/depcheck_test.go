package depcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSymbols(t *testing.T) {
	root := writeTree(t, map[string]string{
		"models/crm.py": `from odoo import fields, models

def module_helper(x):
    return x


class CrmLead(models.Model):
    _inherit = "crm.lead"

    # a comment at body indent
    promo_id = fields.Many2one("sale.promotion")
    note = fields.Text(
        string="Note",
    )

    @api.depends("promo_id")
    def _get_promotion_from_sale_order(self, order):
        def nested(y):
            return y
        total = fields.Float()
        return order

    def create(self, vals):
        return super().create(vals)
`,
		"views/crm.xml": `<odoo>
  <record id="view_lead_form" model="ir.ui.view">
    <field name="inherit_id" ref="crm.crm_lead_view_form"/>
  </record>
  <template id='portal_lead'/>
  <menuitem
      id="menu_promo"
      name="Promotions"/>
  <record id="crm.existing_record" model="ir.ui.view"/>
  <field name="x" id="not_a_record"/>
</odoo>`,
		"tests/test_crm.py":           "class TestCrm(TransactionCase):\n    def test_promo(self):\n        pass\n",
		"migrations/18.0.1.1/post.py": "class Migration:\n    def migrate_old(self):\n        pass\n",
		"static/src/js/lead.js":       "function jsOnly() {}",
	})
	got, err := Symbols(root)
	if err != nil {
		t.Fatal(err)
	}
	want := Set{
		{Field, "promo_id"}:                        true,
		{Field, "note"}:                            true,
		{Method, "_get_promotion_from_sale_order"}: true,
		{Method, "create"}:                         true,
		{XMLID, "view_lead_form"}:                  true,
		{XMLID, "portal_lead"}:                     true,
		{XMLID, "menu_promo"}:                      true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Symbols = %v\nwant      %v", got, want)
	}
}

func TestRemoved(t *testing.T) {
	old := Set{
		{Method, "_get_promotion_from_sale_order"}: true,
		{Method, "_moved_to_sibling"}:              true,
		{Method, "create"}:                         true,
		{Method, "__init__"}:                       true,
		{Method, "action_archive"}:                 true,
		{Field, "promo_id"}:                        true,
		{Field, "kept"}:                            true,
		{XMLID, "view_lead_form"}:                  true,
		{XMLID, "view_moved"}:                      true,
	}
	next := Set{{Field, "kept"}: true}
	keep := Set{{Method, "_moved_to_sibling"}: true, {XMLID, "view_moved"}: true}
	got := Removed(old, next, keep)
	want := []Symbol{
		{Field, "promo_id"},
		{Method, "_get_promotion_from_sale_order"},
		{XMLID, "view_lead_form"},
		{XMLID, "view_moved"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Removed = %v\nwant      %v", got, want)
	}
	if got := Removed(next, old, nil); got != nil {
		t.Errorf("nothing removed, got %v", got)
	}
}

func TestGrepPattern(t *testing.T) {
	for _, tc := range []struct {
		removed []Symbol
		want    string
	}{
		{[]Symbol{{Method, "_get_x"}}, "_get_x"},
		{[]Symbol{{XMLID, "view_form"}}, `ccima_crm\.view_form`},
		{[]Symbol{{Method, "_get_x"}, {Field, "promo_id"}, {XMLID, "menu"}}, `(_get_x|promo_id|ccima_crm\.menu)`},
	} {
		if got := GrepPattern(tc.removed, "ccima_crm"); got != tc.want {
			t.Errorf("GrepPattern(%v) = %q, want %q", tc.removed, got, tc.want)
		}
	}
}

func TestDefines(t *testing.T) {
	method := Symbol{Method, "_get_x"}
	field := Symbol{Field, "promo_id"}
	for _, tc := range []struct {
		s    Symbol
		line string
		want bool
	}{
		{method, "    def _get_x(self):", true},
		{method, "        return self._get_x()", false},
		{method, "    def _get_x_other(self):", false},
		{field, "    promo_id = fields.Many2one('x')", true},
		{field, "        rec.promo_id = False", false},
		{Symbol{XMLID, "view"}, `<record id="view">`, false},
	} {
		if got := Defines(tc.s, tc.line); got != tc.want {
			t.Errorf("Defines(%v, %q) = %v, want %v", tc.s, tc.line, got, tc.want)
		}
	}
}

func TestParseGrep(t *testing.T) {
	roots := map[string]string{
		"/srv/addons/ccima_flow_mail": "ccima_flow_mail",
		"/srv/addons/sale":            "sale",
	}
	out := "/srv/addons/ccima_flow_mail/models/mail_flow.py:212:        promo = lead._get_x(order)\n" +
		"/srv/addons/sale_stock/models/x.py:3:_get_x\n" +
		"/srv/addons/sale/views/a.xml:7:<field ref=\"a:b\"/>\n" +
		"garbage\n" +
		"/srv/addons/sale/broken.py:notaline:x\n"
	got := ParseGrep(out, roots)
	want := []Ref{
		{Module: "ccima_flow_mail", File: "ccima_flow_mail/models/mail_flow.py", Line: 212, Text: "        promo = lead._get_x(order)"},
		{Module: "sale", File: "sale/views/a.xml", Line: 7, Text: `<field ref="a:b"/>`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseGrep = %+v\nwant        %+v", got, want)
	}
}
