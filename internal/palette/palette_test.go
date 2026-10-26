package palette

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
)

// encodePNG renders img to PNG bytes.
func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestSolidColorExtraction(t *testing.T) {
	cases := []color.RGBA{
		{R: 0xff, G: 0x00, B: 0x00, A: 0xff},
		{R: 0x00, G: 0xff, B: 0x00, A: 0xff},
		{R: 0x12, G: 0x34, B: 0x56, A: 0xff},
		{R: 0xfe, G: 0xdc, B: 0xba, A: 0xff},
	}
	opts := DefaultOptions()
	for _, c := range cases {
		p, err := Extract(encodePNG(t, solidImage(64, 64, c)), opts)
		if err != nil {
			t.Fatalf("extract %v: %v", c, err)
		}
		if len(p.Colors) != 1 {
			t.Fatalf("%v: want 1 palette color, got %d", c, len(p.Colors))
		}
		if p.Colors[0] != (RGB{c.R, c.G, c.B}) {
			t.Errorf("%v: palette color = %v, want %v", c, p.Colors[0], c)
		}
		if p.Dominant != (RGB{c.R, c.G, c.B}) {
			t.Errorf("%v: dominant = %v, want %v", c, p.Dominant, c)
		}
		if p.Weights[0] < 0.99 {
			t.Errorf("%v: weight = %v, want ~1", c, p.Weights[0])
		}
	}
}

func TestTwoToneExtraction(t *testing.T) {
	// Left half vivid red, right half vivid blue.
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := range 100 {
		for x := range 200 {
			if x < 100 {
				img.SetRGBA(x, y, color.RGBA{R: 0xe0, G: 0x10, B: 0x20, A: 0xff})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 0x10, G: 0x40, B: 0xe0, A: 0xff})
			}
		}
	}
	p, err := Extract(encodePNG(t, img), DefaultOptions())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(p.Colors) != 2 {
		t.Fatalf("want 2 palette colors, got %d: %v", len(p.Colors), p.Colors)
	}
	// Both swatches should have ~0.5 weight.
	for i, s := range p.Weights {
		if math.Abs(s-0.5) > 0.01 {
			t.Errorf("swatch %d weight = %v, want ~0.5", i, s)
		}
	}
	want := map[RGB]bool{
		{R: 0xe0, G: 0x10, B: 0x20}: true,
		{R: 0x10, G: 0x40, B: 0xe0}: true,
	}
	for _, c := range p.Colors {
		if !want[c] {
			t.Errorf("unexpected palette color %v", c)
		}
	}
	// Dominant must be one of the two source colors, and both are vivid.
	if p.Dominant != p.Colors[0] && p.Dominant != p.Colors[1] {
		t.Errorf("dominant %v not in palette", p.Dominant)
	}
}

func TestGrayVsVividDominant(t *testing.T) {
	// 80% gray (low saturation) + 20% vivid green. The vivid color should win.
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := range 100 {
		for x := range 100 {
			if x < 80 {
				img.SetRGBA(x, y, color.RGBA{R: 0x88, G: 0x88, B: 0x88, A: 0xff})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 0x00, G: 0xe0, B: 0x10, A: 0xff})
			}
		}
	}
	p, err := Extract(encodePNG(t, img), DefaultOptions())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	green := RGB{R: 0x00, G: 0xe0, B: 0x10}
	if p.Dominant != green {
		t.Errorf("dominant = %v, want vivid green %v (palette %v)", p.Dominant, green, p.Colors)
	}
}

func TestBadImage(t *testing.T) {
	if _, err := Extract([]byte("not an image"), DefaultOptions()); err == nil {
		t.Fatal("expected error for garbage input")
	}
}

func TestEmptyImage(t *testing.T) {
	// A 1x1 image downscales to exactly 0x0 under our guard inputs; instead
	// verify the "empty" path with bounds that produce no samples.
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{A: 0x00}) // fully transparent
	img.SetRGBA(1, 0, color.RGBA{A: 0x00})
	img.SetRGBA(0, 1, color.RGBA{A: 0x00})
	img.SetRGBA(1, 1, color.RGBA{A: 0x00})
	p, err := Extract(encodePNG(t, img), DefaultOptions())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(p.Colors) != 0 {
		t.Fatalf("transparent image should yield empty palette, got %v", p.Colors)
	}
}

func TestQuantizeEdgeCases(t *testing.T) {
	if got := Quantize(nil, 4); got != nil {
		t.Fatalf("nil input should return nil, got %v", got)
	}
	samples := []sample{
		{c: RGB{255, 0, 0}, w: 3},
		{c: RGB{0, 255, 0}, w: 1},
		{c: RGB{0, 0, 255}, w: 1},
	}
	sw := Quantize(samples, 4) // fewer samples than target: one swatch each
	if len(sw) != 3 {
		t.Fatalf("want 3 swatches, got %d", len(sw))
	}
	if sw[0].Color != (RGB{255, 0, 0}) || sw[0].Weight != 0.6 {
		t.Errorf("first swatch = %v weight %v, want red 0.6", sw[0].Color, sw[0].Weight)
	}
}
