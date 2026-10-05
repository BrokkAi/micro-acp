package tui

import (
	"image/color"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

var plain = lipgloss.NewStyle()

// palette holds one accent, semantic status colors, and surfaces derived from
// the terminal background so tints read as lifts of the user's own theme.
type palette struct {
	dark                                                bool
	accent, mint, muted, dim, danger, rule, cyan, amber lipgloss.Style
	// selection marks the focused row in lists; surface tints user turns.
	// Tints are nil on terminals with 16 colors or fewer, where any
	// background would be a harsh block.
	selection         lipgloss.Style
	selectBg, surface color.Color
	addBg, delBg      color.Color
	accentHex         string
	hex               map[string]string
	shimmer           []color.Color
}

// on is a style with background c, or no background when c is nil.
func on(c color.Color) lipgloss.Style {
	if c == nil {
		return plain
	}
	return lipgloss.NewStyle().Background(c)
}

func newPalette(dark bool, background color.Color, profile colorprofile.Profile) palette {
	pick := func(light, darkHex string) string {
		if dark {
			return darkHex
		}
		return light
	}
	amount := func(light, darkAmount float64) float64 {
		if dark {
			return darkAmount
		}
		return light
	}
	hex := map[string]string{
		"accent": pick("#1C64C8", "#7CB5F8"),
		"mint":   pick("#26734D", "#8AD4AC"),
		"muted":  pick("#64748B", "#929EAF"),
		"dim":    pick("#94A3B8", "#677386"),
		"danger": pick("#BF3545", "#F28B92"),
		"rule":   pick("#CBD5E1", "#3B4554"),
		"cyan":   pick("#087C89", "#7ECED6"),
		"amber":  pick("#956A17", "#E5BC7A"),
		"text":   pick("#1F2937", "#DDE4EE"),
	}
	fg := func(name string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex[name])) }
	toward := lipgloss.Color(pick("#000000", "#FFFFFF"))
	p := palette{
		dark:      dark,
		accent:    fg("accent"),
		mint:      fg("mint"),
		muted:     fg("muted"),
		dim:       fg("dim"),
		danger:    fg("danger"),
		rule:      fg("rule"),
		cyan:      fg("cyan"),
		amber:     fg("amber"),
		accentHex: hex["accent"],
		hex:       hex,
	}
	// Tints are mixed into the real background. Without a reported one the
	// light or dark guess may be wrong, and a tint behind the terminal's own
	// text color could make it unreadable, so there are none.
	if background != nil && (profile == colorprofile.Unknown || profile >= colorprofile.ANSI256) {
		p.surface = blend(background, toward, amount(0.045, 0.07))
		p.selectBg = blend(background, lipgloss.Color(hex["accent"]), amount(0.13, 0.2))
		p.addBg = blend(background, lipgloss.Color(pick("#2DA44E", "#3FB950")), amount(0.16, 0.2))
		p.delBg = blend(background, lipgloss.Color(pick("#CF222E", "#F85149")), amount(0.13, 0.2))
	}
	p.selection = p.accent.Bold(true).Inherit(on(p.selectBg))
	// Status text rests at muted and brightens as a band passes over it.
	// Coarser palettes would only flicker between a few steps.
	if background != nil && (profile == colorprofile.Unknown || profile == colorprofile.TrueColor) {
		p.shimmer = lipgloss.Blend1D(6, lipgloss.Color(hex["muted"]), lipgloss.Color(hex["text"]))
	}
	return p
}

// blend mixes a toward b by f.
func blend(a, b color.Color, f float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	mix := func(x, y uint32) uint8 { return uint8((float64(x>>8)*(1-f) + float64(y>>8)*f) + 0.5) }
	return color.RGBA{R: mix(ar, br), G: mix(ag, bg), B: mix(ab, bb), A: 255}
}

