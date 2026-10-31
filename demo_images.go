package main

import (
	"image"
	"image/color"
	"math"
	"math/rand"
)

// Demo screenshots are drawn here, wireframe style, so the demo needs no network or image files.

type canvas struct{ *image.RGBA }

func rgb(hex uint32) color.RGBA {
	return color.RGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 255}
}

func (c canvas) rect(x, y, w, h int, col color.RGBA) {
	for j := max(y, 0); j < min(y+h, c.Rect.Dy()); j++ {
		for i := max(x, 0); i < min(x+w, c.Rect.Dx()); i++ {
			c.SetRGBA(i, j, col)
		}
	}
}

// round fills a rectangle with corners cut to radius r.
func (c canvas) round(x, y, w, h, r int, col color.RGBA) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			dx, dy := 0, 0
			if i < r {
				dx = r - i
			} else if i >= w-r {
				dx = i - (w - r - 1)
			}
			if j < r {
				dy = r - j
			} else if j >= h-r {
				dy = j - (h - r - 1)
			}
			if dx*dx+dy*dy <= r*r {
				c.SetRGBA(x+i, y+j, col)
			}
		}
	}
}

func (c canvas) dot(cx, cy, r int, col color.RGBA) { c.round(cx-r, cy-r, 2*r, 2*r, r, col) }

// text draws a placeholder line of "words" as rounded bars.
func (c canvas) text(x, y, w, h int, col color.RGBA, rng *rand.Rand) {
	for end := x + w; x < end; {
		ww := min(h*2+rng.Intn(h*5), end-x)
		c.round(x, y, ww, h, h/2, col)
		x += ww + h
	}
}

func (c canvas) line(x0, y0, x1, y1, thick int, col color.RGBA) {
	steps := max(abs(x1-x0), abs(y1-y0), 1)
	for s := 0; s <= steps; s++ {
		x := x0 + (x1-x0)*s/steps
		y := y0 + (y1-y0)*s/steps
		c.rect(x-thick/2, y-thick/2, thick, thick, col)
	}
}

type demoPalette struct{ bg, chrome, panel, line, text, muted, accent color.RGBA }

var (
	lightUI = demoPalette{rgb(0xf6f7f9), rgb(0xe8eaee), rgb(0xffffff), rgb(0xdfe2e7), rgb(0x3b4250), rgb(0xb5bbc6), rgb(0x3b82f6)}
	darkUI  = demoPalette{rgb(0x16181d), rgb(0x22252c), rgb(0x1d2026), rgb(0x2f333b), rgb(0xd5d9e0), rgb(0x5b616d), rgb(0x60a5fa)}
)

// window draws macOS window chrome and returns the content origin.
func (c canvas) window(x, y, w, h int, p demoPalette) (int, int) {
	c.round(x, y, w, h, 14, p.bg)
	c.round(x, y, w, 44, 14, p.chrome)
	c.rect(x, y+30, w, 14, p.chrome)
	for i, col := range []uint32{0xff5f57, 0xfebc2e, 0x28c840} {
		c.dot(x+24+i*22, y+22, 7, rgb(col))
	}
	return x, y + 44
}

func newCanvas(w, h int, bg color.RGBA) canvas {
	c := canvas{image.NewRGBA(image.Rect(0, 0, w, h))}
	c.rect(0, 0, w, h, bg)
	return c
}

