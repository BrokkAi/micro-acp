package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Shared building blocks so every panel, list, and hint line reads the same.

// gutter is the width of the marker column every transcript row starts with.
const gutter = 2

// hints renders "key label" pairs with the key brighter than its label. Whole
// pairs are dropped from the end rather than cutting a word in half.
func (m *model) hints(width int, pairs ...string) string {
	separator := m.theme.dim.Render(" · ")
	result, used := "", 0
	for i := 0; i+1 < len(pairs); i += 2 {
		key, label := pairs[i], pairs[i+1]
		chunk := key
		if label != "" {
			chunk += " " + label
		}
		size := ansi.StringWidth(chunk)
		if result != "" {
			size += 3
		}
		if used+size > width {
			if result == "" {
				return m.theme.muted.Render(line(chunk, width))
			}
			break
		}
		if result != "" {
			result += separator
		}
		result += m.hintKey().Render(key)
		if label != "" {
			result += m.theme.muted.Render(" " + label)
		}
		used += size
	}
	return result
}

// hintKey styles primary text in the terminal's own foreground, which reads
// on any background, light or dark.
func (m *model) hintKey() lipgloss.Style {
	return lipgloss.NewStyle()
}

// titleRule draws "── Title ───── right ─" across width. The title carries the
// panel's color; the rule itself stays quiet.
func (m *model) titleRule(title, right string, width int, style lipgloss.Style) string {
	if title == "" {
		return m.theme.rule.Render(strings.Repeat("─", max(0, width)))
	}
	title = line(title, max(1, width-6))
	used := 4 + ansi.StringWidth(title)
	tail := ""
	if right != "" && used+ansi.StringWidth(right)+4 <= width {
		tail = " " + m.theme.muted.Render(right) + m.theme.rule.Render(" ─")
		used += ansi.StringWidth(right) + 3
	}
	return m.theme.rule.Render("── ") + style.Render(title) + m.theme.rule.Render(" "+strings.Repeat("─", max(0, width-used))) + tail
}

// hang prefixes the first line with a gutter marker and indents the rest so
// wrapped text hangs under itself rather than under the marker.
func hang(marker, body string) string {
	pad := strings.Repeat(" ", gutter)
	lines := strings.Split(body, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = marker + lines[i]
		} else if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// elapsed formats a duration the way people read a stopwatch.
func elapsed(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
	}
}

// shimmer brightens a band that sweeps across the text every couple of
// seconds, so long waits still look alive. Profiles without color simply
// render the resting color.
func (m *model) shimmer(text string, since time.Duration) string {
	ramp := m.theme.shimmer
	runes := []rune(text)
	if len(ramp) == 0 || len(runes) == 0 {
		return m.theme.muted.Render(text)
	}
	period := float64(len(runes) + 16)
	center := math.Mod(since.Seconds()/2*period, period) - 8
	var b strings.Builder
	for i, r := range runes {
		d := math.Abs(float64(i) - center)
		level := 0.0
		if d < 4 {
			level = (1 + math.Cos(math.Pi*d/4)) / 2
		}
		color := ramp[int(math.Round(level*float64(len(ramp)-1)))]
		b.WriteString(lipgloss.NewStyle().Foreground(color).Render(string(r)))
	}
	return b.String()
}

// displayPath shows paths inside the workspace relative to it and abbreviates
// the home directory, keeping the informative tail when it must truncate.
func displayPath(path, cwd string) string {
	path = strings.ReplaceAll(clean(path), "\n", " ")
	if cwd != "" && filepath.IsAbs(path) {
		if rel, err := filepath.Rel(cwd, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// truncateLeft keeps the end of s, which is the useful part of a path.
func truncateLeft(s string, width int) string {
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(s)
	for len(runes) > 0 && ansi.StringWidth(string(runes))+1 > width {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

// ago describes how long before now t was, coarsely, the way a session list
// is scanned; older dates fall back to the calendar.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	case t.Year() == now.Year():
		return t.Local().Format("Jan 2")
	}
	return t.Local().Format("Jan 2 2006")
}

// listMark tags the markers glamour prints for list items and tasks, so only
// real items are hung, never text that merely starts with "1. " or "✓ ".
// Every marker that follows it is two cells wide.
const listMark = ""

// hangLists indents the wrapped lines of list items so they hang under the
// item text instead of returning to the bullet's column, which glamour
// cannot do. A continuation is a non-blank line at the item's own
// indentation; deeper lines, such as code, are left alone. Lines are only
// shifted, never joined, so hard breaks and long words survive. It reports
// how much narrower to render so every continuation fits.
func hangLists(rendered string, width int) (string, int) {
	if !strings.Contains(rendered, listMark) {
		return rendered, 0
	}
	lines := strings.Split(rendered, "\n")
	need := 0
	for i := range lines {
		last := strings.LastIndex(lines[i], listMark)
		if last < 0 {
			continue
		}
		column := ansi.StringWidth(strings.ReplaceAll(lines[i][:last], listMark, "")) + 2
		lines[i] = strings.ReplaceAll(lines[i], listMark, "")
		plain := ansi.Strip(lines[i])
		indent := len(plain) - len(strings.TrimLeft(plain, " "))
		shift := column - indent
		for j := i + 1; j < len(lines) && !strings.Contains(lines[j], listMark); j++ {
			next := ansi.Strip(lines[j])
			if strings.TrimSpace(next) == "" || len(next)-len(strings.TrimLeft(next, " ")) != indent {
				break
			}
			if ansi.StringWidth(lines[j])+shift > width {
				need = max(need, shift)
				continue
			}
			lines[j] = strings.Repeat(" ", shift) + lines[j]
		}
	}
	return strings.Join(lines, "\n"), need
}

// plainSGR accepts the style parameters the renderer itself uses. Blink and
// conceal never come from it, so they can only be smuggled in by agent text.
func plainSGR(params string) bool {
	if strings.Trim(params, "0123456789;:") != "" {
		return false
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "38", "48", "58":
			if i+1 < len(fields) && fields[i+1] == "5" {
				i += 2
			} else if i+1 < len(fields) && fields[i+1] == "2" {
				i += 4
			}
		case "5", "6", "8":
			return false
		}
	}
	return true
}

// keepEscapes drops every escape sequence except colors (SGR) and
// hyperlinks (OSC 8), plus other control characters. Markdown decodes
// character references such as &#27; after the text was cleaned, so the
// rendered output is filtered again before it reaches the terminal.
func keepEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) && s[j] == 'm' && plainSGR(s[i+2:j]) {
				b.WriteString(s[i : j+1])
			}
			i = j + 1
			continue
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == ']' {
			end, size := -1, 0
			for j := i + 2; j < len(s); j++ {
				if s[j] == 0x07 {
					end, size = j, 1
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					end, size = j, 2
					break
				}
			}
			if end < 0 {
				i += 2
				continue
			}
			if strings.HasPrefix(s[i+2:end], "8;") && !strings.ContainsFunc(s[i+2:end], unicode.IsControl) {
				b.WriteString(s[i : end+size])
			}
			i = end + size
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

var trailingBlank = regexp.MustCompile(`(?:[ \t]|\x1b\[[0-9;:]*m)+$`)

// trimRight removes trailing padding (and the styles wrapped around it) from
// each line so copied text has no stray spaces, then drops blank edge lines.
func trimRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		trimmed := trailingBlank.ReplaceAllString(l, "")
		if trimmed != l && strings.Contains(l[len(trimmed):], "\x1b[") {
			trimmed += "\x1b[m"
		}
		if ansi.Strip(trimmed) == "" {
			trimmed = ""
		}
		lines[i] = trimmed
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
