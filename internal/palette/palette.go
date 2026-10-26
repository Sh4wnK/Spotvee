// Package palette implements album-art color extraction: pure-Go image
// decoding/downscaling, a hybrid histogram + median-cut quantizer, and a
// vivid-dominant color picker.
package palette

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"sort"

	"golang.org/x/image/draw"
)

// maxSamples bounds the number of weighted samples fed to the quantizer.
const maxSamples = 20000

// RGB is a 24-bit color.
type RGB struct {
	R, G, B uint8
}

// Hex renders the color as a lowercase hex string like "ff0066".
func (c RGB) Hex() string {
	return fmt.Sprintf("%02x%02x%02x", c.R, c.G, c.B)
}

// HSV is hue (0..360), saturation (0..1), value (0..1).
type HSV struct {
	H, S, V float64
}

// ToHSV converts RGB to HSV.
func (c RGB) ToHSV() HSV {
	r := float64(c.R) / 255
	g := float64(c.G) / 255
	b := float64(c.B) / 255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	delta := max - min

	h := 0.0
	switch {
	case delta == 0:
	case max == r:
		h = math.Mod((g-b)/delta, 6)
		h *= 60
		if h < 0 {
			h += 360
		}
	case max == g:
		h = ((b-r)/delta + 2) * 60
	default:
		h = ((r-g)/delta + 4) * 60
	}
	s := 0.0
	if max > 0 {
		s = delta / max
	}
	return HSV{H: h, S: s, V: max}
}

// Swatch is a quantized color plus its fraction of total weight.
type Swatch struct {
	Color  RGB
	Weight float64
}

// Options tune extraction.
type Options struct {
	MaxDimension int     // downscale so the longest side is at most this
	PaletteSize  int     // number of palette entries
	SatFloor     float64 // min saturation for vivid candidates
	LumHigh      float64 // max relative luminance for vivid candidates
	LumLow       float64 // min relative luminance for vivid candidates
	VividWeight  float64 // blend between presence and luminance-anchor scores
	VividBase    float64 // luminance anchor for the vivid picker
}

// DefaultOptions returns server-default tuning.
func DefaultOptions() Options {
	return Options{
		MaxDimension: 256,
		PaletteSize:  4,
		SatFloor:     0.18,
		LumHigh:      0.86,
		LumLow:       0.12,
		VividWeight:  0.7,
		VividBase:    0.45,
	}
}

// Palette is the extraction result.
type Palette struct {
	Dominant RGB       `json:"dominant"`
	Colors   []RGB     `json:"colors"`
	Weights  []float64 `json:"weights"`
}

// Extract decodes data and returns the palette + dominant color.
func Extract(data []byte, opts Options) (Palette, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Palette{}, fmt.Errorf("palette: decode: %w", err)
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return Palette{}, fmt.Errorf("palette: empty image")
	}
	small := Downscale(img, opts.MaxDimension)
	swatches := Quantize(Samples(small), opts.PaletteSize)
	p := Palette{
		Colors:  make([]RGB, 0, len(swatches)),
		Weights: make([]float64, 0, len(swatches)),
	}
	for _, s := range swatches {
		p.Colors = append(p.Colors, s.Color)
		p.Weights = append(p.Weights, s.Weight)
	}
	p.Dominant = SelectDominant(p, opts)
	return p, nil
}

// Downscale area-averages img to maxDim on its longest side (no upscale).
func Downscale(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxDim <= 0 || (w <= maxDim && h <= maxDim) {
		return img
	}
	scale := float64(maxDim) / float64(max(w, h))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

type sample struct {
	c RGB
	w float64
}

// Samples converts a small bitmap into weighted, deduplicated samples.
func Samples(src image.Image) []sample {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	area := float64(w * h)
	stride := 1
	if area > maxSamples {
		stride = int(math.Ceil(area / maxSamples))
	}
	merged := make(map[RGB]float64, 4096)
	for y := 0; y < h; y += stride {
		for x := 0; x < w; x += stride {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if a == 0 {
				continue
			}
			merged[RGB{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}]++
		}
	}
	out := make([]sample, 0, len(merged))
	for c, n := range merged {
		out = append(out, sample{c: c, w: float64(n) * float64(stride*stride) / area})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].w > out[j].w })
	return out
}

// SelectDominant picks the vivid dominant color. A candidate must clear the
// saturation floor and luminance window; it is scored as a blend of presence
// (area x saturation) and closeness to the luminance anchor. If nothing
// qualifies, the highest-presence entry is returned.
func SelectDominant(p Palette, opts Options) RGB {
	if len(p.Colors) == 0 {
		return RGB{}
	}
	best := -1
	bestScore := -1.0
	fallback := 0
	bestFallback := -1.0
	for i := range p.Colors {
		c := p.Colors[i]
		hsv := c.ToHSV()
		presence := p.Weights[i] * (0.25 + 0.75*hsv.S)
		if presence > bestFallback {
			bestFallback = presence
			fallback = i
		}
		if hsv.S < opts.SatFloor {
			continue
		}
		lum := RelativeLuminance(c)
		if lum > opts.LumHigh || lum < opts.LumLow {
			continue
		}
		anchor := 1 - math.Abs(lum-opts.VividBase)
		score := presence*(1-opts.VividWeight) + anchor*opts.VividWeight
		if score > bestScore {
			bestScore = score
			best = i
		}
	}
	if best < 0 {
		return p.Colors[fallback]
	}
	return p.Colors[best]
}

// RelativeLuminance is Rec. 709 relative luminance in [0, 1].
func RelativeLuminance(c RGB) float64 {
	return 0.2126*float64(c.R)/255 + 0.7152*float64(c.G)/255 + 0.0722*float64(c.B)/255
}
