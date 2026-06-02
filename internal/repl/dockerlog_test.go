package repl

import "testing"

func TestParseComposeProgress(t *testing.T) {
	tests := []struct {
		name string
		line string
		want composeLine
		ok   bool
	}{
		{
			name: "container restarting is transitional DEBUG",
			line: " Container dvz_ny_odoo_19-db-1  Restarting",
			want: composeLine{resource: "container", name: "dvz_ny_odoo_19-db-1", state: "restarting", level: "DEBUG"},
			ok:   true,
		},
		{
			name: "container started is terminal INFO",
			line: " Container dvz_ny_odoo_19-db-1  Started",
			want: composeLine{resource: "container", name: "dvz_ny_odoo_19-db-1", state: "started", level: "INFO"},
			ok:   true,
		},
		{
			name: "network created",
			line: " Network dvz_ny_odoo_19_default  Created",
			want: composeLine{resource: "network", name: "dvz_ny_odoo_19_default", state: "created", level: "INFO"},
			ok:   true,
		},
		{
			name: "volume removed",
			line: " Volume foo  Removed",
			want: composeLine{resource: "volume", name: "foo", state: "removed", level: "INFO"},
			ok:   true,
		},
		{
			name: "image pulling is transitional DEBUG",
			line: " Image odoo:19  Pulling",
			want: composeLine{resource: "image", name: "odoo:19", state: "pulling", level: "DEBUG"},
			ok:   true,
		},
		{
			name: "compose error maps to ERROR",
			line: " Container dvz_ny_odoo_19-db-1  Error",
			want: composeLine{resource: "container", name: "dvz_ny_odoo_19-db-1", state: "error", level: "ERROR"},
			ok:   true,
		},
		{
			name: "real odoo log line is not captured",
			line: "2026-06-02 18:34:47,606 3675802 INFO develop odoo.modules.loading: loading 1 modules",
			ok:   false,
		},
		{
			name: "ps table row is not captured",
			line: "NAME                    IMAGE       STATUS",
			ok:   false,
		},
		{
			name: "blank line is not captured",
			line: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseComposeProgress(tt.line)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
