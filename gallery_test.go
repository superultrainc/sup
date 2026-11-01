package main

import (
	"image"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestGalleryStepsThroughImages(t *testing.T) {
	m := detailModel(120, 30)
	d := *m.detail.detail
	d.Body = "| | Before | After |\n| - | - | - |\n| Light | ![b](b.png) | ![a](a.png) |"
	d.Comments.Nodes = append(d.Comments.Nodes, prComment{Body: "again ![a](a.png) and ![c](c.png)"})
	m.detail.detail = &d
	img := image.NewNRGBA(image.Rect(0, 0, 400, 200))
	for _, u := range []string{"b.png", fullImagePrefix + "b.png"} {
		imageStore[u] = &imageEntry{img: img, cssW: 400}
	}
	defer func() { imageStore = map[string]*imageEntry{}; imageRenders = map[string][]string{} }()

	m = press(t, m, "i")
	g := m.gallery
	if g == nil || len(g.refs) != 3 {
		t.Fatalf("want 3 distinct images, got %+v", g)
	}
	view := m.View()
	if !strings.Contains(ansi.Strip(view), "Image 1 of 3") || !strings.Contains(view, "▀") {
		t.Fatalf("first image not shown:\n%s", ansi.Strip(view))
	}
	if n := strings.Count(view, "\n"); n != 29 {
		t.Fatalf("view should fill 30 rows, has %d", n+1)
	}
	m = press(t, m, "h")
	if m.gallery.idx != 2 {
		t.Fatalf("left from the first image should wrap to the last, got %d", m.gallery.idx)
	}
	m = press(t, m, "esc")
	if m.gallery != nil || m.detail == nil {
		t.Fatal("esc should close the viewer and stay on the PR")
	}
}
