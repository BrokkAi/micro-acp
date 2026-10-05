package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

func (m *model) hintKey() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.textHex))
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

var listMarker = regexp.MustCompile(`^( *)(• |\d+\. |✓ |□ )`)

// hangLists re-wraps list items so their wrapped lines hang under the item
// text instead of returning to the bullet's column, which glamour cannot do.
// A continuation is a non-blank line at the bullet's own indentation that
// does not start another item; deeper lines, such as code, are left alone.
func hangLists(rendered string, width int) string {
	lines := strings.Split(rendered, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		plain := ansi.Strip(lines[i])
		match := listMarker.FindStringSubmatchIndex(plain)
		if match == nil {
			out = append(out, lines[i])
			continue
		}
		indent, column := match[3]-match[2], ansi.StringWidth(plain[:match[1]])
		end := i + 1
		for end < len(lines) {
			next := ansi.Strip(lines[end])
			lead := len(next) - len(strings.TrimLeft(next, " "))
			if strings.TrimSpace(next) == "" || lead != indent || listMarker.MatchString(next) {
				break
			}
			end++
		}
		if end == i+1 || width-column < 8 {
			out = append(out, lines[i])
			continue
		}
		text := ansi.Cut(lines[i], column, ansi.StringWidth(lines[i]))
		for _, next := range lines[i+1 : end] {
			text += " " + ansi.Cut(next, indent, ansi.StringWidth(next))
		}
		pad := strings.Repeat(" ", column)
		for j, row := range strings.Split(ansi.Wrap(text, width-column, ""), "\n") {
			if j == 0 {
				out = append(out, ansi.Cut(lines[i], 0, column)+row)
			} else {
				out = append(out, pad+row)
			}
		}
		i = end - 1
	}
	return strings.Join(out, "\n")
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
