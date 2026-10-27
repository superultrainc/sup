package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Every rendered row must be exactly the pane width or the card borders break.
func TestDiffRowsFitWidth(t *testing.T) {
	files := parseUnifiedDiff(mockPatch)
	highlightFiles(files)
	d := mockPRDetail(PR{})
	for _, width := range []int{60, 100, 181} {
		for _, split := range []bool{false, true} {
			for _, wrap := range []bool{false, true} {
				r := &diffRenderer{th: darkDiffTheme, width: width - 2, split: split, wrap: wrap, threads: d.ReviewThreads.Nodes}
				r.render(files)
				for i, l := range r.lines {
					if l == " " {
						continue
					}
					if w := lipgloss.Width(l); w != width-1 {
						t.Fatalf("width=%d split=%v wrap=%v line %d is %d wide: %q", width, split, wrap, i, w, l)
					}
				}
				if len(r.threadMarks) != 2 {
					t.Errorf("want 2 inline threads, got %d", len(r.threadMarks))
				}
			}
		}
	}
}
