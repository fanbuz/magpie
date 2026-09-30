//go:build darwin && cgo

package gui

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The menu bar's image: the bird, then a cell for each card side by side,
// drawn at twice the points for a Retina screen, in the bar's text colour
// light or dark. MAGPIE_TRAY_PREVIEW=<dir> writes each as a PNG to look at.
func TestTrayImage(t *testing.T) {
	bird, err := os.ReadFile("tray.png")
	if err != nil {
		t.Fatal(err)
	}
	cells, _, _ := trayUsageView(trayCards(), time.Now(), false)
	out := os.Getenv("MAGPIE_TRAY_PREVIEW")
	widths := map[bool][]int{}
	for _, dark := range []bool{false, true} {
		for n := 0; n <= 3; n++ {
			b, w, h := trayImagePNG(cells[:n], bird, 22, 2, dark, false)
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%d cells: %v", n, err)
			}
			if h != 44 || img.Bounds().Dx() != w || img.Bounds().Dy() != h {
				t.Fatalf("%d cells: %dx%d px, image %v", n, w, h, img.Bounds())
			}
			widths[dark] = append(widths[dark], w)
			// the bird and each cell's digits are there, in the bar's text
			// colour: dark on the light bar, light on the dark one
			ink := inkIn(img, image.Rect(0, 0, 44, 44), dark)
			if ink < 40 {
				t.Errorf("%d cells, dark %v: the bird has %d px of ink", n, dark, ink)
			}
			// and the last card's, in what it adds to the width
			if n > 0 {
				if ink := inkIn(img, image.Rect(widths[dark][n-1], 0, w, h), dark); ink < 60 {
					t.Errorf("%d cells, dark %v: the last cell has %d px of ink", n, dark, ink)
				}
			}
			if out != "" {
				bg, _, _ := trayImagePNG(cells[:n], bird, 22, 2, dark, true)
				name := fmt.Sprintf("tray-%d-%s.png", n, map[bool]string{false: "light", true: "dark"}[dark])
				if err := os.WriteFile(filepath.Join(out, name), bg, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// each card widens it, by no more than a modest cell
	for dark, ws := range widths {
		if ws[0] > 52 {
			t.Errorf("dark %v: the bird alone is %d px", dark, ws[0])
		}
		for i := 1; i < len(ws); i++ {
			if d := ws[i] - ws[i-1]; d < 40 || d > 110 {
				t.Errorf("dark %v: cell %d widens it by %d px", dark, i, d)
			}
		}
	}
	if widths[false][3] != widths[true][3] {
		t.Errorf("light %d px, dark %d px", widths[false][3], widths[true][3])
	}

	// a logo that can't be read, or none, is the name's letter in a ring
	b, _, _ := trayImagePNG([]trayCell{{Icon: []byte("not a picture"), Letter: "Z", Rows: []string{"5%", "7%"}}, {Letter: "Q", Rows: []string{"$3"}}}, bird, 22, 2, false, false)
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if ink := inkIn(img, image.Rect(48, 8, 48+28, 36), false); ink < 40 {
		t.Errorf("no letter where the logo can't be read: %d px", ink)
	}
}

// inkIn counts the pixels in r drawn in the bar's text colour.
func inkIn(img image.Image, r image.Rectangle, dark bool) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y && y < img.Bounds().Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X && x < img.Bounds().Max.X; x++ {
			c, g, b, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			lum := (c + g + b) / 3 * 0xffff / max(a, 1)
			if dark && lum > 0xc000 || !dark && lum < 0x4000 {
				n++
			}
		}
	}
	return n
}
