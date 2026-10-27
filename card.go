package main

import (
	"strings"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// painter wraps text in a style's escape codes. lipgloss.Style.Render is
// too slow for per-token use (it copies a large struct and re-measures),
// so each style is rendered once around a marker and the codes reused.
type painter struct{ prefix, suffix string }

func (p painter) Render(s string) string {
	if p.prefix == "" || s == "" {
		return s
	}
	return p.prefix + s + p.suffix
}

const paintMarker = ""

type styleKey struct {
	fg, bg       string
	bold, italic bool
}

var styleCache = map[styleKey]painter{}

func sty(fg, bg string, bold, italic bool) painter {
	key := styleKey{fg, bg, bold, italic}
	if p, ok := styleCache[key]; ok {
		return p
	}
	s := lipgloss.NewStyle().Bold(bold).Italic(italic)
	if fg != "" {
		s = s.Foreground(lipgloss.Color(fg))
	}
	if bg != "" {
		s = s.Background(lipgloss.Color(bg))
	}
	var p painter
	// Rendering through lipgloss keeps its color-profile downgrade (256/16 colors).
	if before, after, ok := strings.Cut(s.Render(paintMarker), paintMarker); ok {
		p = painter{before, after}
	}
	styleCache[key] = p
	return p
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
		// Match GitHub: plain bold headings and subtle inline code.
		th := currentDiffTheme()
		text, chipBg := th.text, th.chipBg
		bold := true
		for _, h := range []*glamouransi.StyleBlock{&cfg.H1, &cfg.H2, &cfg.H3, &cfg.H4, &cfg.H5, &cfg.H6} {
			h.Prefix, h.Suffix = "", ""
			h.Color, h.BackgroundColor, h.Bold = &text, nil, &bold
		}
		cfg.Code.Color, cfg.Code.BackgroundColor = &text, &chipBg
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
