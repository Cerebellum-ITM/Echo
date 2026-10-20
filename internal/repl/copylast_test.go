package repl

import "testing"

func TestAutoCopyEnabled(t *testing.T) {
	cases := []struct {
		name        string
		noClipboard string
		stdinTTY    bool
		stdoutTTY   bool
		want        bool
	}{
		{"terminal", "", true, true, true},
		{"stdin piped", "", false, true, false},
		{"stdout piped", "", true, false, false},
		{"fully headless", "", false, false, false},
		{"opted out on a terminal", "1", true, true, false},
		{"other values do not opt out", "0", true, true, true},
	}
	origIn, origOut := stdinIsTTY, stdoutIsTTY
	t.Cleanup(func() { stdinIsTTY, stdoutIsTTY = origIn, origOut })
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ECHO_NO_CLIPBOARD", c.noClipboard)
			stdinIsTTY = func() bool { return c.stdinTTY }
			stdoutIsTTY = func() bool { return c.stdoutTTY }
			if got := autoCopyEnabled(); got != c.want {
				t.Errorf("autoCopyEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAutoCopySkippedHeadlessHasNoCopiedField(t *testing.T) {
	origIn, origOut := stdinIsTTY, stdoutIsTTY
	t.Cleanup(func() { stdinIsTTY, stdoutIsTTY = origIn, origOut })
	stdinIsTTY = func() bool { return false }
	stdoutIsTTY = func() bool { return false }

	fields, err := autoCopy("payload")
	if err != nil || len(fields) != 0 {
		t.Fatalf("autoCopy headless = %v, %v; want no field and no error", fields, err)
	}
}
