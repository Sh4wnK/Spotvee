// Package engine drives the color-sync loop: it fans a track color out to
// the selected devices, isolates per-device failures behind circuit breakers,
// dedupes repeated colors and records end-to-end apply latency.
package engine

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/devices"
	"github.com/Sh4wnK/Spotvee/internal/metrics"
)

// Options tune the engine.
type Options struct {
	// ApplyTimeout bounds a single fan-out to the devices.
	ApplyTimeout time.Duration
	// LatencySamples is the ring size for apply-latency percentiles.
	LatencySamples int
	// CycleInterval is the default palette-cycling interval.
	CycleInterval time.Duration
	// Breaker threshold/cooldown (see devices.BreakerOptions).
	Breaker devices.BreakerOptions
}

// DefaultOptions returns long-run-safe defaults.
func DefaultOptions() Options {
	return Options{
		ApplyTimeout:   10 * time.Second,
		LatencySamples: 4096,
		CycleInterval:  4 * time.Second,
		Breaker:        devices.DefaultBreakerOptions(),
	}
}

// DeviceStatus is the API-facing view of a managed device.
type DeviceStatus struct {
	devices.Device
	Breaker string `json:"breaker"`
	LastErr string `json:"last_error,omitempty"`
}

// Engine owns the selected-light set and applies colors to it.
type Engine struct {
	logger *slog.Logger
	ctrl   devices.Controller
	opts   Options

	mu        sync.Mutex
	devices   map[string]*managed
	last      *devices.Color // last successfully applied color
	latency   *metrics.Histogram
	ctx       context.Context
	cancel    context.CancelFunc
	cycleOn   bool
	cycleSeq  []devices.Color
	cycleIdx  int
	cycleStop chan struct{}
	cycleWG   sync.WaitGroup
}

type managed struct {
	devices.Device
	cb      *devices.CircuitBreaker
	lastErr string
}

// New creates an engine. It starts no goroutines until StartCycling is
// called and performs no network I/O.
func New(ctrl devices.Controller, logger *slog.Logger, opts Options) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.ApplyTimeout <= 0 {
		opts.ApplyTimeout = 10 * time.Second
	}
	if opts.LatencySamples <= 0 {
		opts.LatencySamples = 4096
	}
	if opts.CycleInterval <= 0 {
		opts.CycleInterval = 4 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		logger:  logger,
		ctrl:    ctrl,
		opts:    opts,
		devices: make(map[string]*managed),
		latency: metrics.NewHistogram(opts.LatencySamples),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// ReplaceSelection swaps the set of device IDs the engine drives. Unknown IDs
// are kept as "unavailable" entries (breaker closed) until Devices reports
// them; the watcher refreshes device metadata separately.
func (e *Engine) ReplaceSelection(ids []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	keep := make(map[string]*managed, len(ids))
	for _, id := range ids {
		if m, ok := e.devices[id]; ok {
			keep[id] = m
		} else {
			keep[id] = &managed{Device: devices.Device{ID: id}, cb: devices.NewBreaker(e.opts.Breaker)}
		}
	}
	e.devices = keep
}

// Selection returns the currently selected device IDs in stable order.
func (e *Engine) Selection() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.devices))
	for id := range e.devices {
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// Devices returns the status view of managed devices.
func (e *Engine) Devices() []DeviceStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]DeviceStatus, 0, len(e.devices))
	for _, m := range e.devices {
		out = append(out, DeviceStatus{Device: m.Device, Breaker: m.cb.State(), LastErr: m.lastErr})
	}
	sortDevices(out)
	return out
}

// UpdateDevices refreshes the local metadata (name/sku) for selected IDs.
func (e *Engine) UpdateDevices(found []devices.Device) {
	e.mu.Lock()
	defer e.mu.Unlock()
	byID := make(map[string]devices.Device, len(found))
	for _, d := range found {
		byID[d.ID] = d
	}
	for id, m := range e.devices {
		if d, ok := byID[id]; ok {
			m.Device = d
		}
	}
}

// Latency returns the apply-latency histogram summary (ms).
func (e *Engine) Latency() metrics.Summary {
	return e.latency.Summary()
}

// LatencyHistogram returns the histogram backing Latency so external
// exporters can render it.
func (e *Engine) LatencyHistogram() *metrics.Histogram { return e.latency }

