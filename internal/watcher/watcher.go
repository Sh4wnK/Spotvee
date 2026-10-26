// Package watcher polls Spotify playback state at an adaptive interval and
// emits track-change events to the sync engine. It survives 204s (nothing
// playing), 429s (Retry-After honored) and transient network failures without
// crashing.
package watcher

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/spotify"
)

// Poller is what the watcher polls. The production implementation is
// *spotify.Client; tests use fakes.
type Poller interface {
	CurrentlyPlaying(ctx context.Context) (*spotify.CurrentlyPlaying, error)
}

// Config tunes polling.
type Config struct {
	BaseInterval time.Duration
	MinInterval  time.Duration
	MaxInterval  time.Duration
	// FastLead is the remaining-track-time window that triggers fast polling
	// near a track's end so the next track's color lands within a second.
	FastLead time.Duration
	// RetryAfterCap bounds how long a 429 backoff can last.
	RetryAfterCap time.Duration
}

// DefaultConfig returns long-run-safe defaults.
func DefaultConfig() Config {
	return Config{
		BaseInterval:  3 * time.Second,
		MinInterval:   500 * time.Millisecond,
		MaxInterval:   5 * time.Second,
		FastLead:      10 * time.Second,
		RetryAfterCap: 30 * time.Second,
	}
}

// Event is delivered to handlers on track changes.
type Event struct {
	TrackID    string
	Name       string
	Artist     string
	Album      string
	ArtURL     string
	DurationMS int64
	ProgressMS int64
	IsPlaying  bool
}

// Handler receives track-change events. Returning an error is logged but does
// not stop the watcher.
type Handler func(ctx context.Context, e Event) error

// Watcher runs the polling loop.
type Watcher struct {
	poller  Poller
	log     *slog.Logger
	cfg     Config
	handler Handler

	startMu   chan struct{}
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	lastDelay time.Duration
}

// New builds a watcher. Call Start to begin polling.
func New(poller Poller, h Handler, log *slog.Logger, cfg Config) *Watcher {
	if log == nil {
		log = slog.Default()
	}
	cfg = normalize(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	return &Watcher{
		poller:  poller,
		log:     log,
		cfg:     cfg,
		handler: h,
		startMu: make(chan struct{}, 1),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

func normalize(cfg Config) Config {
	if cfg.BaseInterval <= 0 {
		cfg.BaseInterval = 3 * time.Second
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = 500 * time.Millisecond
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = 5 * time.Second
	}
	if cfg.FastLead <= 0 {
		cfg.FastLead = 10 * time.Second
	}
	if cfg.RetryAfterCap <= 0 {
		cfg.RetryAfterCap = 30 * time.Second
	}
	return cfg
}

// Start launches the polling goroutine. Only the first call starts a loop;
// a stopped watcher cannot be restarted.
func (w *Watcher) Start() {
	w.startMu <- struct{}{}
	defer func() { <-w.startMu }()
	if w.closed {
		return
	}
	select {
	case <-w.done:
		return // already stopped; do not restart
	default:
	}
	go w.run()
}

// Stop ends the polling loop and waits for it to exit.
func (w *Watcher) Stop() {
	w.startMu <- struct{}{}
	defer func() { <-w.startMu }()
	if w.closed {
		return
	}
	w.closed = true
	w.cancel()
	<-w.done
}

func (w *Watcher) run() {
	defer close(w.done)

	var lastID string
	next := w.cfg.BaseInterval

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(next):
		}

		ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
		cp, err := w.poller.CurrentlyPlaying(ctx)
		cancel()
		now := time.Now()

		if err != nil {
			next = w.backoff(err)
			continue
		}

		if cp == nil || cp.Item == nil || cp.Item.ID == "" {
			// 204 or empty payload: nothing playing right now.
			lastID = ""
			next = w.cfg.BaseInterval
			continue
		}

		st := trackStateFor(cp, now)
		if cp.Item.ID != lastID {
			w.deliver(ctx, cp)
			lastID = cp.Item.ID
		}
		next = st.next(w.cfg)
	}
}

// trackStateFor snapshots the current playback timing.
func trackStateFor(cp *spotify.CurrentlyPlaying, now time.Time) trackState {
	return trackState{
		remaining: time.Duration(cp.Item.DurationMS-cp.ProgressMS) * time.Millisecond,
		playing:   cp.IsPlaying,
	}
}

type trackState struct {
	remaining time.Duration
	playing   bool
}

// next picks the adaptive poll interval: fast near a track's end, base rate
// otherwise.
func (st trackState) next(cfg Config) time.Duration {
	if st.remaining <= 0 {
		return cfg.BaseInterval
	}
	if st.remaining <= cfg.FastLead {
		return cfg.MinInterval
	}
	return cfg.BaseInterval
}

// backoff computes the next poll delay after an error. Retry-After is honored
// (capped); other errors back off linearly up to MaxInterval.
func (w *Watcher) backoff(err error) time.Duration {
	var rle *spotify.RateLimitError
	if errors.As(err, &rle) {
		delay := parseRetryAfter(rle.RetryAfter, w.cfg.BaseInterval)
		if delay > w.cfg.RetryAfterCap {
			delay = w.cfg.RetryAfterCap
		}
		w.log.Warn("spotify rate limited", "retry_after", delay.String(), "err", err)
		return delay
	}
	if w.lastDelay == 0 {
		w.lastDelay = w.cfg.BaseInterval
	}
	w.lastDelay = min(w.cfg.MaxInterval, w.lastDelay*2)
	w.log.Debug("poll error", "err", err)
	return w.lastDelay
}

func parseRetryAfter(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return fallback
}

func (w *Watcher) deliver(ctx context.Context, cp *spotify.CurrentlyPlaying) {
	ev := Event{
		TrackID:    cp.Item.ID,
		Name:       cp.Item.Name,
		Album:      cp.Item.Album.Name,
		DurationMS: cp.Item.DurationMS,
		ProgressMS: cp.ProgressMS,
		IsPlaying:  cp.IsPlaying,
	}
	if len(cp.Item.Artists) > 0 {
		ev.Artist = cp.Item.Artists[0].Name
	}
	ev.ArtURL = largestImage(cp.Item.Album.Images)
	if err := w.handler(w.ctx, ev); err != nil {
		w.log.Warn("track handler failed", "track", ev.TrackID, "err", err)
	}
}

func largestImage(images []spotify.AlbumImage) string {
	bestIdx := -1
	for i := range images {
		if images[i].URL == "" {
			continue
		}
		if bestIdx < 0 || images[i].Width > images[bestIdx].Width {
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return ""
	}
	return images[bestIdx].URL
}
