package watcher

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/spotify"
)

// fakePoller serves canned responses, optionally with a hook for assertions.
type fakePoller struct {
	mu      sync.Mutex
	results []pollerResult
	idx     int
}

type pollerResult struct {
	cp  *spotify.CurrentlyPlaying
	err error
}

func (f *fakePoller) push(cp *spotify.CurrentlyPlaying, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, pollerResult{cp: cp, err: err})
}

func (f *fakePoller) CurrentlyPlaying(ctx context.Context) (*spotify.CurrentlyPlaying, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= len(f.results) {
		return nil, errors.New("no more fake results")
	}
	r := f.results[f.idx]
	f.idx++
	return r.cp, r.err
}

func (f *fakePoller) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.idx
}

func track(id string, progressMs, durationMs int64) *spotify.CurrentlyPlaying {
	return &spotify.CurrentlyPlaying{
		ProgressMS: progressMs,
		Item: &spotify.Track{
			ID:         id,
			Name:       "Track " + id,
			DurationMS: durationMs,
			Artists:    []spotify.Artist{{Name: "Artist"}},
			Album:      spotify.TrackAlbum{Name: "Album"},
		},
	}
}

type recordingHandler struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingHandler) handle(ctx context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingHandler) list() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func newTestWatcher(p Poller, h Handler) *Watcher {
	cfg := DefaultConfig()
	cfg.BaseInterval = 30 * time.Millisecond
	cfg.MinInterval = 10 * time.Millisecond
	cfg.MaxInterval = 100 * time.Millisecond
	return New(p, h, slog.New(slog.NewTextHandler(ioDiscard{}, nil)), cfg)
}

// ioDiscard is a tiny io.Writer used to silence watcher logs in tests.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestWatcherEmitsOnTrackChange(t *testing.T) {
	p := &fakePoller{}
	h := &recordingHandler{}
	p.push(track("a", 1000, 180000), nil)
	p.push(track("a", 2000, 180000), nil)
	p.push(track("b", 500, 180000), nil)
	w := newTestWatcher(p, h.handle)
	w.Start()
	defer w.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if evs := h.list(); len(evs) >= 2 {
			if evs[0].TrackID != "a" || evs[1].TrackID != "b" {
				t.Fatalf("events = %+v, want a then b", evs)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out; events = %+v", h.list())
}

func TestWatcherHandles204(t *testing.T) {
	p := &fakePoller{}
	h := &recordingHandler{}
	p.push(nil, nil) // 204 / nothing playing
	p.push(track("x", 0, 60000), nil)
	w := newTestWatcher(p, h.handle)
	w.Start()
	defer w.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if evs := h.list(); len(evs) == 1 {
			if evs[0].TrackID != "x" {
				t.Fatalf("event = %+v, want x", evs[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out; events = %+v", h.list())
}

func TestWatcherBacksOffOn429(t *testing.T) {
	p := &fakePoller{}
	h := &recordingHandler{}
	// 429 with Retry-After 1s, then a playable track.
	p.push(nil, &spotify.RateLimitError{RetryAfter: "1"})
	p.push(track("y", 0, 60000), nil)
	w := newTestWatcher(p, h.handle)
	w.Start()
	defer w.Stop()

	// The track should still be delivered; backoff only delays the retry.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if evs := h.list(); len(evs) == 1 && evs[0].TrackID == "y" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for track y; events = %+v", h.list())
}

func TestWatcherTransientErrorThenRecovers(t *testing.T) {
	p := &fakePoller{}
	h := &recordingHandler{}
	p.push(nil, errors.New("network boom"))
	p.push(track("z", 0, 60000), nil)
	w := newTestWatcher(p, h.handle)
	w.Start()
	defer w.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if evs := h.list(); len(evs) == 1 && evs[0].TrackID == "z" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out; events = %+v", h.list())
}

func TestWatcherStopStopsPolling(t *testing.T) {
	p := &fakePoller{}
	h := &recordingHandler{}
	p.push(track("q", 0, 60000), nil)
	w := newTestWatcher(p, h.handle)
	w.Start()
	defer w.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.count() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.Stop() // must not hang
}
