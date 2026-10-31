package main

import (
	"image"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestKittyImageLines(t *testing.T) {
	cellPxW, cellPxH = 10, 20
	kittyPending, kittyIDs, kittyNextID = nil, map[string]int{}, 1

	img := image.NewNRGBA(image.Rect(0, 0, 800, 400))
	lines := kittyImageLines("u", &imageEntry{img: img}, 60, 40)
	// 800px at 10px/cell caps at 60 cols (600px wide), so 300px tall is 15 rows.
	if len(lines) != 15 {
		t.Fatalf("want 15 rows, got %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 60 {
			t.Fatalf("row %d is %d cells wide", i, w)
		}
		if !strings.HasPrefix(l, "\x1b[38;2;0;0;1m") {
			t.Fatalf("row %d should carry image id 1 in its foreground color", i)
		}
	}
	if len(kittyPending) != 1 {
		t.Fatalf("want one queued transmission, got %d", len(kittyPending))
	}
	tx := kittyPending[0]
	if !strings.HasPrefix(tx, "\x1b_Ga=T,U=1,f=100,t=d,i=1,c=60,r=15,q=2,") || !strings.HasSuffix(tx, "\x1b\\") {
		t.Fatalf("unexpected transmission header: %.60q", tx)
	}

	// Same image and size reuses the transmission.
	kittyImageLines("u", &imageEntry{img: img}, 60, 40)
	if len(kittyPending) != 1 {
		t.Fatalf("re-rendering the same size should not retransmit")
	}
}

func TestKittyFreesOldImagesAndResends(t *testing.T) {
	defer func(b int) {
		kittyBudget, kittyPending, kittyIDs, kittyImages, kittyBytes = b, nil, map[string]int{}, map[int]*kittyImage{}, 0
	}(kittyBudget)
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100)) // 40 KB decoded
	kittyBudget = 100 << 10
	for _, key := range []string{"a", "b", "c"} {
		kittyTick++
		kittyImageLines(key, &imageEntry{img: img, kittyB64: "x"}, 60, 40)
	}
	if _, ok := kittyIDs["a\x0010x5"]; ok || len(kittyImages) != 2 || kittyBytes > kittyBudget {
		t.Fatalf("oldest image should be freed, have %d images, %d bytes", len(kittyImages), kittyBytes)
	}
	if !strings.Contains(strings.Join(kittyPending, ""), "a=d,d=I,i=") {
		t.Fatal("freed image should be deleted in the terminal")
	}
	// Images used in the current update are never freed, even over budget.
	kittyTick++
	for _, key := range []string{"d", "e", "f"} {
		kittyImageLines(key, &imageEntry{img: img, kittyB64: "x"}, 60, 40)
	}
	for _, key := range []string{"d", "e", "f"} {
		if _, ok := kittyIDs[key+"\x0010x5"]; !ok {
			t.Fatalf("%s was freed while in use", key)
		}
	}
}
