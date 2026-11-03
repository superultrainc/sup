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
	"strconv"
	"strings"
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
	imgWidthRe  = regexp.MustCompile(`(?i)\bwidth\s*=\s*["']?(\d+)["']?(?:\s|/|>)`)
	emojiImgRe  = regexp.MustCompile(`(?i)\bclass\s*=\s*["'][^"']*\bemoji\b`)
	maxImageMB  = 15
	imageMaxRow = 40
)

// imageRef is an image in markdown, with byte offsets so text around it can be split.
type imageRef struct {
	start, end int
	url, alt   string
	width      int // the img tag's width in CSS px, 0 if unset
}

func findImages(body string) []imageRef {
	var refs []imageRef
	for _, m := range mdImageRe.FindAllStringSubmatchIndex(body, -1) {
		refs = append(refs, imageRef{m[0], m[1], body[m[4]:m[5]], body[m[2]:m[3]], 0})
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
		width := 0
		if w := imgWidthRe.FindStringSubmatch(tag); w != nil {
			width, _ = strconv.Atoi(w[1])
		}
		refs = append(refs, imageRef{m[0], m[1], src[1], alt, width})
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
	cssW     int        // width GitHub draws it at, in CSS px
	frames   []gifFrame // animated GIF frames after the first, kitty only
	delay    int        // first frame's delay in ms
	err      error
	loading  bool
}

// Keyed by the image's markdown URL, which is stable; only touched from Update.
var (
	imageStore   = map[string]*imageEntry{}
	imageRenders = map[string][]string{}
	imageSources = map[string]string{} // markdown URL -> download URL, which may be signed
)

type imageJob struct {
	key, url string // key is the markdown URL; url is what we download
	width    int    // img tag width, 0 if unset
	full     bool   // for the viewer: natural size, not GitHub's
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
			imageSources[r.url] = dl
			jobs = append(jobs, imageJob{key: r.url, url: dl, width: r.width})
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
	// One message per image, so each shows as soon as it's ready instead of
	// waiting on the slowest download in the PR.
	cmds := make([]tea.Cmd, len(jobs))
	for i, j := range jobs {
		j := j
		cmds[i] = func() tea.Msg {
			imageSem <- struct{}{}
			defer func() { <-imageSem }()
			return imagesLoadedMsg{results: map[string]imageEntry{j.key: loadImage(j)}}
		}
	}
	return tea.Batch(cmds...)
}

var imageSem = make(chan struct{}, 4) // concurrent image loads

func imageCachePath(key string) string {
	sum := sha1.Sum([]byte(key))
	return filepath.Join(cacheDir(), "img", hex.EncodeToString(sum[:]))
}

const (
	maxImageDim     = 1600
	githubBodyWidth = 840 // CSS px; GitHub's comment column shrinks wider images to this
)

// cssWidth is how wide GitHub draws an image: its width attribute, else its
// own width, never wider than the comment column.
func cssWidth(attr, natural int) int {
	if attr > 0 {
		natural = attr
	}
	return min(natural, githubBodyWidth)
}

// payloadSize is the pixel size worth keeping: scale times the CSS width
// (2 for Retina sharpness), never upscaled, at most maxImageDim on a side.
func payloadSize(b image.Rectangle, cssW, scale int) (int, int) {
	w, h := b.Dx(), b.Dy()
	tw := min(w, scale*cssW)
	th := max(h*tw/max(w, 1), 1)
	if s := max(tw, th); s > maxImageDim {
		tw, th = max(tw*maxImageDim/s, 1), max(th*maxImageDim/s, 1)
	}
	return tw, th
}

// finishImage scales img to w x h and, in kitty mode, builds the payload.
func finishImage(img image.Image, w, h int) (image.Image, string) {
	img = resample(img, w, h)
	if !imagesKitty {
		return img, ""
	}
	return img, kittyEncode(img)
}

func loadImage(j imageJob) imageEntry {
	var data []byte
	var img image.Image
	if name, ok := strings.CutPrefix(j.url, "demo:"); ok {
		if img = demoImage(name); img == nil {
			return imageEntry{err: fmt.Errorf("no demo image %q", name)}
		}
	} else {
		path := imageCachePath(j.key)
		var err error
		data, err = os.ReadFile(path)
		cached := err == nil
		if !cached {
			if data, err = downloadImage(j.url); err != nil {
				return imageEntry{err: err}
			}
		}
		if img, _, err = image.Decode(bytes.NewReader(data)); err != nil {
			return imageEntry{err: fmt.Errorf("unsupported image")}
		}
		if !cached {
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, data, 0644)
		}
	}
	var anim *gif.GIF
	if imagesKitty && data != nil {
		if g, err := gif.DecodeAll(bytes.NewReader(data)); err == nil && len(g.Image) > 1 {
			anim = g
		}
	}
	cssW := cssWidth(j.width, img.Bounds().Dx())
	scale := 2
	if anim != nil {
		scale = 1 // the terminal keeps every frame decoded
	}
	if j.full {
		cssW, scale = img.Bounds().Dx(), 1
	}
	tw, th := payloadSize(img.Bounds(), cssW, scale)
	img, b64 := finishImage(img, tw, th)
	e := imageEntry{img: img, kittyB64: b64, cssW: cssW}
	if anim != nil {
		e.frames, e.delay = gifFrames(anim, tw, th)
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
	minGIFFrame  = 100      // ms; faster frames are dropped to keep the payload small
	gifMemory    = 24 << 20 // decoded bytes per GIF; longer GIFs drop frames evenly
)

// gifFrames composites each frame onto the canvas, since GIF frames are often
// partial, scales it to w x h, and returns the frames after the first as
// changed regions, with the first frame's delay. Frames that change nothing
// extend the previous one.
func gifFrames(g *gif.GIF, w, h int) ([]gifFrame, int) {
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	canvas := image.NewRGBA(bounds)
	scaled := func() *image.RGBA {
		if w == bounds.Dx() && h == bounds.Dy() {
			return canvas
		}
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(out, out.Rect, resample(canvas, w, h), image.Point{}, draw.Src)
		return out
	}
	shown := image.NewRGBA(image.Rect(0, 0, w, h))
	delayMS := func(i int) int { return max(g.Delay[i]*10, 20) }
	total := 0
	for i := range g.Image[:min(len(g.Image), maxGIFFrames)] {
		total += delayMS(i)
	}
	gap := max(minGIFFrame, total/max(gifMemory/(w*h*4), 2))
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
		if i == 0 {
			copy(shown.Pix, scaled().Pix)
		} else if *last < gap {
			*last += delayMS(i)
		} else if cur := scaled(); changedRect(shown, cur).Empty() {
			*last += delayMS(i)
		} else {
			r := changedRect(shown, cur)
			out = append(out, gifFrame{kittyEncode(cur.SubImage(r)), r.Min.X, r.Min.Y, delayMS(i)})
			copy(shown.Pix, cur.Pix)
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

// imageCols is how many cells wide GitHub's size for the image is, at most maxCols.
func imageCols(e *imageEntry, maxCols int) int {
	w := e.cssW
	if w == 0 {
		w = e.img.Bounds().Dx()
	}
	return min(maxCols, max((w+cellPxW-1)/cellPxW, 1))
}

// imageLines renders an image at most width cells wide, or a placeholder line.
func imageLines(ref imageRef, width int) []string {
	return imageLinesSized(ref, width, imageMaxRow)
}

func imageLabel(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return filepath.Base(u.Path)
	}
	return filepath.Base(raw)
}

// imageLinesSized renders an image in at most width x maxRows cells.
func imageLinesSized(ref imageRef, width, maxRows int) []string {
	th := currentDiffTheme()
	muted := sty(th.muted, "", false, false)
	label := ref.alt
	if label == "" {
		label = imageLabel(strings.TrimPrefix(ref.url, fullImagePrefix))
	}
	e := imageStore[ref.url]
	switch {
	case e == nil || e.loading:
		return []string{muted.Render("▣ " + truncate(label, width-12) + " · loading…")}
	case e.err != nil:
		return []string{muted.Render("▣ " + truncate(label, width-24) + " · image unavailable")}
	}
	if imagesKitty {
		return kittyImageLines(ref.url, e, width, maxRows)
	}
	cols := imageCols(e, width)
	key := fmt.Sprintf("%s\x00%dx%d", ref.url, cols, maxRows)
	if lines, ok := imageRenders[key]; ok {
		return lines
	}
	lines := halfBlocks(e.img, cols, maxRows, th)
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

// richBody is markdownLines with images rendered inline between text runs,
// and tables of images laid out side by side as GitHub shows them.
func richBody(body string, width int) []string {
	body = cleanBody(body)
	if len(findImages(body)) == 0 {
		return markdownLines(body, width)
	}
	var out []string
	for _, c := range splitImageTables(body) {
		lines := inlineImages(c.text, width)
		if c.table != nil {
			lines = imageTableLines(c.table, width)
		}
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	return out
}

// inlineImages renders markdown with each image on its own lines.
func inlineImages(body string, width int) []string {
	var out []string
	pos := 0
	refs := findImages(body)
	for i := 0; i < len(refs); i++ {
		r := refs[i]
		if r.start < pos {
			continue
		}
		if text := markdownLines(body[pos:r.start], width); len(text) > 0 {
			out = append(out, text...)
			out = append(out, "")
		}
		// Images separated only by spaces or a single newline sit side by side, as on GitHub.
		row := []imageRef{r}
		for i+1 < len(refs) && strings.TrimSpace(body[row[len(row)-1].end:refs[i+1].start]) == "" &&
			!strings.Contains(body[row[len(row)-1].end:refs[i+1].start], "\n\n") {
			i++
			row = append(row, refs[i])
		}
		out = append(out, imageRow(row, width)...)
		out = append(out, "")
		pos = row[len(row)-1].end
	}
	out = append(out, markdownLines(body[pos:], width)...)
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
