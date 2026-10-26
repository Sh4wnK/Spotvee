package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/Sh4wnK/Spotvee/internal/devices"
)

// fakeCtrl is an in-memory controller recording calls and failing on demand.
type fakeCtrl struct {
	mu         sync.Mutex
	devs       []devices.Device
	colors     []devices.Color
	failID     map[string]error // device id -> error to inject
	powerCalls int
}

func (f *fakeCtrl) Devices(ctx context.Context) ([]devices.Device, error) {
	return f.devs, nil
}

func (f *fakeCtrl) SetPower(ctx context.Context, sku, id string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.powerCalls++
	return nil
}

func (f *fakeCtrl) SetBrightness(ctx context.Context, sku, id string, percent int) error {
	return nil
}

func (f *fakeCtrl) SetColorRGB(ctx context.Context, sku, id string, c devices.Color) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failID[id]; err != nil {
		return err
	}
	f.colors = append(f.colors, c)
	return nil
}

func (f *fakeCtrl) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.colors)
}

func newTestEngine(c *fakeCtrl) (*Engine, *devices.Device) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := New(c, logger, DefaultOptions())
	d := &devices.Device{SKU: "H6601", ID: "dev-1", Name: "strip"}
	e.ReplaceSelection([]string{d.ID})
	e.UpdateDevices([]devices.Device{*d})
	return e, d
}

func TestEngineDedupesIdenticalColors(t *testing.T) {
	c := &fakeCtrl{}
	e, _ := newTestEngine(c)
	defer e.Close()

	col := devices.Color{R: 10, G: 20, B: 30}
	if acked, err := e.Apply(context.Background(), col); err != nil || acked != 1 {
		t.Fatalf("apply1: acked=%d err=%v", acked, err)
	}
	if acked, err := e.Apply(context.Background(), col); err != nil || acked != 1 {
		t.Fatalf("apply2: acked=%d err=%v", acked, err)
	}
	if got := c.calls(); got != 1 {
		t.Fatalf("controller saw %d calls, want 1 (dedupe)", got)
	}
	if acked, err := e.Apply(context.Background(), devices.Color{R: 10, G: 20, B: 31}); err != nil || acked != 1 {
		t.Fatalf("apply3: acked=%d err=%v", acked, err)
	}
	if got := c.calls(); got != 2 {
		t.Fatalf("controller saw %d calls, want 2 after new color", got)
	}
}

func TestEngineErrorIsolation(t *testing.T) {
	c := &fakeCtrl{failID: map[string]error{"bad": errors.New("network down")}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := New(c, logger, DefaultOptions())
	e.ReplaceSelection([]string{"good", "bad"})
	e.UpdateDevices([]devices.Device{
		{SKU: "H6601", ID: "good", Name: "good"},
		{SKU: "H6601", ID: "bad", Name: "bad"},
	})
	defer e.Close()

	acked, err := e.Apply(context.Background(), devices.Color{R: 1, G: 2, B: 3})
	if acked != 1 {
		t.Fatalf("acked = %d, want 1 (good device)", acked)
	}
	if err == nil {
		t.Fatal("expected error from bad device")
	}

	// The bad device's breaker should now be tripped; a second apply still
	// succeeds for the good device.
	acked, err = e.Apply(context.Background(), devices.Color{R: 1, G: 2, B: 4})
	if acked != 1 {
		t.Fatalf("apply2 acked = %d, want 1", acked)
	}
}

func TestEngineBreakerTripsAfterThreshold(t *testing.T) {
	c := &fakeCtrl{failID: map[string]error{"bad": errors.New("always failing")}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := New(c, logger, DefaultOptions())
	e.ReplaceSelection([]string{"bad"})
	e.UpdateDevices([]devices.Device{{SKU: "H6601", ID: "bad", Name: "bad"}})
	defer e.Close()

	for i := range DefaultOptions().Breaker.Threshold + 1 {
		e.Apply(context.Background(), devices.Color{R: uint8(i), G: 0, B: 0})
	}
	st := e.Devices()
	if len(st) != 1 {
		t.Fatalf("want 1 device status, got %d", len(st))
	}
	if st[0].Breaker == "closed" {
		t.Fatalf("breaker should be open after threshold failures, got %q", st[0].Breaker)
	}
}

func TestEngineLatencyRecorded(t *testing.T) {
	c := &fakeCtrl{}
	e, _ := newTestEngine(c)
	defer e.Close()
	e.Apply(context.Background(), devices.Color{R: 5, G: 5, B: 5})
	e.Apply(context.Background(), devices.Color{R: 6, G: 5, B: 5})
	s := e.Latency()
	if s.Count != 2 {
		t.Fatalf("latency count = %d, want 2", s.Count)
	}
	if s.P50 < 0 {
		t.Fatalf("p50 = %v, want >= 0", s.P50)
	}
}

func TestEngineNoSelectionIsNoop(t *testing.T) {
	c := &fakeCtrl{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := New(c, logger, DefaultOptions())
	defer e.Close()
	acked, err := e.Apply(context.Background(), devices.Color{R: 9, G: 9, B: 9})
	if acked != 0 || err != nil {
		t.Fatalf("acked=%d err=%v, want 0,nil", acked, err)
	}
	if c.calls() != 0 {
		t.Fatalf("controller saw %d calls, want 0", c.calls())
	}
}
