package repl

import (
	"context"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/pascualchavez/echo/internal/banner"
	"github.com/pascualchavez/echo/internal/config"
	"github.com/pascualchavez/echo/internal/theme"
)

// Nerd Font glyphs for container health.
const (
	glyphDocker   = "" // nf-md-docker
	glyphPostgres = "" // nf-md-postgresql
)

type promptBuilder struct {
	styles      theme.Styles
	palette     theme.Palette
	cfg         *config.Config
	composeName string // already truncated
	stage       theme.Stage
	version     string
	health      *healthCache
	logoIcon    string
}

func newPromptBuilder(sess *session) *promptBuilder {
	name := resolveComposeProject(sess.cfg)
	name = truncateName(name, sess.cfg.PromptNameMax)

	var hc *healthCache
	if sess.cfg.OdooContainer != "" && sess.cfg.DBContainer != "" {
		hc = newHealthCache(sess.cfg.OdooContainer, sess.cfg.DBContainer, sess.cfg.HealthTTL)
	}

	return &promptBuilder{
		styles:      sess.styles,
		palette:     sess.palette,
		cfg:         sess.cfg,
		composeName: name,
		stage:       sess.stage,
		version:     sess.version,
		health:      hc,
		logoIcon:    banner.LogoIcon(sess.cfg.Logo),
	}
}

// refresh syncs the builder's mutable inputs from the session after a
// theme/stage/version change. Cheaper than rebuilding from scratch.
func (p *promptBuilder) refresh(sess *session) {
	p.styles = sess.styles
	p.palette = sess.palette
	p.stage = sess.stage
	p.version = sess.version
	p.logoIcon = banner.LogoIcon(sess.cfg.Logo)

	// Container names may have changed via `init`; rebuild the cache.
	if sess.cfg.OdooContainer != "" && sess.cfg.DBContainer != "" {
		if p.health == nil ||
			p.health.odooName != sess.cfg.OdooContainer ||
			p.health.dbName != sess.cfg.DBContainer {
			p.health = newHealthCache(sess.cfg.OdooContainer, sess.cfg.DBContainer, sess.cfg.HealthTTL)
		}
	} else {
		p.health = nil
	}

	if name := truncateName(resolveComposeProject(sess.cfg), sess.cfg.PromptNameMax); name != "" {
		p.composeName = name
	}
}

// Render builds the full prompt string for the next readLine call.
func (p *promptBuilder) Render(ctx context.Context) string {
	s := p.styles

	var parts []string
	for _, seg := range p.cfg.PromptSegments {
		if r := p.renderSegment(ctx, seg); r != "" {
			parts = append(parts, r)
		}
	}

	head := s.Accent.Render(p.logoIcon)
	body := strings.Join(parts, " ")

	out := head
	if body != "" {
		out += " " + body
	}
	out += s.Out.Render(":") +
		s.Tilde.Render("~") +
		s.Dollar.Render("$ ")
	return out
}

func (p *promptBuilder) renderSegment(ctx context.Context, name string) string {
	s := p.styles
	switch name {
	case "name":
		if p.composeName == "" {
			return ""
		}
		return s.Accent.Render("echo-" + p.composeName)

	case "version_db":
		open := s.Dim.Render("[")
		closeB := s.Dim.Render("]")
		ver := s.Out.Render(p.version + ".0")
		sep := s.Faint.Render(" · ")
		db := s.Dim.Render(p.cfg.DBName)
		return open + ver + sep + db + closeB

	case "stage":
		stageColor := p.palette.PromptColor(p.stage)
		return lipgloss.NewStyle().
			Foreground(stageColor).
			Bold(true).
			Render(string(p.stage))

	case "health":
		if p.health == nil {
			return ""
		}
		snap := p.health.Read(ctx)
		return healthGlyph(p.palette, glyphDocker, snap.Odoo) + " " +
			healthGlyph(p.palette, glyphPostgres, snap.DB)

	default:
		return ""
	}
}

func healthGlyph(p theme.Palette, glyph string, state containerState) string {
	var color lipgloss.Color
	switch state {
	case stateRunning:
		color = p.Success
	case stateRestarting:
		color = p.Warning
	case stateStopped:
		color = p.Error
	default:
		color = p.Faint
	}
	return lipgloss.NewStyle().Foreground(color).Render(glyph)
}

// validatePromptSegments returns the subset of seg that is recognized
// and the list of unknown names (for a one-shot warning at startup).
func validatePromptSegments(segments []string) (valid []string, unknown []string) {
	seen := make(map[string]bool, len(segments))
	for _, s := range segments {
		switch s {
		case "name", "version_db", "stage", "health":
			if !seen[s] {
				valid = append(valid, s)
				seen[s] = true
			}
		default:
			unknown = append(unknown, s)
		}
	}
	return valid, unknown
}
