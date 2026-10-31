package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Kitty graphics with Unicode placeholders: each image is transmitted once
// as a virtual placement, then drawn by placeholder characters whose
// foreground color names the image and whose diacritics give row/column.
// Placeholders are ordinary text, so images scroll and redraw with the TUI.
// See https://sw.kovidgoyal.net/kitty/graphics-protocol/#unicode-placeholders

var (
	imagesKitty         bool // set at startup by detectImageMode
	cellPxW, cellPxH    = 10, 20
	kittyPending        []string           // transmissions not yet written to the terminal
	kittyIDs            = map[string]int{} // url+size -> image id
	kittyNextID         = 1
	kittyFlushScheduled bool

	// Terminals silently drop old images past a memory limit (Rex keeps about
	// 128 MB), so sup frees its least recently used ones first and resends on demand.
	kittyImages    = map[int]*kittyImage{}
	kittyBytes     int
	kittyBudget    = 96 << 20
	kittyTick      int // bumped every Update; images used in this one are never freed
	kittyEvictions int // bumped on every free, so views holding placeholders re-render
)

type kittyImage struct {
	key   string
	bytes int // decoded size in the terminal
	used  int // kittyTick of last use
}

// detectImageMode asks the terminal whether it supports kitty graphics, before
// the TUI owns stdin. SUP_IMAGES=kitty|blocks overrides detection.
func detectImageMode() {
	switch os.Getenv("SUP_IMAGES") {
	case "kitty":
		imagesKitty = true
		return
	case "blocks":
		return
	}
	// WezTerm answers the graphics query but doesn't support placeholders.
	if strings.EqualFold(os.Getenv("TERM_PROGRAM"), "WezTerm") {
		return
	}
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return
	}
	old, err := term.MakeRaw(in)
	if err != nil {
		return
	}
	defer term.Restore(in, old)

	// Graphics query, cell size in pixels, then primary device attributes,
	// which every terminal answers last; reading until it arrives drains the replies.
	os.Stdout.WriteString("\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[16t\x1b[c")
	reply := readUntilDA1(in, 500*time.Millisecond)

	imagesKitty = strings.Contains(reply, "_Gi=31;OK")
	if m := regexp.MustCompile(`\x1b\[6;(\d+);(\d+)t`).FindStringSubmatch(reply); m != nil {
		h, _ := strconv.Atoi(m[1])
		w, _ := strconv.Atoi(m[2])
		if w > 0 && h > 0 {
			cellPxW, cellPxH = w, h
		}
	}
}

// graphemeModeSet records that we switched on mode 2027, to switch it off on exit.
var graphemeModeSet bool

// enableGraphemeWidths turns on grapheme clustering (mode 2027) where the terminal
// has it but leaves it off, so flags and joined emoji take the widths layout assumes.
func enableGraphemeWidths() {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return
	}
	old, err := term.MakeRaw(in)
	if err != nil {
		return
	}
	defer term.Restore(in, old)
	os.Stdout.WriteString("\x1b[?2027$p\x1b[c")
	if strings.Contains(readUntilDA1(in, 500*time.Millisecond), "\x1b[?2027;2$y") {
		os.Stdout.WriteString("\x1b[?2027h")
		graphemeModeSet = true
	}
}

func restoreGraphemeWidths() {
	if graphemeModeSet {
		os.Stdout.WriteString("\x1b[?2027l")
	}
}

var da1Re = regexp.MustCompile(`\x1b\[\?[0-9;]*c`)

func readUntilDA1(fd int, timeout time.Duration) string {
	var buf []byte
	tmp := make([]byte, 1024)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && !da1Re.Match(buf) {
		var fds unix.FdSet
		fds.Set(fd)
		left := time.Until(deadline)
		tv := unix.NsecToTimeval(left.Nanoseconds())
		n, err := unix.Select(fd+1, &fds, nil, nil, &tv)
		if err != nil || n == 0 {
			continue
		}
		r, err := unix.Read(fd, tmp)
		if err != nil || r <= 0 {
			break
		}
		buf = append(buf, tmp[:r]...)
	}
	return string(buf)
}

// kittyFreeAll deletes every image sup transmitted; run on exit.
func kittyFreeAll() {
	if imagesKitty && kittyNextID > 1 {
		os.Stdout.WriteString("\x1b_Ga=d,d=A,q=2\x1b\\")
	}
}

