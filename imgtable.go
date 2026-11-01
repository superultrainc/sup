package main

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// bodyChunk is either markdown text or a table that holds images.
type bodyChunk struct {
	text  string
	table [][]string // rows of cells; row 1 is the separator
}

var tableSepRe = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$`)

// splitImageTables pulls out pipe tables that contain images, so they can be
// laid out as a grid; tables without images stay markdown.
// shortcut: only tables whose rows start with "|", which is how PR templates write them.
func splitImageTables(body string) []bodyChunk {
	lines := strings.Split(body, "\n")
	var out []bodyChunk
	var text []string
	flush := func() {
		if len(text) > 0 {
			out = append(out, bodyChunk{text: strings.Join(text, "\n")})
			text = nil
		}
	}
	fence := false
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "```") {
			fence = !fence
		}
		if fence || !strings.HasPrefix(t, "|") || i+1 >= len(lines) || !tableSepRe.MatchString(strings.TrimSpace(lines[i+1])) {
			text = append(text, lines[i])
			continue
		}
		j := i + 2
		for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "|") {
			j++
		}
		if len(findImages(strings.Join(lines[i:j], "\n"))) == 0 {
			text = append(text, lines[i:j]...)
			i = j - 1
			continue
		}
		flush()
		var rows [][]string
		for _, l := range lines[i:j] {
			rows = append(rows, tableCells(l))
		}
		out = append(out, bodyChunk{table: rows})
		i = j - 1
	}
	flush()
	return out
}

func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

const tableGap = 2

// imageTableLines lays out a table with images side by side: text columns
// (like a "Light" / "Dark" label) take what they need, image columns share the rest.
func imageTableLines(rows [][]string, width int) []string {
	header, body := rows[0], rows[2:]
	ncol := len(header)
	for _, r := range body {
		ncol = max(ncol, len(r))
	}
	cell := func(r []string, c int) string {
		if c < len(r) {
			return r[c]
		}
		return ""
	}
	widths := make([]int, ncol)
	imgCols, used := 0, tableGap*(ncol-1)
	for c := range widths {
		hasImg := false
		for _, r := range body {
			hasImg = hasImg || len(findImages(cell(r, c))) > 0
		}
		if hasImg {
			imgCols++
			continue
		}
		for _, r := range append([][]string{header}, body...) {
			widths[c] = max(widths[c], lipgloss.Width(plainCell(cell(r, c))))
		}
		widths[c] = min(widths[c], width/4)
		used += widths[c]
	}
	imgW := (width - used) / max(imgCols, 1)
	if imgW < 12 {
		// Too narrow for a grid: one cell after another.
		var out []string
		for _, r := range append([][]string{header}, body...) {
			for _, c := range r {
				out = append(out, cellLines(c, width)...)
			}
		}
		return out
	}
	for c := range widths {
		if widths[c] == 0 {
			widths[c] = imgW
		}
	}

	th := currentDiffTheme()
	bold := sty(th.text, "", true, false)
	var out []string
	if strings.Join(header, "") != "" {
		var sb strings.Builder
		for c, w := range widths {
			if c > 0 {
				sb.WriteString(spaces(tableGap))
			}
			sb.WriteString(fitWidth(bold.Render(plainCell(cell(header, c))), w))
		}
		out = append(out, sb.String(), "")
	}
	for i, r := range body {
		if i > 0 {
			out = append(out, "")
		}
		cols := make([][]string, ncol)
		height := 0
		for c, w := range widths {
			cols[c] = cellLines(cell(r, c), w)
			height = max(height, len(cols[c]))
		}
		for y := 0; y < height; y++ {
			var sb strings.Builder
			for c, w := range widths {
				if c > 0 {
					sb.WriteString(spaces(tableGap))
				}
				l := ""
				if y < len(cols[c]) {
					l = cols[c][y]
				}
				sb.WriteString(fitWidth(l, w))
			}
			out = append(out, strings.TrimRight(sb.String(), " "))
		}
	}
	return out
}

// imageRow lays images out side by side in equal columns, or stacked if too narrow.
func imageRow(refs []imageRef, width int) []string {
	colW := (width - tableGap*(len(refs)-1)) / len(refs)
	if len(refs) == 1 || colW < 12 {
		var out []string
		for i, r := range refs {
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, imageLines(r, width)...)
		}
		return out
	}
	cols := make([][]string, len(refs))
	height := 0
	for i, r := range refs {
		cols[i] = imageLines(r, colW)
		height = max(height, len(cols[i]))
	}
	out := make([]string, height)
	for y := range out {
		var sb strings.Builder
		for i, c := range cols {
			if i > 0 {
				sb.WriteString(spaces(tableGap))
			}
			l := ""
			if y < len(c) {
				l = c[y]
			}
			sb.WriteString(fitWidth(l, colW))
		}
		out[y] = strings.TrimRight(sb.String(), " ")
	}
	return out
}

// cellLines renders one table cell: its text, then its images stacked.
func cellLines(cell string, width int) []string {
	refs := findImages(cell)
	if len(refs) == 0 {
		if t := plainCell(cell); t != "" {
			return []string{truncate(t, width)}
		}
		return nil
	}
	var out []string
	for i, r := range refs {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, imageLines(r, width)...)
	}
	return out
}

var cellMarkupRe = regexp.MustCompile("(<br\\s*/?>)|[*_`]")

// plainCell is a cell's text without images or simple markup.
func plainCell(cell string) string {
	var sb strings.Builder
	pos := 0
	for _, r := range findImages(cell) {
		sb.WriteString(cell[pos:r.start])
		pos = r.end
	}
	sb.WriteString(cell[pos:])
	return strings.TrimSpace(cellMarkupRe.ReplaceAllString(sb.String(), " "))
}
