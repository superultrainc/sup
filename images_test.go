package main

import (
	"image"
	"image/color"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestFindImages(t *testing.T) {
	body := "Before\n![shot](https://github.com/user-attachments/assets/abc)\nmid <img width=\"702\" alt=\"Clean Shot\" src=\"https://github.com/user-attachments/assets/def\" />\nend"
	refs := findImages(body)
	if len(refs) != 2 {
		t.Fatalf("want 2 images, got %d", len(refs))
	}
	if refs[0].url != "https://github.com/user-attachments/assets/abc" || refs[0].alt != "shot" {
		t.Errorf("markdown image: %+v", refs[0])
	}
	if refs[1].url != "https://github.com/user-attachments/assets/def" || refs[1].alt != "Clean Shot" {
		t.Errorf("html image: %+v", refs[1])
	}
	if body[refs[0].start:refs[0].end] != "![shot](https://github.com/user-attachments/assets/abc)" {
		t.Errorf("offsets don't cover the markdown: %q", body[refs[0].start:refs[0].end])
	}
}

func TestHTMLImageSrcsSkipsEmoji(t *testing.T) {
	html := `<p><img class="emoji" src="https://x/emoji.png"> <a><img src="https://signed/a.png?jwt=1&amp;x=2"></a></p>`
	srcs := htmlImageSrcs(html)
	if len(srcs) != 1 || srcs[0] != "https://signed/a.png?jwt=1&x=2" {
		t.Fatalf("got %v", srcs)
	}
}

func TestHalfBlocksSize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	lines := halfBlocks(img, 80, 40, darkDiffTheme)
	// 400x200 at 80 cols is 80x40 pixels, i.e. 20 rows of two pixels.
	if len(lines) != 20 {
		t.Fatalf("want 20 rows, got %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 80 {
			t.Fatalf("row %d is %d wide", i, w)
		}
	}
	if got := halfBlocks(image.NewRGBA(image.Rect(0, 0, 100, 1000)), 80, 10, darkDiffTheme); len(got) != 10 {
		t.Fatalf("tall images should cap at maxRows, got %d rows", len(got))
	}
}
