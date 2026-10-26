// Package sync wires the playback watcher, artwork fetcher, palette
// extractor and light engine together: on each track change it computes the
// dominant color, pushes it to the selected devices and broadcasts the result
// to API/WebSocket subscribers.
package sync

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/art"
	"github.com/Sh4wnK/Spotvee/internal/devices"
	"github.com/Sh4wnK/Spotvee/internal/engine"
	"github.com/Sh4wnK/Spotvee/internal/palette"
	"github.com/Sh4wnK/Spotvee/internal/watcher"
)

// State is the current sync state, API/WS-facing.
type State struct {
	Track     TrackInfo `json:"track"`
	Color     ColorInfo `json:"color"`
	Syncing   bool      `json:"syncing"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TrackInfo describes the current track.
type TrackInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	ArtURL     string `json:"art_url"`
	DurationMS int64  `json:"duration_ms"`
}

// ColorInfo is the extracted palette view.
type ColorInfo struct {
	Dominant devices.Color   `json:"dominant"`
	Palette  []devices.Color `json:"palette"`
	Weights  []float64       `json:"weights"`
}

// Coordinator owns the sync pipeline.
type Coordinator struct {
	log   *slog.Logger
	art   *art.Fetcher
	eng   *engine.Engine
	opts  palette.Options
	cycle time.Duration

	mu      sync.RWMutex
	state   State
	subs    map[chan State]struct{}
	syncing bool
	cycleOn bool
}

// Options configure the coordinator.
type Options struct {
	CycleInterval time.Duration
	Palette       palette.Options
}

// New builds a coordinator.
func New(eng *engine.Engine, fetcher *art.Fetcher, log *slog.Logger, opts Options) *Coordinator {
	if log == nil {
		log = slog.Default()
	}
	if opts.CycleInterval <= 0 {
		opts.CycleInterval = 4 * time.Second
	}
	if opts.Palette.PaletteSize == 0 {
		opts.Palette = palette.DefaultOptions()
	}
	return &Coordinator{
		log:   log,
		art:   fetcher,
		eng:   eng,
		opts:  opts.Palette,
		cycle: opts.CycleInterval,
		subs:  make(map[chan State]struct{}),
	}
}

// SetPaletteOptions swaps extraction tuning.
func (c *Coordinator) SetPaletteOptions(opts palette.Options) {
	c.mu.Lock()
	c.opts = opts
	c.mu.Unlock()
}

// HandleTrack implements watcher.Handler: fetch art, extract color, push.
func (c *Coordinator) HandleTrack(ctx context.Context, ev watcher.Event) error {
	artBytes, err := c.art.Fetch(ctx, ev.ArtURL)
	if err != nil {
		c.log.Warn("artwork fetch failed", "track", ev.TrackID, "err", err)
		c.publish(ev, nil, err)
		return nil
	}

	c.mu.RLock()
	opts := c.opts
	engaged := c.syncing
	c.mu.RUnlock()

	p := palette.Palette{}
	if engaged {
		pal, perr := palette.Extract(artBytes, opts)
		if perr != nil {
			c.log.Warn("palette extraction failed", "track", ev.TrackID, "err", perr)
			p = palette.Palette{}
		} else {
			p = pal
		}
	}
	colors := make([]devices.Color, len(p.Colors))
	for i := range p.Colors {
		colors[i] = devices.Color(p.Colors[i])
	}
	weights := append([]float64(nil), p.Weights...)
	c.publish(ev, &palettePayload{Dominant: devices.Color(p.Dominant), Colors: colors, Weights: weights}, nil)

	if engaged && p.Dominant != (palette.RGB{}) && len(c.eng.Selection()) > 0 {
		acked, applyErr := c.eng.Apply(ctx, devices.Color(p.Dominant))
		if applyErr != nil {
			c.log.Warn("apply failed", "track", ev.TrackID, "acked", acked, "err", applyErr)
		}
	}

	c.mu.RLock()
	cycleOn := c.cycleOn
	c.mu.RUnlock()
	if engaged && cycleOn && len(colors) > 1 {
		c.eng.StartCycling(colors, c.cycle)
	} else {
		c.eng.StopCycling()
	}
	return nil
}

type palettePayload struct {
	Dominant devices.Color
	Colors   []devices.Color
	Weights  []float64
}

func (c *Coordinator) publish(ev watcher.Event, pal *palettePayload, fetchErr error) {
	var dom devices.Color
	var colors []devices.Color
	var weights []float64
	if pal != nil {
		dom = devices.Color(pal.Dominant)
		for _, pc := range pal.Colors {
			colors = append(colors, devices.Color(pc))
		}
		weights = pal.Weights
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := State{
		Track: TrackInfo{
			ID:         ev.TrackID,
			Name:       ev.Name,
			Artist:     ev.Artist,
			Album:      ev.Album,
			ArtURL:     ev.ArtURL,
			DurationMS: ev.DurationMS,
		},
		Color:     ColorInfo{Dominant: dom, Palette: colors, Weights: weights},
		Syncing:   c.syncing,
		UpdatedAt: time.Now().UTC(),
	}
	if fetchErr != nil {
		st.Error = fetchErr.Error()
	}
	c.state = st
	for ch := range c.subs {
		select {
		case ch <- st:
		default:
		}
	}
}

// SetSyncing enables/disables color sync and palette cycling.
func (c *Coordinator) SetSyncing(on, cycle bool) {
	c.mu.Lock()
	c.syncing = on
	c.cycleOn = on && cycle
	if !on {
		c.eng.StopCycling()
	}
	c.state.Syncing = on
	for ch := range c.subs {
		select {
		case ch <- c.state:
		default:
		}
	}
	c.mu.Unlock()
}

// State returns a snapshot of the current sync state.
func (c *Coordinator) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Subscribe registers a channel receiving state updates. Slow subscribers
// are dropped rather than stalling the pipeline.
func (c *Coordinator) Subscribe() chan State {
	ch := make(chan State, 8)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (c *Coordinator) Unsubscribe(ch chan State) {
	c.mu.Lock()
	delete(c.subs, ch)
	c.mu.Unlock()
}

// SelectAndSync sets the device selection.
func (c *Coordinator) SelectAndSync(ids []string) {
	c.eng.ReplaceSelection(ids)
}

// Selection returns the currently selected device IDs.
func (c *Coordinator) Selection() []string { return c.eng.Selection() }

// Syncing reports whether color sync is enabled.
func (c *Coordinator) Syncing() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.syncing
}

// Engine exposes the underlying engine.
func (c *Coordinator) Engine() *engine.Engine { return c.eng }

// Cycle returns the palette-cycling interval.
func (c *Coordinator) Cycle() time.Duration { return c.cycle }
