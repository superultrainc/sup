package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	e := loadImage(j)
	img, b64, err := e.img, e.kittyB64, e.err
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

func TestRawFileURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/o/r/blob/pr-assets/x/before/a.png?raw=true": "https://raw.githubusercontent.com/o/r/pr-assets/x/before/a.png",
		"https://github.com/o/r/raw/main/a.gif":                         "https://raw.githubusercontent.com/o/r/main/a.gif",
		"https://github.com/user-attachments/assets/abc":                "https://github.com/user-attachments/assets/abc",
		"https://example.com/o/r/blob/main/a.png":                       "https://example.com/o/r/blob/main/a.png",
	} {
		u, _ := url.Parse(in)
		if got := rawFileURL(u).String(); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

func TestAnimatedGIFLoops(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	imagesKitty = true
	defer func() { imagesKitty = false; kittyPending = nil }()
	pal := color.Palette{color.Black, color.White}
	g := &gif.GIF{Config: image.Config{Width: 20, Height: 20, ColorModel: pal}}
	for i := 0; i < 3; i++ {
		// Later frames cover only part of the canvas, as optimized GIFs do.
		f := image.NewPaletted(image.Rect(0, 0, 20-i*5, 20-i*5), pal)
		for p := range f.Pix {
			f.Pix[p] = uint8(i % 2)
		}
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, 10)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	j := imageJob{key: "anim", url: "x"}
	path := imageCachePath(j.key)
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, buf.Bytes(), 0644)
	e := loadImage(j)
	if e.err != nil || len(e.frames) != 2 || e.delay != 100 {
		t.Fatalf("want 2 more frames at 100ms, got %d, %d, %v", len(e.frames), e.delay, e.err)
	}
	kittyImageLines("anim", &e, 60, 40)
	out := strings.Join(kittyPending, "")
	if e.frames[1].b64 == "" || e.frames[1].x != 0 || !strings.Contains(out, "c=2") || !strings.Contains(out, "s=3") {
		t.Fatal("frames not sent or not looped")
	}
}
