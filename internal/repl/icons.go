package repl

import (
	"os"
	"path"
	"strings"
)

// iconsEnabled reports whether nerd-font file-type glyphs should render. The
// ECHO_ICONS env var wins; then the `icons` config value; then "auto" —
// on for an interactive stdout that isn't a known plain terminal (the Linux
// VT console and dumb terminals never carry nerd-font glyphs), off when the
// output is piped/redirected (recipes, --log, CI) so logs stay glyph-free.
func (sess *session) iconsEnabled() bool {
	cfgIcons := ""
	if sess.cfg != nil {
		cfgIcons = sess.cfg.Icons
	}
	return resolveIcons(os.Getenv("ECHO_ICONS"), cfgIcons, stdoutIsTTY(), os.Getenv("TERM"))
}

// resolveIcons is the pure decision: env override → config value → auto
// (interactive, non-plain terminal). Extracted for testing without a session.
func resolveIcons(envIcons, cfgIcons string, isTTY bool, term string) bool {
	if v, ok := parseIconToggle(envIcons); ok {
		return v
	}
	if v, ok := parseIconToggle(cfgIcons); ok {
		return v
	}
	if !isTTY {
		return false
	}
	switch term {
	case "", "dumb", "linux":
		return false
	}
	return true
}

// parseIconToggle reads an explicit on/off value, returning ok=false for
// "auto"/empty/unrecognized so the caller falls through to auto-detection.
func parseIconToggle(v string) (on, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "on", "true", "yes":
		return true, true
	case "0", "off", "false", "no":
		return false, true
	}
	return false, false
}

// folderIcon (seti-folder) prefixes directory nodes; defaultFileIcon
// (seti-default) is the fallback for an unmapped extension.
const (
	folderIcon      = ""
	defaultFileIcon = ""
)

// fileIcons maps a lowercased file extension to its nerd-font glyph. The set
// covers the file types found in an Odoo module.
var fileIcons = map[string]string{
	".py":   "",          // seti-python
	".xml":  "",          // seti-xml
	".csv":  "",          // seti-csv
	".po":   "\U000f05ca", // md-translate
	".pot":  "\U000f05ca", // md-translate
	".js":   "",          // seti-javascript
	".css":  "",          // seti-css
	".scss": "",          // seti-css
	".json": "",          // seti-json
	".yml":  "",          // seti-yml
	".yaml": "",          // seti-yml
	".md":   "",          // seti-markdown
	".rst":  "",          // seti-markdown
	".html": "",          // seti-html
	".png":  "",          // seti-image
	".jpg":  "",          // seti-image
	".jpeg": "",          // seti-image
	".svg":  "",          // seti-image
	".gif":  "",          // seti-image
	".ico":  "",          // seti-image
}

// fileIcon returns the glyph for a file name's extension, defaulting to the
// generic file glyph.
func fileIcon(name string) string {
	if ic, ok := fileIcons[strings.ToLower(path.Ext(name))]; ok {
		return ic
	}
	return defaultFileIcon
}
