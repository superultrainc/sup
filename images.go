package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Images render as half blocks: each cell is two stacked pixels (▀ with
// truecolor fg/bg), so they flow with text, scroll, and search like any line.

var (
	mdImageRe   = regexp.MustCompile(`!\[([^\]]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	htmlImgRe   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	imgSrcRe    = regexp.MustCompile(`(?i)\bsrc\s*=\s*["']([^"']+)["']`)
	imgAltRe    = regexp.MustCompile(`(?i)\balt\s*=\s*["']([^"']*)["']`)
	emojiImgRe  = regexp.MustCompile(`(?i)\bclass\s*=\s*["'][^"']*\bemoji\b`)
	maxImageMB  = 15
	imageMaxRow = 40
)

// imageRef is an image in markdown, with byte offsets so text around it can be split.
type imageRef struct {
	start, end int
	url, alt   string
}

func findImages(body string) []imageRef {
	var refs []imageRef
	for _, m := range mdImageRe.FindAllStringSubmatchIndex(body, -1) {
		refs = append(refs, imageRef{m[0], m[1], body[m[4]:m[5]], body[m[2]:m[3]]})
	}
	for _, m := range htmlImgRe.FindAllStringIndex(body, -1) {
		tag := body[m[0]:m[1]]
		src := imgSrcRe.FindStringSubmatch(tag)
		if src == nil {
			continue
		}
		alt := ""
		if a := imgAltRe.FindStringSubmatch(tag); a != nil {
			alt = a[1]
		}
		refs = append(refs, imageRef{m[0], m[1], src[1], alt})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].start < refs[j].start })
	return refs
}

// htmlImageSrcs lists the img sources in GitHub's rendered HTML, which for
// private repos are short-lived signed URLs; emoji images are skipped.
func htmlImageSrcs(html string) []string {
	var out []string
	for _, tag := range htmlImgRe.FindAllString(html, -1) {
		if emojiImgRe.MatchString(tag) {
			continue
		}
		if src := imgSrcRe.FindStringSubmatch(tag); src != nil {
			out = append(out, strings.ReplaceAll(src[1], "&amp;", "&"))
		}
	}
	return out
}

type imageEntry struct {
	img      image.Image
	kittyB64 string     // base64 PNG for kitty, built off the UI goroutine
	frames   []gifFrame // animated GIF frames after the first, kitty only
	delay    int        // first frame's delay in ms
	err      error
	loading  bool
}

// Keyed by the image's markdown URL, which is stable; only touched from Update.
var (
	imageStore   = map[string]*imageEntry{}
	imageRenders = map[string][]string{}
)

type imageJob struct {
	key, url string // key is the markdown URL; url is what we download
}

type imagesLoadedMsg struct {
	results map[string]imageEntry
}

// queueImages starts downloads for every image in the PR not yet loaded.
// retryFailed is set on fresh network data, whose signed URLs are new.
func queueImages(d *prDetail, retryFailed bool) tea.Cmd {
	if d == nil {
		return nil
	}
	var jobs []imageJob
	add := func(body, html string) {
		refs := findImages(cleanBody(body))
		srcs := htmlImageSrcs(html)
		for i, r := range refs {
			if e, ok := imageStore[r.url]; ok && (e.err == nil || !retryFailed) {
				continue
			}
			dl := r.url
			if len(srcs) == len(refs) {
				dl = srcs[i]
			}
			imageStore[r.url] = &imageEntry{loading: true}
			jobs = append(jobs, imageJob{r.url, dl})
		}
	}
	add(d.Body, d.BodyHTML)
	for _, c := range d.Comments.Nodes {
		add(c.Body, c.BodyHTML)
	}
	for _, r := range d.Reviews.Nodes {
		add(r.Body, r.BodyHTML)
	}
	for _, t := range d.ReviewThreads.Nodes {
		for _, c := range t.Comments.Nodes {
			add(c.Body, c.BodyHTML)
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	return func() tea.Msg {
		results := make(map[string]imageEntry, len(jobs))
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, j := range jobs {
			wg.Add(1)
			go func(j imageJob) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				e := loadImage(j)
				mu.Lock()
				results[j.key] = e
				mu.Unlock()
			}(j)
		}
		wg.Wait()
		return imagesLoadedMsg{results: results}
	}
}

func imageCachePath(key string) string {
	sum := sha1.Sum([]byte(key))
	return filepath.Join(cacheDir(), "img", hex.EncodeToString(sum[:]))
}

const maxImageDim = 1600

// finishImage bounds the decoded size and, in kitty mode, builds the payload.
func finishImage(img image.Image) (image.Image, string) {
	b := img.Bounds()
	if w, h := b.Dx(), b.Dy(); w > maxImageDim || h > maxImageDim {
		s := float64(maxImageDim) / float64(max(w, h))
		img = resample(img, max(int(float64(w)*s), 1), max(int(float64(h)*s), 1))
	}
	if !imagesKitty {
		return img, ""
	}
	return img, kittyEncode(img)
}

func loadImage(j imageJob) imageEntry {
	if name, ok := strings.CutPrefix(j.url, "demo:"); ok {
		if img := demoImage(name); img != nil {
			img, b64 := finishImage(img)
			return imageEntry{img: img, kittyB64: b64}
		}
		return imageEntry{err: fmt.Errorf("no demo image %q", name)}
	}
	path := imageCachePath(j.key)
	data, err := os.ReadFile(path)
	cached := err == nil
	if !cached {
		if data, err = downloadImage(j.url); err != nil {
			return imageEntry{err: err}
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return imageEntry{err: fmt.Errorf("unsupported image")}
	}
	if !cached {
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, data, 0644)
	}
	img, b64 := finishImage(img)
	e := imageEntry{img: img, kittyB64: b64}
	if imagesKitty {
		if g, err := gif.DecodeAll(bytes.NewReader(data)); err == nil && len(g.Image) > 1 {
			e.frames, e.delay = gifFrames(g)
		}
	}
	return e
}

type gifFrame struct {
	b64   string // PNG of only the region that changed
	x, y  int
	delay int // ms
}

const (
	maxGIFFrames = 300
	minGIFFrame  = 100 // ms; faster frames are dropped to keep the payload small
)

// gifFrames composites each frame onto the canvas, since GIF frames are often
// partial, and returns the frames after the first as changed regions, with
// the first frame's delay. Frames that change nothing extend the previous one.
func gifFrames(g *gif.GIF) ([]gifFrame, int) {
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	// shortcut: big GIFs stay still, since finishImage scales the first frame; scale frames too if one shows up.
	if bounds.Dx() > maxImageDim || bounds.Dy() > maxImageDim {
		return nil, 0
	}
	canvas, shown := image.NewRGBA(bounds), image.NewRGBA(bounds)
	delayMS := func(i int) int { return max(g.Delay[i]*10, 20) }
	first := delayMS(0)
	var out []gifFrame
	for i, f := range g.Image[:min(len(g.Image), maxGIFFrames)] {
		var prev *image.RGBA
		if g.Disposal[i] == gif.DisposalPrevious {
			prev = image.NewRGBA(bounds)
			copy(prev.Pix, canvas.Pix)
		}
		draw.Draw(canvas, f.Bounds(), f, f.Bounds().Min, draw.Over)
		last := &first
		if len(out) > 0 {
			last = &out[len(out)-1].delay
		}
		if i > 0 {
			if r := changedRect(shown, canvas); r.Empty() || *last < minGIFFrame {
				*last += delayMS(i)
			} else {
				out = append(out, gifFrame{kittyEncode(canvas.SubImage(r)), r.Min.X, r.Min.Y, delayMS(i)})
				copy(shown.Pix, canvas.Pix)
			}
		} else {
			copy(shown.Pix, canvas.Pix)
		}
		switch g.Disposal[i] {
		case gif.DisposalBackground:
			draw.Draw(canvas, f.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = prev
		}
	}
	return out, first
}

// changedRect is the smallest rectangle holding every pixel that differs.
func changedRect(a, b *image.RGBA) image.Rectangle {
	var r image.Rectangle
	w := a.Rect.Dx()
	for y := 0; y < a.Rect.Dy(); y++ {
		row := y * a.Stride
		for x := 0; x < w; x++ {
			i := row + x*4
			if a.Pix[i] != b.Pix[i] || a.Pix[i+1] != b.Pix[i+1] || a.Pix[i+2] != b.Pix[i+2] || a.Pix[i+3] != b.Pix[i+3] {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

// pruneImageCache drops cached images nobody has needed in a while.
func pruneImageCache() {
	dir := filepath.Join(cacheDir(), "img")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range entries {
		if info, err := de.Info(); err == nil && time.Since(info.ModTime()) > 14*24*time.Hour {
			os.Remove(filepath.Join(dir, de.Name()))
		}
	}
}

// rawFileURL maps a github.com blob or raw link to raw.githubusercontent.com,
// since github.com rejects API tokens and 404s for private repos.
func rawFileURL(u *url.URL) *url.URL {
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 4)
	if u.Host != "github.com" || len(parts) < 4 || (parts[2] != "blob" && parts[2] != "raw") {
		return u
	}
	return &url.URL{Scheme: "https", Host: "raw.githubusercontent.com", Path: "/" + parts[0] + "/" + parts[1] + "/" + parts[3]}
}

func downloadImage(raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("bad image url")
	}
	u = rawFileURL(u)
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	// Only GitHub's own hosts get the token; signed URLs carry their own auth.
	if u.Host == "github.com" || u.Host == "api.github.com" || u.Host == "raw.githubusercontent.com" {
		req.Header.Set("Authorization", "Bearer "+ghToken)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("image %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, int64(maxImageMB)<<20))
}

func handleImagesLoaded(msg imagesLoadedMsg) {
	for key, r := range msg.results {
		e := r
		imageStore[key] = &e
		for k := range imageRenders {
			if strings.HasPrefix(k, key+"\x00") {
				delete(imageRenders, k)
			}
		}
	}
}

// imageLines renders an image at most width cells wide, or a placeholder line.
func imageLines(ref imageRef, width int) []string {
	th := currentDiffTheme()
	muted := sty(th.muted, "", false, false)
	label := ref.alt
	if label == "" {
		label = filepath.Base(ref.url)
	}
	e := imageStore[ref.url]
	switch {
	case e == nil || e.loading:
		return []string{muted.Render("▣ " + truncate(label, width-12) + " · loading…")}
	case e.err != nil:
		return []string{muted.Render("▣ " + truncate(label, width-24) + " · image unavailable")}
	}
	if imagesKitty {
		return kittyImageLines(ref.url, e, width, imageMaxRow)
	}
	key := fmt.Sprintf("%s\x00%d", ref.url, width)
	if lines, ok := imageRenders[key]; ok {
		return lines
	}
	lines := halfBlocks(e.img, width, imageMaxRow, th)
	imageRenders[key] = lines
	return lines
}

// halfBlocks downsamples img by area averaging into rows of ▀ cells. Cells
// are about twice as tall as wide, so two stacked pixels keep the aspect.
func halfBlocks(img image.Image, maxCols, maxRows int, th diffTheme) []string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	cols := min(maxCols, w)
	rows := (h*cols/w + 1) / 2
	if rows > maxRows {
		rows = maxRows
		cols = max(w*rows*2/h, 1)
	}
	rows = max(rows, 1)
	pw, ph := cols, rows*2

	// Transparent pixels blend onto the page background.
	bgR, bgG, bgB := 13, 17, 23
	if glamourStyle == "light" {
		bgR, bgG, bgB = 255, 255, 255
	}
	px := make([][3]uint8, pw*ph)
	for y := 0; y < ph; y++ {
		y0, y1 := b.Min.Y+y*h/ph, b.Min.Y+max((y+1)*h/ph, y*h/ph+1)
		for x := 0; x < pw; x++ {
			x0, x1 := b.Min.X+x*w/pw, b.Min.X+max((x+1)*w/pw, x*w/pw+1)
			// Sample at most 4x4 points per cell; big screenshots stay fast.
			sx, sy := max((x1-x0)/4, 1), max((y1-y0)/4, 1)
			var r, g, bl, n uint32
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					cr, cg, cb, ca := img.At(xx, yy).RGBA()
					a := ca >> 8
					r += (cr>>8 + uint32(bgR)*(255-a)/255)
					g += (cg>>8 + uint32(bgG)*(255-a)/255)
					bl += (cb>>8 + uint32(bgB)*(255-a)/255)
					n++
				}
			}
			px[y*pw+x] = [3]uint8{uint8(min(r/n, 255)), uint8(min(g/n, 255)), uint8(min(bl/n, 255))}
		}
	}
	out := make([]string, rows)
	for row := 0; row < rows; row++ {
		var sb strings.Builder
		for x := 0; x < pw; x++ {
			t, u := px[(row*2)*pw+x], px[(row*2+1)*pw+x]
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", t[0], t[1], t[2], u[0], u[1], u[2])
		}
		sb.WriteString("\x1b[0m")
		out[row] = sb.String()
	}
	return out
}

// richBody is markdownLines with images rendered inline between text runs.
func richBody(body string, width int) []string {
	body = cleanBody(body)
	refs := findImages(body)
	if len(refs) == 0 {
		return markdownLines(body, width)
	}
	var out []string
	pos := 0
	for _, r := range refs {
		if r.start < pos {
			continue
		}
		if text := markdownLines(body[pos:r.start], width); len(text) > 0 {
			out = append(out, text...)
			out = append(out, "")
		}
		out = append(out, imageLines(r, width)...)
		out = append(out, "")
		pos = r.end
	}
	out = append(out, markdownLines(body[pos:], width)...)
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
