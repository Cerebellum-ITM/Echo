package env

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Load reads `.env` from dir and returns its key/value pairs.
// Missing files return an empty map (not an error). Unparseable lines
// are skipped silently.
func Load(dir string) map[string]string {
	out := make(map[string]string)
	f, err := os.Open(filepath.Join(dir, ".env"))
	if err != nil {
		return out
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k != "" {
			out[k] = v
		}
	}
	return out
}
