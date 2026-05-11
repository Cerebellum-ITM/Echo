package repl

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const (
	historyFileName = "history"
	historyMaxLen   = 1000
)

// historyPath returns ~/.config/echo/history.
func historyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "echo", historyFileName), nil
}

func loadHistory() []string {
	p, err := historyPath()
	if err != nil {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// appendHistory adds line to history, deduping consecutive duplicates and
// capping at historyMaxLen. Persists to disk.
func appendHistory(history []string, line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return history
	}
	if n := len(history); n > 0 && history[n-1] == line {
		return history
	}
	history = append(history, line)
	if len(history) > historyMaxLen {
		history = history[len(history)-historyMaxLen:]
	}
	saveHistory(history)
	return history
}

func saveHistory(history []string) {
	p, err := historyPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	f, err := os.Create(p)
	if err != nil {
		return
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, line := range history {
		w.WriteString(line)
		w.WriteByte('\n')
	}
	w.Flush()
}