func demoImage(name string) image.Image {
	rng := rand.New(rand.NewSource(int64(len(name)) * 7919))
	backdrop := rgb(0x8b9bb4)
	switch name {
	case "stats":
		c := newCanvas(1200, 760, backdrop)
		p := lightUI
		x, y := c.window(60, 40, 1080, 680, p)
		c.text(x+40, y+36, 220, 18, p.text, rng)
		for i := 0; i < 4; i++ { // range picker, "Today" selected
			col := p.chrome
			if i == 0 {
				col = p.accent
			}
			c.round(x+640+i*100, y+28, 92, 34, 8, col)
		}
		for i := 0; i < 3; i++ {
			c.round(x+40+i*340, y+96, 320, 120, 12, p.panel)
			c.text(x+64+i*340, y+124, 120, 12, p.muted, rng)
			c.round(x+64+i*340, y+156, 140+rng.Intn(60), 30, 6, p.text)
		}
		c.round(x+40, y+240, 1000, 360, 12, p.panel)
		for i := 0; i < 24; i++ {
			h := 60 + rng.Intn(240)
			col := p.accent
			if i < 18 {
				col = rgb(0xbfd5fb)
			}
			c.round(x+76+i*40, y+560-h, 26, h, 5, col)
		}
		return c
	case "darkmode":
		c := newCanvas(1200, 700, backdrop)
		for half, p := range []demoPalette{lightUI, darkUI} {
			x, y := c.window(40+half*580, 60, 540, 580, p)
			c.rect(x, y, 150, 536, p.chrome)
			for i := 0; i < 6; i++ {
				c.text(x+20, y+30+i*40, 100, 12, p.muted, rng)
			}
			c.text(x+180, y+30, 260, 20, p.text, rng)
			for i := 0; i < 3; i++ {
				c.round(x+180, y+90+i*140, 330, 120, 12, p.panel)
				c.text(x+204, y+118+i*140, 200, 12, p.text, rng)
				c.text(x+204, y+148+i*140, 260, 10, p.muted, rng)
				c.round(x+204, y+174+i*140, 90, 16, 8, p.accent)
			}
		}
		return c
	case "billing":
		c := newCanvas(1200, 720, rgb(0xeef1f5))
		p := lightUI
		c.text(80, 70, 300, 26, p.text, rng)
		c.round(80, 140, 1040, 500, 16, p.panel)
		for i := 0; i < 5; i++ {
			y := 180 + i*80
			if i == 1 { // the new "Next invoice" row
				c.round(100, y-14, 1000, 64, 10, rgb(0xe8f7ee))
				c.dot(132, y+18, 8, rgb(0x16a34a))
			}
			c.text(160, y+10, 260, 14, p.text, rng)
			c.text(820, y+10, 120, 14, p.muted, rng)
			c.rect(100, y+56, 1000, 2, p.line)
		}
		c.round(900, 580, 200, 44, 10, p.accent)
		return c
	case "keyboard":
		c := newCanvas(900, 1000, rgb(0xf2f2f7))
		p := lightUI
		for i := 0; i < 4; i++ {
			c.text(60, 80+i*44, 640+rng.Intn(120), 16, p.text, rng)
		}
		c.rect(0, 560, 900, 440, rgb(0xd1d4db))
		rows := []int{10, 9, 7}
		for r, n := range rows {
			off := (900 - n*84) / 2
			for k := 0; k < n; k++ {
				c.round(off+k*84+6, 600+r*104, 72, 88, 10, rgb(0xffffff))
			}
		}
		c.round(220, 912, 460, 72, 10, rgb(0xffffff))
		c.line(760, 650, 420, 650, 10, rgb(0xef4444)) // swipe gesture
		c.line(420, 650, 450, 620, 10, rgb(0xef4444))
		c.line(420, 650, 450, 680, 10, rgb(0xef4444))
		return c
	case "menubar":
		c := newCanvas(1200, 520, rgb(0x6d7fa3))
		c.rect(0, 0, 1200, 44, rgb(0x2a2d34))
		for i := 0; i < 6; i++ {
			c.round(900+i*44, 12, 22, 22, 6, rgb(0xc9ccd3))
		}
		c.dot(868, 22, 13, rgb(0xef4444)) // recording icon
		p := darkUI
		c.round(600, 60, 420, 300, 14, p.panel)
		for i := 0; i < 40; i++ {
			h := 10 + int(60*math.Abs(math.Sin(float64(i)/3)))*(60+rng.Intn(40))/100
			c.round(630+i*9, 200-h/2, 6, h, 3, rgb(0xef4444))
		}
		c.text(630, 270, 300, 14, p.text, rng)
		c.text(630, 300, 200, 12, p.muted, rng)
		return c
	case "meeting":
		c := newCanvas(1200, 760, backdrop)
		p := darkUI
		x, y := c.window(60, 40, 1080, 680, p)
		for i := 0; i < 110; i++ {
			h := 8 + int(70*math.Abs(math.Sin(float64(i)/5)*math.Cos(float64(i)/13)))
			c.round(x+40+i*9, y+90-h/2, 6, h, 3, p.accent)
		}
		speakers := []uint32{0xf59e0b, 0x22c55e, 0xa855f7}
		for i := 0; i < 6; i++ {
			yy := y + 170 + i*76
			c.dot(x+56, yy+12, 12, rgb(speakers[i%3]))
			c.text(x+84, yy, 140, 12, p.text, rng)
			c.text(x+84, yy+24, 700+rng.Intn(200), 10, p.muted, rng)
		}
		return c
	case "memory":
		c := newCanvas(1200, 640, rgb(0xffffff))
		p := lightUI
		for i := 0; i <= 5; i++ {
			c.rect(100, 80+i*90, 1020, 2, p.line)
		}
		c.text(100, 30, 260, 18, p.text, rng)
		prevB, prevA := 520, 500
		for i := 1; i <= 40; i++ {
			x0, x1 := 100+(i-1)*25, 100+i*25
			b := 520 - i*10 + rng.Intn(20) // before: climbs steadily
			a := 500 - rng.Intn(24)        // after: flat
			c.line(x0, prevB, x1, b, 5, rgb(0xef4444))
			c.line(x0, prevA, x1, a, 5, rgb(0x16a34a))
			prevB, prevA = b, a
		}
		return c
	case "landing":
		c := newCanvas(1200, 900, rgb(0xffffff))
		p := lightUI
		c.text(80, 40, 140, 18, p.text, rng)
		for i := 0; i < 4; i++ {
			c.text(700+i*110, 42, 80, 12, p.muted, rng)
		}
		c.round(220, 150, 760, 46, 12, rgb(0x111827))
		c.round(320, 214, 560, 46, 12, rgb(0x111827))
		c.text(300, 290, 600, 14, p.muted, rng)
		c.round(500, 340, 200, 52, 26, p.accent)
		c.round(140, 440, 920, 300, 18, rgb(0x1f2937))
		for i := 0; i < 50; i++ {
			h := 10 + rng.Intn(90)
			c.round(200+i*16, 590-h/2, 9, h, 4, rgb(0x60a5fa))
		}
		for i := 0; i < 3; i++ {
			c.round(140+i*315, 770, 290, 110, 14, p.bg)
			c.text(166+i*315, 800, 180, 12, p.text, rng)
			c.text(166+i*315, 830, 220, 10, p.muted, rng)
		}
		return c
	}
	return nil
}
