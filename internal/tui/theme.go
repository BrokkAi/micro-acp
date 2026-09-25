package tui

import "charm.land/lipgloss/v2"

var plain = lipgloss.NewStyle()

type palette struct {
	dark                                                      bool
	accent, mint, muted, danger, rule, cyan, amber, selection lipgloss.Style
}

func newPalette(dark bool) palette {
	shade := lipgloss.LightDark(dark)
	fg := func(light, dark string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(shade(lipgloss.Color(light), lipgloss.Color(dark)))
	}
	p := palette{
		dark:   dark,
		accent: fg("#1C64C8", "#7CB5F8"),
		mint:   fg("#26734D", "#8AD4AC"),
		muted:  fg("#64748B", "#929EAF"),
		danger: fg("#BF3545", "#F28B92"),
		rule:   fg("#CBD5E1", "#3E4959"),
		cyan:   fg("#087C89", "#7ECED6"),
		amber:  fg("#956A17", "#E5BC7A"),
	}
	p.selection = p.accent.Bold(true).Background(shade(lipgloss.Color("#E8F1FC"), lipgloss.Color("#1D2C40")))
	return p
}

func (m *model) applyTheme(dark bool) {
	m.theme = newPalette(dark)
	styles := m.input.Styles()
	styles.Focused.CursorLine = plain
	styles.Focused.Prompt = m.theme.accent
	styles.Focused.Placeholder = m.theme.muted
	m.input.SetStyles(styles)
	m.spinner.Style = m.theme.accent
	m.renderCache = map[string]string{}
}
