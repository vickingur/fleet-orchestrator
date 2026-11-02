package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

type styles struct {
	title, bold, faint, ok, warn, bad, muted, accent, rule lipgloss.Style
	okC, warnC, badC, mutedC, accentC                      color.Color
}

// newStyles derives every color from the terminal background, so the view
// reads equally well on light and dark themes.
func newStyles(dark bool) styles {
	ld := lipgloss.LightDark(dark)
	s := styles{
		okC:     ld(lipgloss.Color("#1A7F37"), lipgloss.Color("#3FB950")),
		warnC:   ld(lipgloss.Color("#9A6700"), lipgloss.Color("#D29922")),
		badC:    ld(lipgloss.Color("#CF222E"), lipgloss.Color("#F85149")),
		mutedC:  ld(lipgloss.Color("#6E7781"), lipgloss.Color("#8B949E")),
		accentC: ld(lipgloss.Color("#0969DA"), lipgloss.Color("#58A6FF")),
	}
	s.bold = lipgloss.NewStyle().Bold(true)
	s.title = lipgloss.NewStyle().Bold(true).Foreground(s.accentC)
	s.faint = lipgloss.NewStyle().Foreground(s.mutedC)
	s.muted = s.faint
	s.ok = lipgloss.NewStyle().Foreground(s.okC)
	s.warn = lipgloss.NewStyle().Foreground(s.warnC)
	s.bad = lipgloss.NewStyle().Foreground(s.badC)
	s.accent = lipgloss.NewStyle().Foreground(s.accentC)
	s.rule = lipgloss.NewStyle().Foreground(s.mutedC)
	return s
}
