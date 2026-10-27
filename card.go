package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// styleCache avoids rebuilding a lipgloss.Style per highlighted token.
var styleCache = map[[4]string]lipgloss.Style{}

func sty(fg, bg string, bold, italic bool) lipgloss.Style {
	key := [4]string{fg, bg, fmt.Sprint(bold), fmt.Sprint(italic)}
	if s, ok := styleCache[key]; ok {
		return s
	}
	s := lipgloss.NewStyle().Bold(bold).Italic(italic)
	if fg != "" {
		s = s.Foreground(lipgloss.Color(fg))
	}
	if bg != "" {
		s = s.Background(lipgloss.Color(bg))
	}
	styleCache[key] = s
	return s
}

// fitWidth truncates or pads s to exactly w cells.
func fitWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// Cards are drawn at a fixed total width (borders included).
func cardTop(title, border string, width int) string {
	bs := sty(border, "", false, false)
	if title == "" {
		return bs.Render("╭" + strings.Repeat("─", width-2) + "╮")
	}
	title = ansi.Truncate(title, max(width-6, 1), "…")
	fill := max(width-5-lipgloss.Width(title), 0)
	return bs.Render("╭─ ") + title + bs.Render(" "+strings.Repeat("─", fill)+"╮")
}

func cardRow(content, border string, width int) string {
	bs := sty(border, "", false, false)
	return bs.Render("│") + fitWidth(content, width-2) + bs.Render("│")
}

func cardBottom(border string, width int) string {
	return sty(border, "", false, false).Render("╰" + strings.Repeat("─", width-2) + "╯")
}

// textCard frames body with one column of padding; body lines should fit width-4.
func textCard(title, border string, body []string, width int) []string {
	out := []string{cardTop(title, border, width)}
	for _, l := range body {
		out = append(out, cardRow(" "+fitWidth(l, width-4)+" ", border, width))
	}
	return append(out, cardBottom(border, width))
}

var mdRenderers = map[int]*glamour.TermRenderer{}

// markdownLines renders GitHub markdown to lines at most width cells wide.
func markdownLines(body string, width int) []string {
	body = cleanBody(body)
	if body == "" {
		return nil
	}
	r, ok := mdRenderers[width]
	if !ok {
		cfg := styles.DarkStyleConfig
		if glamourStyle == "light" {
			cfg = styles.LightStyleConfig
		}
		// Cards already provide padding, so drop glamour's document margin.
		zero := uint(0)
		cfg.Document.Margin = &zero
		cfg.Document.BlockPrefix, cfg.Document.BlockSuffix = "", ""
		r, _ = glamour.NewTermRenderer(glamour.WithStyles(cfg), glamour.WithWordWrap(width), glamour.WithEmoji())
		mdRenderers[width] = r
	}
	out := body
	if r != nil {
		if s, err := r.Render(body); err == nil {
			out = s
		}
	}
	lines := strings.Split(out, "\n")
	blank := func(s string) bool { return strings.TrimSpace(ansi.Strip(s)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return lines
}