// Apply pushes a color to every selected device concurrently, waiting for
// all acknowledgements (or the apply timeout). Identical consecutive colors
// are deduplicated. A failed device's error is isolated and its breaker is
// moved toward open so it can never stall the loop. Apply records the
// end-to-end latency (call entry to last ack) in the engine histogram.
// It reports the number of devices that acknowledged plus the first
// non-breaker error (nil when everything succeeded or nothing was selected).
func (e *Engine) Apply(ctx context.Context, c devices.Color) (int, error) {
	start := time.Now()
	selected := e.snapshot()
	if len(selected) == 0 {
		return 0, nil
	}

	e.mu.Lock()
	if e.last != nil && *e.last == c {
		e.mu.Unlock()
		return len(selected), nil // dedupe: nothing changed
	}
	e.mu.Unlock()

	applyCtx, cancel := context.WithTimeout(ctx, e.opts.ApplyTimeout)
	defer cancel()

	type result struct {
		ok      bool
		err     error
		isBreak bool
	}
	results := make(chan result, len(selected))
	now := time.Now()
	for _, m := range selected {
		m := m
		go func() {
			if !m.cb.Allow(now) {
				results <- result{isBreak: true}
				return
			}
			err := e.ctrl.SetColorRGB(applyCtx, m.SKU, m.ID, devices.Color{R: c.R, G: c.G, B: c.B})
			if err != nil {
				m.cb.Failure(time.Now())
				e.recordErr(m.ID, err)
				results <- result{err: err}
				return
			}
			m.cb.Success()
			e.recordErr(m.ID, nil)
			results <- result{ok: true}
		}()
	}

	acked := 0
	var firstErr error
	for range selected {
		r := <-results
		if r.ok {
			acked++
		}
		if r.err != nil && firstErr == nil && !r.isBreak {
			firstErr = r.err
		}
	}
	if acked > 0 {
		e.mu.Lock()
		cp := c
		e.last = &cp
		e.mu.Unlock()
		e.latency.Add(float64(time.Since(start).Milliseconds()))
	}
	return acked, firstErr
}

func (e *Engine) snapshot() []*managed {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*managed, 0, len(e.devices))
	for _, m := range e.devices {
		if m.SKU == "" && m.Name == "" {
			// Device not yet resolved via Devices(); still try to drive it.
			out = append(out, m)
			continue
		}
		out = append(out, m)
	}
	return out
}

func (e *Engine) recordErr(id string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if m, ok := e.devices[id]; ok {
		if err != nil {
			m.lastErr = err.Error()
		} else {
			m.lastErr = ""
		}
	}
}

// StartCycling enables palette cycling: the engine re-applies the palette
// colors one at a time at the given interval until StopCycling or shutdown.
// Called with an empty slice, it stops cycling.
func (e *Engine) StartCycling(colors []devices.Color, interval time.Duration) {
	e.stopCycling()
	e.mu.Lock()
	e.cycleSeq = append([]devices.Color(nil), colors...)
	e.cycleIdx = 0
	e.cycleOn = len(colors) > 0
	if interval <= 0 {
		interval = e.opts.CycleInterval
	}
	stop := make(chan struct{})
	e.cycleStop = stop
	e.cycleWG.Add(1)
	e.mu.Unlock()

	go func() {
		defer e.cycleWG.Done()
		defer func() {
			if r := recover(); r != nil {
				e.logger.Error("cycle goroutine panicked", "panic", r)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				e.cycleOnce()
			}
		}
	}()
}

func (e *Engine) cycleOnce() {
	e.mu.Lock()
	if !e.cycleOn || len(e.cycleSeq) == 0 {
		e.mu.Unlock()
		return
	}
	c := e.cycleSeq[e.cycleIdx%len(e.cycleSeq)]
	e.cycleIdx++
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(e.ctx, e.opts.ApplyTimeout)
	defer cancel()
	acked, err := e.Apply(ctx, c)
	if acked == 0 && err != nil {
		e.logger.Debug("cycle apply skipped", "err", err)
	}
}

// stopCycling waits for the cycling goroutine (if any) to exit.
func (e *Engine) stopCycling() {
	e.mu.Lock()
	stop := e.cycleStop
	e.cycleStop = nil
	e.cycleOn = false
	e.cycleSeq = nil
	e.mu.Unlock()
	if stop != nil {
		close(stop)
		e.cycleWG.Wait()
	}
}

// StopCycling disables palette cycling.
func (e *Engine) StopCycling() {
	e.stopCycling()
}

// Close shuts the engine down, stopping cycling and cancelling the engine
// context.
func (e *Engine) Close() {
	e.stopCycling()
	e.cancel()
}

// SetLast clears the dedupe latch (used when the watcher resets).
func (e *Engine) SetLast(c *devices.Color) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.last = c
}

// CurrentColor returns the last applied color.
func (e *Engine) CurrentColor() *devices.Color {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

func sortStrings(s []string) {
	sort.Strings(s)
}

func sortDevices(s []DeviceStatus) {
	sort.Slice(s, func(i, j int) bool { return s[i].ID < s[j].ID })
}
