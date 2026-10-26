package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Sh4wnK/Spotvee/internal/devices"
	"github.com/Sh4wnK/Spotvee/internal/engine"
	"github.com/Sh4wnK/Spotvee/internal/metrics"
	"github.com/Sh4wnK/Spotvee/internal/palette"
	"github.com/Sh4wnK/Spotvee/internal/sync"
)

type fakeAuth struct {
	authd bool
	url   string
}

func (f *fakeAuth) Authenticated() bool { return f.authd }
func (f *fakeAuth) LoginURL() string    { return f.url }
func (f *fakeAuth) Exchange(ctx context.Context, state, code string) error {
	f.authd = true
	return nil
}

type fakeDevices struct {
	devs []devices.Device
	err  error
}

func (f *fakeDevices) ListDevices(ctx context.Context) ([]devices.Device, error) {
	return f.devs, f.err
}

type loggerNull struct{}

func (loggerNull) Write(p []byte) (int, error) { return len(p), nil }

func newTestServer(auth *fakeAuth, token string) (*Server, *engine.Engine, *fakeDevices) {
	logger := slog.New(slog.NewTextHandler(loggerNull{}, nil))
	ctrl := &nopCtrl{o: *auth}
	eng := engine.New(ctrl, logger, engine.DefaultOptions())
	coord := sync.New(eng, nil, logger, sync.Options{Palette: palette.DefaultOptions()})
	src := &fakeDevices{devs: []devices.Device{
		{SKU: "H6601", ID: "dev-1", Name: "strip"},
		{SKU: "H6104", ID: "dev-2", Name: "bulb"},
	}}
	s := New(Options{
		Logger:       logger,
		Auth:         auth,
		Coordinator:  coord,
		Engine:       eng,
		DeviceSource: src,
		AppToken:     token,
		Metrics:      metrics.NewAppMetrics(eng.LatencyHistogram()),
	})
	return s, eng, src
}

// nopCtrl implements devices.Controller for engine construction.
type nopCtrl struct{ o fakeAuth }

func (n *nopCtrl) Devices(ctx context.Context) ([]devices.Device, error) { return nil, nil }
func (n *nopCtrl) SetPower(ctx context.Context, sku, id string, on bool) error {
	return nil
}
func (n *nopCtrl) SetBrightness(ctx context.Context, sku, id string, percent int) error {
	return nil
}
func (n *nopCtrl) SetColorRGB(ctx context.Context, sku, id string, c devices.Color) error {
	return nil
}

func doReq(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthStatus(t *testing.T) {
	auth := &fakeAuth{authd: true}
	s, _, _ := newTestServer(auth, "")
	rec := doReq(t, s.Handler(), http.MethodGet, "/api/auth/status", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]bool
	json.Unmarshal(rec.Body.Bytes(), &body)
	if !body["authenticated"] {
		t.Fatal("expected authenticated=true")
	}
}

func TestLoginURL(t *testing.T) {
	auth := &fakeAuth{url: "https://accounts.spotify.com/authorize?x=1"}
	s, _, _ := newTestServer(auth, "")
	rec := doReq(t, s.Handler(), http.MethodGet, "/api/auth/login-url", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["url"] != auth.url {
		t.Fatalf("url = %q, want %q", body["url"], auth.url)
	}
}

func TestDevicesWithSelection(t *testing.T) {
	auth := &fakeAuth{}
	s, eng, _ := newTestServer(auth, "")
	eng.ReplaceSelection([]string{"dev-2"})
	rec := doReq(t, s.Handler(), http.MethodGet, "/api/devices", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var devs []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &devs)
	if len(devs) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devs))
	}
	if devs[1]["selected"] != true {
		t.Fatalf("dev-2 should be selected: %v", devs[1])
	}
}

func TestSelectDevices(t *testing.T) {
	auth := &fakeAuth{}
	s, eng, _ := newTestServer(auth, "")
	rec := doReq(t, s.Handler(), http.MethodPost, "/api/select", `{"ids":["dev-1"]}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if l := len(eng.Selection()); l != 1 || eng.Selection()[0] != "dev-1" {
		t.Fatalf("selection = %v, want [dev-1]", eng.Selection())
	}
}

func TestSyncToggle(t *testing.T) {
	auth := &fakeAuth{}
	s, _, _ := newTestServer(auth, "")
	rec := doReq(t, s.Handler(), http.MethodPost, "/api/sync", `{"enabled":true}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !s.coord.Syncing() {
		t.Fatal("syncing should be on after enable")
	}
}

func TestBearerTokenAuth(t *testing.T) {
	auth := &fakeAuth{}
	s, _, _ := newTestServer(auth, "sekrit")
	h := s.Handler()

	rec := doReq(t, h, http.MethodGet, "/api/state", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}
	rec = doReq(t, h, http.MethodGet, "/api/state", "", "sekrit")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 with token, got %d", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	auth := &fakeAuth{}
	s, eng, _ := newTestServer(auth, "")
	eng.ReplaceSelection([]string{"dev-1"})
	eng.UpdateDevices([]devices.Device{{SKU: "H6601", ID: "dev-1", Name: "strip"}})
	eng.Apply(context.Background(), devices.Color{R: 1, G: 2, B: 3})
	eng.Apply(context.Background(), devices.Color{R: 9, G: 9, B: 9})
	rec := doReq(t, s.Handler(), http.MethodGet, "/api/metrics", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if m["count"].(float64) != 2 {
		t.Fatalf("count = %v, want 2", m["count"])
	}

	rec = doReq(t, s.Handler(), http.MethodGet, "/metrics", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "spotvee_sync_latency_ms_p95") {
		t.Fatalf("prometheus output missing p95: %s", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	auth := &fakeAuth{}
	s, _, _ := newTestServer(auth, "")
	rec := doReq(t, s.Handler(), http.MethodGet, "/healthz", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestWebSocketPushesState(t *testing.T) {
	auth := &fakeAuth{}
	s, eng, _ := newTestServer(auth, "")
	sel := make(chan struct{})
	eng.ReplaceSelection([]string{"dev-1"})
	close(sel)
	_ = sel

	hs := httptest.NewServer(s.Handler())
	defer hs.Close()

	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/api/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Push a state update through the coordinator.
	s.coord.SetSyncing(true, false)

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var st struct {
		Syncing bool `json:"syncing"`
	}
	if err := json.Unmarshal(msg, &st); err != nil {
		t.Fatalf("bad ws json: %v", err)
	}
	if !st.Syncing {
		t.Fatalf("ws state syncing = %v, want true", st.Syncing)
	}
}
