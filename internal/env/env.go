package env

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Load reads `.env` from dir and returns its key/value pairs.
// Missing files return an empty map (not an error). Unparseable lines
// are skipped silently.
func Load(dir string) map[string]string {
	f, err := os.Open(filepath.Join(dir, ".env"))
	if err != nil {
		return make(map[string]string)
	}
	defer f.Close()
	return Parse(f)
}

// Parse reads dotenv-style key/value pairs from r. Blank lines and
// comments are skipped; surrounding quotes are stripped. Used for both
// local files and remote `.env` content fetched over SSH.
func Parse(r io.Reader) map[string]string {
	out := make(map[string]string)
	scanner := bufio.NewScanner(r)
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
