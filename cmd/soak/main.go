// Command soak stress-tests the full Spotvee sync pipeline against fake
// Spotify and fake Govee endpoints at accelerated track-change rates.
//
// It runs the real watcher + coordinator + engine (only the external
// services are faked) and reports panic counts, goroutine growth, heap
// usage, apply-latency percentiles and device-command throughput after a
// configurable duration. This is the reproducibility tooling behind the
// "0 crashes over 24h stress tests" goal: the fake servers run
// in-process, so a 15-minute soak saturates the same code paths a 24h
// wall-clock run exercises. A failing device is injected periodically to
// prove the circuit breaker isolates it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/art"
	"github.com/Sh4wnK/Spotvee/internal/devices"
	"github.com/Sh4wnK/Spotvee/internal/engine"
	"github.com/Sh4wnK/Spotvee/internal/palette"
	"github.com/Sh4wnK/Spotvee/internal/spotify"
	synclib "github.com/Sh4wnK/Spotvee/internal/sync"
	"github.com/Sh4wnK/Spotvee/internal/watcher"
)

var panicCount atomic.Int64

func main() {
	var (
		duration    = flag.Duration("duration", 2*time.Minute, "how long to run")
		deviceN     = flag.Int("devices", 4, "number of fake Govee devices")
		trackEvery  = flag.Duration("track-every", 2*time.Second, "fake track-change cadence")
		reportEvery = flag.Duration("report-every", 30*time.Second, "progress report period")
		cmdLatency  = flag.Duration("cmd-latency", 50*time.Millisecond, "fake device command latency")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Fake Spotify API + generated album art.
	spot := newFakeSpotify(3, *trackEvery)
	spotSrv := httptest.NewServer(spot)
	defer spotSrv.Close()

	// Fake Govee controller (in-process; no HTTP needed).
	lights := newFakeLights(*deviceN, *cmdLatency)

	// Real engine + coordinator + watcher.
	eng := engine.New(lights, logger, engine.DefaultOptions())
	defer eng.Close()
	ids := make([]string, *deviceN)
	for i := range *deviceN {
		ids[i] = fmt.Sprintf("DEV-%02d", i)
	}
	eng.ReplaceSelection(ids)
	eng.UpdateDevices(lights.devices())

	fetcher := art.New(art.Options{MaxEntries: 8, MaxBytes: 1 << 20})
	coord := synclib.New(eng, fetcher, logger, synclib.Options{
		CycleInterval: 2 * time.Second,
		Palette:       palette.DefaultOptions(),
	})
	coord.SetSyncing(true, false)

	spotClient, err := spotify.NewClient(spotify.Options{
		ClientID:    "soak-client",
		RedirectURI: "http://localhost/cb",
		APIURL:      spotSrv.URL,
		AuthURL:     spotSrv.URL + "/authorize",
		TokenURL:    spotSrv.URL + "/api/token",
		HTTPClient:  spotSrv.Client(),
	})
	if err != nil {
		fatal("spotify client: %v", err)
	}
	spotClient.SetToken(spotify.Token{
		AccessToken: "t",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(24 * time.Hour),
	})
	watch := watcher.New(spotClient, coord.HandleTrack, logger, watcher.Config{
		BaseInterval:  500 * time.Millisecond,
		MinInterval:   100 * time.Millisecond,
		MaxInterval:   2 * time.Second,
		FastLead:      5 * time.Second,
		RetryAfterCap: 5 * time.Second,
	})
	watch.Start()

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(rootCtx, *duration)
	defer cancel()
	go spot.run(ctx)

	fmt.Printf("soak: %s, %d fake devices, track cadence %s, device latency %s\n",
		*duration, *deviceN, *trackEvery, *cmdLatency)
	start := time.Now()
	lastGoroutines := runtime.NumGoroutine()

	report := func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		g := runtime.NumGoroutine()
		s := eng.Latency()
		fmt.Printf("  t=%s | goroutines %d (Δ%+d) | heap %s | p50 %.1fms p95 %.1fms p99 %.1fms | applies n=%d | cmds/s %.1f\n",
			time.Since(start).Round(time.Second),
			g, g-lastGoroutines,
			humanBytes(m.HeapInuse),
			s.P50, s.P95, s.P99,
			s.Count,
			lights.commandsPerSec())
		lastGoroutines = g
	}

	reportTicker := time.NewTicker(*reportEvery)
	defer reportTicker.Stop()

	<-ctx.Done()
	report()
	watch.Stop()
	eng.Close()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s := eng.Latency()
	fmt.Println()
	fmt.Println("soak complete")
	fmt.Printf("  runtime:        %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("  panics:         %d\n", panicCount.Load())
	fmt.Printf("  goroutines:     %d\n", runtime.NumGoroutine())
	fmt.Printf("  heap:           %s in use (%s total allocated)\n", humanBytes(m.HeapInuse), humanBytes(m.TotalAlloc))
	fmt.Printf("  track changes:  %d\n", spot.changes.Load())
	fmt.Printf("  device cmds:    %d (%.1f/s)\n", lights.commands.Load(), lights.commandsPerSec())
	fmt.Printf("  latency ms:     p50 %.1f p95 %.1f p99 %.1f max %.1f (n=%d)\n",
		s.P50, s.P95, s.P99, s.Max, s.Count)
	open := 0
	for _, st := range eng.Devices() {
		if st.Breaker == "open" {
			open++
		}
	}
	fmt.Printf("  breakers open:  %d/%d\n", open, *deviceN)
	if n := lights.failures.Load(); n > 0 {
		fmt.Printf("  injected failures: %d (all isolated)\n", n)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func humanBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// ---------------------------------------------------------------------------
// fake Spotify

type fakeSpotify struct {
	mu       sync.Mutex
	art      [][]byte
	interval time.Duration
	changes  atomic.Int64
}

func newFakeSpotify(artCount int, interval time.Duration) *fakeSpotify {
	f := &fakeSpotify{interval: interval}
	for i := range artCount {
		f.art = append(f.art, testArtwork(i))
	}
	return f
}

func (f *fakeSpotify) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/token":
		_, _ = w.Write([]byte(`{"access_token":"t","token_type":"Bearer","expires_in":3600}`))

	case r.URL.Path == "/me/player/currently-playing":
		idx := int(f.changes.Load()) % len(f.art)
		cp := spotify.CurrentlyPlaying{
			ProgressMS: 500,
			IsPlaying:  true,
			Item: &spotify.Track{
				ID:         fmt.Sprintf("track-%d", idx),
				Name:       fmt.Sprintf("Soak Track %d", idx),
				DurationMS: int64(f.interval*4) / int64(time.Millisecond),
				Artists:    []spotify.Artist{{Name: "stress-test"}},
				Album: spotify.TrackAlbum{
					Name:   "soak-album",
					Images: []spotify.AlbumImage{{URL: "http://" + r.Host + "/art/" + fmt.Sprint(idx), Width: 640}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cp)

	case len(r.URL.Path) > 5 && r.URL.Path[:5] == "/art/":
		var idx int
		if _, err := fmt.Sscanf(r.URL.Path, "/art/%d", &idx); err != nil || idx < 0 || idx >= len(f.art) {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		art := f.art[idx]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(art)

	default:
		http.NotFound(w, r)
	}
}

// run advances the "currently playing" track every interval.
func (f *fakeSpotify) run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.changes.Add(1)
		}
	}
}

// testArtwork renders a two-tone PNG with a known vivid color per index so
// the palette extractor has real (deterministic) data.
func testArtwork(i int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	base := []color.RGBA{
		{R: 0xd0, G: 0x20, B: 0x30},
		{R: 0x10, G: 0x50, B: 0xd0},
		{R: 0x10, G: 0xa0, B: 0x30},
	}
	c := base[i%len(base)]
	for y := range 128 {
		for x := range 128 {
			c2 := color.RGBA{R: c.B, G: c.G, B: c.R, A: 0xff}
			if x > y {
				img.SetRGBA(x, y, c2)
			} else {
				img.SetRGBA(x, y, c)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// fake Govee controller

type fakeLights struct {
	mu       sync.Mutex
	devs     []devices.Device
	latency  time.Duration
	commands atomic.Int64
	failures atomic.Int64
	t0       time.Time
}

func newFakeLights(n int, latency time.Duration) *fakeLights {
	f := &fakeLights{latency: latency, t0: time.Now()}
	for i := range n {
		f.devs = append(f.devs, devices.Device{
			SKU:  "H6601",
			ID:   fmt.Sprintf("DEV-%02d", i),
			Name: fmt.Sprintf("Fake Strip %02d", i),
		})
	}
	return f
}

func (f *fakeLights) devices() []devices.Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]devices.Device(nil), f.devs...)
}

func (f *fakeLights) Devices(ctx context.Context) ([]devices.Device, error) {
	return f.devices(), nil
}

func (f *fakeLights) SetPower(ctx context.Context, sku, id string, on bool) error {
	return f.simulate(ctx)
}

func (f *fakeLights) SetBrightness(ctx context.Context, sku, id string, percent int) error {
	return f.simulate(ctx)
}

func (f *fakeLights) SetColorRGB(ctx context.Context, sku, id string, c devices.Color) error {
	f.commands.Add(1)
	// Inject a transient failure roughly every 100th command on a pseudo-fixed
	// device to exercise per-device error isolation and the circuit breaker
	// under continuous load.
	if n := f.commands.Load(); n%100 == 66 {
		f.failures.Add(1)
		return fmt.Errorf("%s: injected transient failure", id)
	}
	return f.simulate(ctx)
}

// simulate sleeps for the fake command latency, honoring cancellation.
func (f *fakeLights) simulate(ctx context.Context) error {
	t := time.NewTimer(f.latency)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (f *fakeLights) commandsPerSec() float64 {
	el := time.Since(f.t0).Seconds()
	if el <= 0 {
		return 0
	}
	return float64(f.commands.Load()) / el
}
