package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestLoadImageDownscales(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	imagesKitty = true
	defer func() { imagesKitty = false }()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 4000, 3000))); err != nil {
		t.Fatal(err)
	}
	j := imageJob{key: "big", url: "x"}
	path := imageCachePath(j.key)
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, buf.Bytes(), 0644)
	img, b64, err := loadImage(j)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() > 1600 || b.Dy() > 1600 || b.Dx() < 1500 {
		t.Fatalf("bad size %v", b)
	}
	if b64 == "" {
		t.Fatal("want kitty payload")
	}
}

func TestPruneImageCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := filepath.Join(cacheDir(), "img")
	os.MkdirAll(dir, 0755)
	oldF, newF := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	os.WriteFile(oldF, nil, 0644)
	os.WriteFile(newF, nil, 0644)
	past := time.Now().Add(-15 * 24 * time.Hour)
	os.Chtimes(oldF, past, past)
	pruneImageCache()
	if _, err := os.Stat(oldF); err == nil {
		t.Fatal("old file should be pruned")
	}
	if _, err := os.Stat(newF); err != nil {
		t.Fatal("new file should stay")
	}
}
