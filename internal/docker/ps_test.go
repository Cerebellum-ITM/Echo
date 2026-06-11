package docker

import "testing"

func TestParsePSJSONArray(t *testing.T) {
	in := []byte(`[{"Name":"p-odoo-1","Service":"odoo","Image":"odoo:19.0","State":"running","Status":"Up 2 hours (healthy)","Health":"healthy","Publishers":[{"URL":"0.0.0.0","TargetPort":8069,"PublishedPort":8069,"Protocol":"tcp"}]},
	{"Name":"p-db-1","Service":"db","Image":"postgres:16","State":"running","Status":"Up 2 hours","Publishers":[{"TargetPort":5432,"PublishedPort":0,"Protocol":"tcp"}]}]`)
	rows, err := parsePSJSON(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Service != "odoo" || rows[0].Health != "healthy" {
		t.Errorf("row0 = %+v", rows[0])
	}
	if got := rows[0].Ports(); got != "8069→8069" {
		t.Errorf("odoo ports = %q, want 8069→8069", got)
	}
	if got := rows[1].Ports(); got != "" {
		t.Errorf("db ports = %q, want empty (unpublished)", got)
	}
}

func TestParsePSJSONNDJSON(t *testing.T) {
	in := []byte("{\"Service\":\"odoo\",\"State\":\"exited\",\"Status\":\"Exited (1)\"}\n{\"Service\":\"db\",\"State\":\"running\",\"Status\":\"Up\"}\n")
	rows, err := parsePSJSON(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 2 || rows[0].State != "exited" || rows[1].Service != "db" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestParsePSJSONEmpty(t *testing.T) {
	rows, err := parsePSJSON([]byte("  \n"))
	if err != nil || rows != nil {
		t.Errorf("empty: rows=%v err=%v, want nil,nil", rows, err)
	}
}

func TestPSPortsNonTCPAndDedup(t *testing.T) {
	c := PSContainer{Publishers: []PSPublisher{
		{TargetPort: 8069, PublishedPort: 8069, Protocol: "tcp"},
		{TargetPort: 8069, PublishedPort: 8069, Protocol: "tcp"},
		{TargetPort: 69, PublishedPort: 69, Protocol: "udp"},
	}}
	if got := c.Ports(); got != "8069→8069, 69→69/udp" {
		t.Errorf("ports = %q", got)
	}
}
