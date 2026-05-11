// Package clipboard writes text to the system clipboard.
//
// It tries the appropriate command for the host OS and, on Linux, falls
// back across the common clipboard utilities. Returns ErrUnavailable
// when no helper binary is found in PATH.
package clipboard

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var ErrUnavailable = errors.New("no clipboard helper found")

// helper describes how to invoke one clipboard tool: command + args.
type helper struct {
	bin  string
	args []string
}

// helpersFor returns the prioritised list of helpers for the current OS.
func helpersFor() []helper {
	switch runtime.GOOS {
	case "darwin":
		return []helper{{"pbcopy", nil}}
	case "linux":
		return []helper{
			{"wl-copy", nil},                            // Wayland
			{"xclip", []string{"-selection", "clipboard"}}, // X11 (xclip)
			{"xsel", []string{"--clipboard", "--input"}},   // X11 (xsel)
		}
	case "windows":
		return []helper{{"clip", nil}}
	}
	return nil
}

// WriteAll copies text to the system clipboard. Tries the OS-specific
// helpers in order; on Unix systems falls back to OSC 52 escape, which
// modern terminals (iTerm2, kitty, alacritty, wezterm, Ghostty, foot,
// ...) forward to the host clipboard. Returns ErrUnavailable if no
// route works.
func WriteAll(text string) error {
	helpers := helpersFor()

	for _, h := range helpers {
		if _, err := exec.LookPath(h.bin); err != nil {
			continue
		}
		cmd := exec.Command(h.bin, h.args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	// OSC 52 fallback — only useful for terminal-controlled clipboards.
	if runtime.GOOS != "windows" {
		if err := writeOSC52(text); err == nil {
			return nil
		}
	}

	return fmt.Errorf("%w (install pbcopy, wl-copy, xclip, or xsel — or use a terminal with OSC 52)", ErrUnavailable)
}

// writeOSC52 emits the OSC 52 escape sequence to the controlling
// terminal. Modern terminals decode the base64 payload and place it in
// the host clipboard.
func writeOSC52(text string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", encoded)

	if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer f.Close()
		_, err := f.WriteString(seq)
		return err
	}
	_, err := os.Stderr.WriteString(seq)
	return err
}
