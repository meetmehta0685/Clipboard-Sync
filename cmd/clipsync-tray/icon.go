package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// icon draws the tray glyph: two offset sheets, the front one ruled, matching
// the app icon. Template icons are black on transparent (macOS tints them);
// the regular icon uses the brand ink for Windows and Linux trays.
func icon(template bool) []byte {
	const size = 44 // 22pt at 2x
	ink := color.NRGBA{0x1e, 0x36, 0x87, 0xff}
	if template {
		ink = color.NRGBA{0, 0, 0, 0xff}
	}
	back := ink
	back.A = 0x80
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	outline := func(x0, y0, x1, y1, w int, c color.NRGBA) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if x < x0+w || x >= x1-w || y < y0+w || y >= y1-w {
					img.SetNRGBA(x, y, c)
				}
			}
		}
	}
	fill := func(x0, y0, x1, y1 int, c color.NRGBA) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetNRGBA(x, y, c)
			}
		}
	}
	outline(14, 8, 38, 40, 3, back) // carbon sheet behind
	fill(8, 4, 32, 36, color.NRGBA{})
	outline(8, 4, 32, 36, 3, ink) // top sheet
	for _, y := range []int{14, 20, 26} {
		fill(13, y, 27, y+3, ink)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