func (m *model) applyTheme(dark bool) {
	m.theme = newPalette(dark, m.background, m.profile)
	styles := m.input.Styles()
	styles.Focused.CursorLine = plain
	styles.Focused.Prompt = m.theme.accent
	styles.Focused.Placeholder = m.theme.dim
	styles.Blurred.Prompt = m.theme.dim
	styles.Blurred.Placeholder = m.theme.dim
	m.input.SetStyles(styles)
	m.spinner.Style = m.theme.accent
	m.renderCache = map[string]string{}
	m.renderers = map[int]*markdownRenderer{}
	// Open fields keep their text; only their colors follow the theme.
	if m.picker != nil {
		m.picker.input.SetStyles(m.textInput().Styles())
	}
	if m.elicitation != nil {
		m.elicitation.input.SetStyles(m.textInput().Styles())
	}
}

func ptr[T any](v T) *T { return &v }

// markdownStyle keeps glamour's structure but drops its margins, blank-line
// padding, and fixed 256-color palette in favor of the theme. Body text uses
// the terminal's own foreground.
func (p palette) markdownStyle(width int) ansi.StyleConfig {
	c := func(name string) *string { return ptr(p.hex[name]) }
	// Glamour renders indent tokens and block prefixes in the parent style,
	// so their colors are embedded directly.
	paint := func(name, s string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(p.hex[name])).Render(s)
	}
	codeTheme := "github-dark"
	if !p.dark {
		codeTheme = "github"
	}
	rule := ""
	for range min(width, 48) {
		rule += "─"
	}
	return ansi.StyleConfig{
		Document:   ansi.StyleBlock{Margin: ptr(uint(0))},
		Paragraph:  ansi.StyleBlock{},
		BlockQuote: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("muted"), Italic: ptr(true)}, Indent: ptr(uint(1)), IndentToken: ptr(paint("rule", "│") + " ")},
		List:       ansi.StyleList{LevelIndent: 2},
		Heading:    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BlockSuffix: "\n", Bold: ptr(true)}},
		H1:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("accent"), Underline: ptr(true)}},
		H2:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("accent")}},
		H3:         ansi.StyleBlock{},
		H4:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("muted")}},
		H5:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("muted")}},
		H6:         ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("muted"), Bold: ptr(false)}},
		Text:       ansi.StylePrimitive{},
		Strikethrough: ansi.StylePrimitive{
			CrossedOut: ptr(true),
		},
		Emph:           ansi.StylePrimitive{Italic: ptr(true)},
		Strong:         ansi.StylePrimitive{Bold: ptr(true)},
		HorizontalRule: ansi.StylePrimitive{Color: c("rule"), Format: "\n" + rule + "\n"},
		Item:           ansi.StylePrimitive{BlockPrefix: listMark + paint("muted", "•") + " "},
		Enumeration:    ansi.StylePrimitive{BlockPrefix: listMark + ". "},
		Task:           ansi.StyleTask{Ticked: listMark + paint("mint", "✓") + " ", Unticked: listMark + paint("muted", "□") + " "},
		Link:           ansi.StylePrimitive{Color: c("muted"), Underline: ptr(true)},
		LinkText:       ansi.StylePrimitive{Color: c("accent")},
		Image:          ansi.StylePrimitive{Color: c("muted"), Underline: ptr(true)},
		ImageText:      ansi.StylePrimitive{Color: c("accent"), Format: "Image: {{.text}}"},
		Code:           ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("cyan")}},
		CodeBlock:      ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{Margin: ptr(uint(2))}, Theme: codeTheme},
		Table:          ansi.StyleTable{CenterSeparator: ptr(paint("rule", "┼")), ColumnSeparator: ptr(paint("rule", "│")), RowSeparator: ptr(paint("rule", "─"))},
		DefinitionList: ansi.StyleBlock{},
		DefinitionTerm: ansi.StylePrimitive{Bold: ptr(true)},
		DefinitionDescription: ansi.StylePrimitive{
			BlockPrefix: "\n" + paint("muted", "›") + " ",
		},
		HTMLBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("muted")}},
		HTMLSpan:  ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c("muted")}},
	}
}
