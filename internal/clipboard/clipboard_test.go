package clipboard

import "testing"

func TestIsRemote(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"no remote env", map[string]string{}, false},
		{"SSH_TTY set", map[string]string{"SSH_TTY": "/dev/pts/0"}, true},
		{"SSH_CONNECTION set", map[string]string{"SSH_CONNECTION": "1.2.3.4 22 5.6.7.8 22"}, true},
		{"TMUX set", map[string]string{"TMUX": "/tmp/tmux-1000/default,1234,0"}, true},
		{"empty SSH_TTY", map[string]string{"SSH_TTY": ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SSH_TTY", "")
			t.Setenv("SSH_CONNECTION", "")
			t.Setenv("TMUX", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := isRemote(); got != tc.want {
				t.Fatalf("isRemote() = %v, want %v", got, tc.want)
			}
		})
	}
}
