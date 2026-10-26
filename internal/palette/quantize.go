package palette

import (
	"math"
	"sort"
)

// Quantize reduces samples to at most n representative colors using median
// cut in RGB space: the box with the widest channel range is repeatedly split
// at the median until n boxes remain. Weights are normalized to sum to 1 and
// results are sorted by weight descending. An empty input returns nil.
func Quantize(samples []sample, n int) []Swatch {
	if len(samples) == 0 {
		return nil
	}
	if n < 1 {
		n = 1
	}
	if len(samples) <= n {
		total := 0.0
		for _, s := range samples {
			total += s.w
		}
		sw := make([]Swatch, 0, len(samples))
		for _, s := range samples {
			sw = append(sw, Swatch{Color: s.c, Weight: s.w / total})
		}
		sort.Slice(sw, func(i, j int) bool { return sw[i].Weight > sw[j].Weight })
		return sw
	}

	boxes := []*box{{samples: samples}}
	for len(boxes) < n {
		idx, ok := widest(boxes)
		if !ok {
			break
		}
		left, right := split(boxes[idx])
		if left == nil || right == nil {
			break
		}
		boxes[idx] = left
		boxes = append(boxes, right)
	}

	total := 0.0
	for _, b := range boxes {
		total += b.weight()
	}
	sw := make([]Swatch, 0, len(boxes))
	for _, b := range boxes {
		s := b.average()
		if total > 0 {
			s.Weight /= total
		}
		sw = append(sw, s)
	}
	sort.Slice(sw, func(i, j int) bool { return sw[i].Weight > sw[j].Weight })
	return sw
}

type box struct {
	samples []sample
}

func (b *box) weight() float64 {
	w := 0.0
	for _, s := range b.samples {
		w += s.w
	}
	return w
}

func (b *box) average() Swatch {
	var r, g, bl, w float64
	for _, s := range b.samples {
		r += float64(s.c.R) * s.w
		g += float64(s.c.G) * s.w
		bl += float64(s.c.B) * s.w
		w += s.w
	}
	if w <= 0 {
		return Swatch{}
	}
	return Swatch{
		Color: RGB{
			R: uint8(math.Round(r / w)),
			G: uint8(math.Round(g / w)),
			B: uint8(math.Round(bl / w)),
		},
		Weight: w,
	}
}

// widest picks the box with the largest single-channel range, nudged by log
// weight so a large low-contrast region still splits before a tiny
// high-contrast one.
func widest(boxes []*box) (int, bool) {
	best := -1
	bestScore := -1.0
	for i, b := range boxes {
		if len(b.samples) <= 1 {
			continue
		}
		minR, minG, minB := uint8(255), uint8(255), uint8(255)
		maxR, maxG, maxB := uint8(0), uint8(0), uint8(0)
		for _, s := range b.samples {
			minR = min(minR, s.c.R)
			minG = min(minG, s.c.G)
			minB = min(minB, s.c.B)
			maxR = max(maxR, s.c.R)
			maxG = max(maxG, s.c.G)
			maxB = max(maxB, s.c.B)
		}
		spread := max(int(maxR)-int(minR), int(maxG)-int(minG), int(maxB)-int(minB))
		score := float64(spread) + math.Log2(b.weight()+1)
		if score > bestScore {
			bestScore = score
			best = i
		}
	}
	return best, best >= 0
}

// split divides a box along its widest channel at the channel median. It
// returns (nil, nil) when the box cannot be divided (all pixels identical).
func split(b *box) (*box, *box) {
	s := b.samples
	minR, minG, minB := uint8(255), uint8(255), uint8(255)
	maxR, maxG, maxB := uint8(0), uint8(0), uint8(0)
	for _, p := range s {
		minR = min(minR, p.c.R)
		minG = min(minG, p.c.G)
		minB = min(minB, p.c.B)
		maxR = max(maxR, p.c.R)
		maxG = max(maxG, p.c.G)
		maxB = max(maxB, p.c.B)
	}
	if int(maxR)-int(minR) == 0 && int(maxG)-int(minG) == 0 && int(maxB)-int(minB) == 0 {
		return nil, nil
	}
	ch := 0
	if int(maxG)-int(minG) > int(maxR)-int(minR) && int(maxG)-int(minG) > int(maxB)-int(minB) {
		ch = 1
	} else if int(maxB)-int(minB) > int(maxR)-int(minR) && int(maxB)-int(minB) > int(maxG)-int(minG) {
		ch = 2
	}
	median := channelMedian(s, ch)
	var left, right []sample
	for _, p := range s {
		if channelVal(p.c, ch) <= median {
			left = append(left, p)
		} else {
			right = append(right, p)
		}
	}
	if len(left) == 0 || len(right) == 0 {
		return nil, nil
	}
	return &box{samples: left}, &box{samples: right}
}

func channelVal(c RGB, ch int) int {
	switch ch {
	case 1:
		return int(c.G)
	case 2:
		return int(c.B)
	default:
		return int(c.R)
	}
}

func channelMedian(s []sample, ch int) int {
	vals := make([]int, len(s))
	for i := range s {
		vals[i] = channelVal(s[i].c, ch)
	}
	sort.Ints(vals)
	return vals[len(vals)/2]
}