// kittyImageLines lays out img in at most maxCols x maxRows cells and returns
// the placeholder rows, queueing the transmission the first time.
func kittyImageLines(key string, e *imageEntry, maxCols, maxRows int) []string {
	img, b64 := e.img, e.kittyB64
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	cols := min(maxCols, max((w+cellPxW-1)/cellPxW, 1))
	rows := max((h*cols*cellPxW/w+cellPxH-1)/cellPxH, 1)
	if rows > maxRows {
		rows = maxRows
		cols = max(w*rows*cellPxH/(h*cellPxW), 1)
	}

	sizeKey := fmt.Sprintf("%s\x00%dx%d", key, cols, rows)
	id, ok := kittyIDs[sizeKey]
	if ok {
		kittyImages[id].used = kittyTick
	} else {
		id = kittyNextID
		kittyNextID++
		kittyIDs[sizeKey] = id
		bytes := w * h * 4 * (1 + len(e.frames))
		if b64 == "" {
			tw, th := min(cols*cellPxW, w), min(rows*cellPxH, h)
			b64 = kittyEncode(resample(img, tw, th))
			bytes = tw * th * 4
		}
		kittyFree(bytes)
		kittyImages[id] = &kittyImage{sizeKey, bytes, kittyTick}
		kittyBytes += bytes
		kittyPending = append(kittyPending, kittyTransmit(id, b64, cols, rows))
		if len(e.frames) > 0 && b64 == e.kittyB64 {
			kittyPending = append(kittyPending, kittyAnimation(id, e))
		}
	}

	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	out := make([]string, rows)
	for r := 0; r < rows; r++ {
		var sb strings.Builder
		sb.WriteString(color)
		for c := 0; c < cols; c++ {
			sb.WriteString("\U0010EEEE")
			sb.WriteRune(kittyDiacritics[r])
			sb.WriteRune(kittyDiacritics[c])
		}
		sb.WriteString("\x1b[0m")
		out[r] = sb.String()
	}
	return out
}

// kittyFree deletes least recently used images until need more bytes fit the budget.
func kittyFree(need int) {
	for kittyBytes+need > kittyBudget {
		oldest := 0
		for id, im := range kittyImages {
			if im.used < kittyTick && (oldest == 0 || im.used < kittyImages[oldest].used) {
				oldest = id
			}
		}
		if oldest == 0 {
			return
		}
		im := kittyImages[oldest]
		delete(kittyImages, oldest)
		delete(kittyIDs, im.key)
		kittyBytes -= im.bytes
		kittyEvictions++
		kittyPending = append(kittyPending, fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", oldest))
	}
}

// kittyEncode returns img as a base64 PNG; kitty scales it into any placement box.
func kittyEncode(img image.Image) string {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// kittyTransmit wraps a base64 PNG in chunked APC commands that create a
// virtual placement (U=1) of cols x rows.
func kittyTransmit(id int, data string, cols, rows int) string {
	return kittyChunks(fmt.Sprintf("a=T,U=1,f=100,t=d,i=%d,c=%d,r=%d,q=2", id, cols, rows), data)
}

// kittyAnimation adds a GIF's later frames to image id and loops them in the terminal.
func kittyAnimation(id int, e *imageEntry) string {
	var sb strings.Builder
	for i, f := range e.frames {
		// Frame i+2 draws its changed region over frame i+1 (c=).
		sb.WriteString(kittyChunks(fmt.Sprintf("a=f,f=100,t=d,i=%d,x=%d,y=%d,c=%d,z=%d,q=2", id, f.x, f.y, i+1, f.delay), f.b64))
	}
	fmt.Fprintf(&sb, "\x1b_Ga=a,i=%d,r=1,z=%d,q=2\x1b\\", id, e.delay)
	fmt.Fprintf(&sb, "\x1b_Ga=a,i=%d,s=3,v=1,q=2\x1b\\", id)
	return sb.String()
}

// kittyChunks sends base64 data under the given keys, split as the protocol requires.
func kittyChunks(keys, data string) string {
	if data == "" {
		return ""
	}
	const chunk = 4096
	var sb strings.Builder
	for i := 0; i < len(data); i += chunk {
		more := 0
		if i+chunk < len(data) {
			more = 1
		}
		part := data[i:min(i+chunk, len(data))]
		if i == 0 {
			fmt.Fprintf(&sb, "\x1b_G%s,m=%d;%s\x1b\\", keys, more, part)
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", more, part)
		}
	}
	return sb.String()
}

// resample box-filters img down to w x h (never up).
func resample(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if w >= b.Dx() && h >= b.Dy() {
		return img
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	sw, sh := b.Dx(), b.Dy()
	for y := 0; y < h; y++ {
		y0, y1 := b.Min.Y+y*sh/h, b.Min.Y+max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := b.Min.X+x*sw/w, b.Min.X+max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, a, n uint32
			// At most 4x4 samples per output pixel keeps huge images fast.
			sx, sy := max((x1-x0)/4, 1), max((y1-y0)/4, 1)
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					cr, cg, cb, ca := img.At(xx, yy).RGBA()
					r, g, bl, a, n = r+cr, g+cg, bl+cb, a+ca, n+1
				}
			}
			// RGBA() is premultiplied; un-premultiply for NRGBA.
			i := out.PixOffset(x, y)
			if a == 0 {
				continue
			}
			out.Pix[i+0] = uint8(r * 255 / a)
			out.Pix[i+1] = uint8(g * 255 / a)
			out.Pix[i+2] = uint8(bl * 255 / a)
			out.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return out
}

type kittyFlushedMsg struct{ count int }

// scheduleKittyFlush drops sent transmissions once a frame carrying them has
// certainly been written (frames flush every ~16ms).
func scheduleKittyFlush() tea.Cmd {
	if len(kittyPending) == 0 || kittyFlushScheduled {
		return nil
	}
	kittyFlushScheduled = true
	n := len(kittyPending)
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return kittyFlushedMsg{count: n} })
}

func handleKittyFlushed(msg kittyFlushedMsg) {
	kittyPending = kittyPending[min(msg.count, len(kittyPending)):]
	kittyFlushScheduled = false
}
