package main

import (
	"image"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestImageTableSideBySide(t *testing.T) {
	cellPxW, cellPxH = 10, 20
	for _, u := range []string{"b1.png", "a1.png", "b2.png", "a2.png"} {
		imageStore[u] = &imageEntry{img: image.NewNRGBA(image.Rect(0, 0, 200, 100)), cssW: 200}
	}
	defer func() { imageStore = map[string]*imageEntry{}; imageRenders = map[string][]string{} }()
	body := "Intro\n\n| | Before | After |\n| --- | --- | --- |\n" +
		`| Light | <img src="b1.png" width="200"> | <img src="a1.png" width="200"> |` + "\n" +
		`| **Dark** | <img src="b2.png" width="200"> | <img src="a2.png" width="200"> |` + "\nOutro"
	lines := richBody(body, 60)
	text := strings.Join(lines, "\n")
	for _, want := range []string{"Intro", "Before", "After", "Light", "Dark", "Outro"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(text, "|") || strings.Contains(text, "**") {
		t.Errorf("table markup leaked:\n%s", text)
	}
	// 200px images at 10px cells are 20 cols, so both fit on one row of 5 cell rows each.
	var light int
	for i, l := range lines {
		if strings.Contains(l, "Light") {
			light = i
		}
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	if got := strings.Count(lines[light], "▀"); got != 40 {
		t.Errorf("want two 20-col images beside the label, got %d cells", got)
	}
}

func TestPlainTablesStayMarkdown(t *testing.T) {
	chunks := splitImageTables("| a | b |\n| - | - |\n| 1 | 2 |")
	if len(chunks) != 1 || chunks[0].table != nil {
		t.Fatal("a table without images should stay text")
	}
}
