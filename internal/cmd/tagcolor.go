package cmd

import (
	"hash/fnv"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/theme"
)

// tagRe matches a leading commit tag like `[ADD]`, `[FIX]`, `[IMP]` — the
// `[Tag]` scheme deploy's commit subjects use. Only ASCII-letter tags so a
// `[2024]` or `[#42]` in a subject isn't mistaken for one.
var tagRe = regexp.MustCompile(`\[[A-Za-z]+\]`)

// renderTailWithTags renders a picker row's secondary column (the dim tail
// after the name) with any leading `[TAG]` token colored by its type, the
// rest staying dim. Used so the deploy commit picker tints each commit's
// tag; rows without a tag (every other picker) render fully dim, unchanged.
func renderTailWithTags(tail string, dim lipgloss.Style, p theme.Palette) string {
	loc := tagRe.FindStringIndex(tail)
	if loc == nil {
		return dim.Render(tail)
	}
	before, tag, after := tail[:loc[0]], tail[loc[0]:loc[1]], tail[loc[1]:]
	inner := strings.Trim(tag, "[]")
	return dim.Render(before) + tagStyle(inner, p).Render(tag) + dim.Render(after)
}

// tagStyle maps a commit tag to its display style. Known tags use semantic
// palette colors (ADD green, FIX red, IMP cyan, …); an unrecognized tag gets
// a stable pseudo-random pastel from tagFallbackPalette, so the project's own
// tags still read as distinct, consistent colors without being hardcoded.
func tagStyle(tag string, p theme.Palette) lipgloss.Style {
	bold := lipgloss.NewStyle().Bold(true)
	switch strings.ToUpper(tag) {
	case "ADD", "FEAT", "NEW":
		return bold.Foreground(p.Success)
	case "FIX", "BUG", "HOTFIX":
		return bold.Foreground(p.Error)
	case "IMP", "PERF", "OPT":
		return bold.Foreground(p.Info)
	case "REF", "REFACTOR", "CLN", "STY":
		return bold.Foreground(p.Accent2)
	case "DOC", "DOCS":
		return bold.Foreground(p.Warning)
	case "REL", "BUMP", "VER":
		return bold.Foreground(p.Accent)
	case "REM", "REMOVE", "DEL":
		return bold.Foreground(p.Error)
	case "MERGE":
		return bold.Foreground(p.Accent2)
	case "WIP", "TMP":
		return bold.Foreground(p.Faint)
	}
	return bold.Foreground(tagFallbackColor(tag))
}

// tagFallbackPalette is the pastel rotation an unknown tag is hashed into,
// mirroring the logger color rotation so unrecognized tags still get a stable,
// legible tint instead of a single catch-all color.
var tagFallbackPalette = []lipgloss.Color{
	"#ffb3ba", // coral
	"#ffd6a5", // peach
	"#caffbf", // mint
	"#9bf6ff", // cyan
	"#a0c4ff", // sky
	"#bdb2ff", // lavender
	"#ffc6ff", // pink
	"#f0a6ca", // rose
}

// tagFallbackColor picks a tagFallbackPalette slot by FNV-1a hash of the
// (upper-cased) tag, so a given unknown tag always renders the same color.
func tagFallbackColor(tag string) lipgloss.Color {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToUpper(tag)))
	return tagFallbackPalette[h.Sum32()%uint32(len(tagFallbackPalette))]
}
