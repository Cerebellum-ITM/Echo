package cmd

import (
	"strings"
	"testing"
)

// TestParseCompareArgs pins the flag/positional split, especially that the
// remote-mode switches are consumed and never leak into the module name.
func TestParseCompareArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		module  string
		copy    bool
		from    string
		remote  bool
		wantErr bool
	}{
		{"empty", nil, "", false, "", false, false},
		{"module only", []string{"sale"}, "sale", false, "", false, false},
		{"copy", []string{"sale", "--copy"}, "sale", true, "", false, false},
		{"from with value", []string{"sale", "--from", "prod"}, "sale", false, "prod", false, false},
		{"from-value not a module", []string{"--from", "prod"}, "", false, "prod", false, false},
		{"from equals copy", []string{"sale", "--from=prod", "--copy"}, "sale", true, "prod", false, false},
		{"bare remote", []string{"--remote", "sale"}, "sale", false, "", true, false},
		{"unknown flag", []string{"sale", "--nope"}, "", false, "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			module, copyFlag, from, remote, err := parseCompareArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseCompareArgs(%v) err = nil, want error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCompareArgs(%v) err = %v", tc.args, err)
			}
			if module != tc.module || copyFlag != tc.copy || from != tc.from || remote != tc.remote {
				t.Errorf("parseCompareArgs(%v) = (%q, %v, %q, %v); want (%q, %v, %q, %v)",
					tc.args, module, copyFlag, from, remote,
					tc.module, tc.copy, tc.from, tc.remote)
			}
		})
	}
}

// TestUnifiedDiffIdentical: identical content produces an empty diff.
func TestUnifiedDiffIdentical(t *testing.T) {
	const s = "line one\nline two\n"
	if got := unifiedDiff(s, s, "docker/sale/x.py", "local/sale/x.py"); got != "" {
		t.Fatalf("unifiedDiff(identical) = %q, want empty", got)
	}
}

// TestUnifiedDiffChange: a changed line surfaces both labels, a `-` old
// line and a `+` new line.
func TestUnifiedDiffChange(t *testing.T) {
	old := "a = 1\nb = 2\n"
	newer := "a = 1\nb = 3\n"
	got := unifiedDiff(old, newer, "prod/sale/models/x.py", "local/sale/models/x.py")
	for _, want := range []string{
		"--- prod/sale/models/x.py",
		"+++ local/sale/models/x.py",
		"-b = 2",
		"+b = 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unifiedDiff missing %q in:\n%s", want, got)
		}
	}
}

// TestUnifiedDiffMissingContainer: an empty container side renders the
// whole local file as added.
func TestUnifiedDiffMissingContainer(t *testing.T) {
	got := unifiedDiff("", "new file\ncontent\n", "docker/sale/new.py", "local/sale/new.py")
	if !strings.Contains(got, "+new file") || !strings.Contains(got, "+content") {
		t.Errorf("unifiedDiff(empty container) should render all `+`, got:\n%s", got)
	}
	if strings.Contains(got, "\n-new file") {
		t.Errorf("unexpected `-` line for an added file:\n%s", got)
	}
}
