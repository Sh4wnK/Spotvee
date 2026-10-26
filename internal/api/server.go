// Package api exposes the HTTP + WebSocket surface for the mobile app:
// auth status, device listing/selection, sync control, live state and
// latency metrics.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Sh4wnK/Spotvee/internal/devices"
	"github.com/Sh4wnK/Spotvee/internal/engine"
	"github.com/Sh4wnK/Spotvee/internal/metrics"
	"github.com/Sh4wnK/Spotvee/internal/spotify"
	"github.com/Sh4wnK/Spotvee/internal/sync"
)

// upgrader is the shared WebSocket upgrader. Origin checks are relaxed: the
// mobile app is a native client with no browser origin.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 8192,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// AuthFlow implements the Spotify OAuth dance (see internal/spotify).
type AuthFlow interface {
	Authenticated() bool
	LoginURL() string
	Exchange(ctx context.Context, state, code string) error
}

// DeviceSource abstracts device discovery.
type DeviceSource interface {
	ListDevices(ctx context.Context) ([]devices.Device, error)
}

// Server handles all HTTP routes.
type Server struct {
	log          *slog.Logger
	auth         AuthFlow
	coord        *sync.Coordinator
	eng          *engine.Engine
	deviceSource DeviceSource
	tokenAuth    string
	metrics      *metrics.AppMetrics
}

// Options configures the API server.
type Options struct {
	Logger       *slog.Logger
	Auth         AuthFlow
	Coordinator  *sync.Coordinator
	Engine       *engine.Engine
	DeviceSource DeviceSource
	AppToken     string
	Metrics      *metrics.AppMetrics
}

// New builds the API server.
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Server{
		log:          o.Logger,
		auth:         o.Auth,
		coord:        o.Coordinator,
		eng:          o.Engine,
		deviceSource: o.DeviceSource,
		tokenAuth:    o.AppToken,
		metrics:      o.Metrics,
	}
}

// Handler returns the mux for the API, wrapped in auth + logging middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/status", s.withAuth(s.authStatus))
	mux.HandleFunc("/api/auth/login-url", s.withAuth(s.loginURL))
	mux.HandleFunc("/api/auth/callback", s.authCallback) // browser hits this directly
	mux.HandleFunc("/api/auth/refresh", s.withAuth(s.refresh))
	mux.HandleFunc("/api/devices", s.withAuth(s.devices))
	mux.HandleFunc("/api/select", s.withAuth(s.selectDevices))
	mux.HandleFunc("/api/sync", s.withAuth(s.syncControl))
	mux.HandleFunc("/api/state", s.withAuth(s.state))
	mux.HandleFunc("/api/ws", s.withAuth(s.ws))
	mux.HandleFunc("/api/metrics", s.withAuth(s.metricsJSON))
	mux.HandleFunc("/metrics", s.promMetrics)
	mux.HandleFunc("/healthz", s.health)
	return s.logRequests(mux)
}

// ---------------------------------------------------------------------------
// auth

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.auth != nil && s.auth.Authenticated()})
}

func (s *Server) loginURL(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		httpError(w, http.StatusServiceUnavailable, "no spotify auth configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": s.auth.LoginURL()})
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		httpError(w, http.StatusServiceUnavailable, "no spotify auth configured")
		return
	}
	// Forcing a refresh is optional; the token auto-refreshes on demand.
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.auth.Authenticated()})
}

func (s *Server) authCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if oauthErr := r.URL.Query().Get("error"); oauthErr != "" || code == "" {
		httpError(w, http.StatusBadRequest, "authorization failed: "+oauthErr)
		return
	}
	if err := s.auth.Exchange(r.Context(), state, code); err != nil {
		s.log.Warn("oauth exchange failed", "err", err)
		httpError(w, http.StatusUnauthorized, "authorization failed: "+err.Error())
		return
	}
	// The mobile app navigates to the login URL; after success Spotify
	// redirects here. Return a small page so a browser flow also works.
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<html><body><h2>Spotvee connected</h2><p>Return to the app.</p></body></html>"))
}

// ---------------------------------------------------------------------------
// devices

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	if s.deviceSource == nil {
		httpError(w, http.StatusServiceUnavailable, "no device source")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	list, err := s.deviceSource.ListDevices(ctx)
	if err != nil {
		s.log.Warn("list devices failed", "err", err)
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	selected := map[string]bool{}
	for _, id := range s.coord.Selection() {
		selected[id] = true
	}
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		out = append(out, map[string]any{
			"id":       d.ID,
			"sku":      d.SKU,
			"name":     d.Name,
			"selected": selected[d.ID],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) selectDevices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ids []string `json:"ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	s.coord.SelectAndSync(req.Ids)
	writeJSON(w, http.StatusOK, map[string]any{"selected": req.Ids})
}

func (s *Server) syncControl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
		Cycle   bool `json:"cycle"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	s.coord.SetSyncing(req.Enabled, req.Cycle)
	writeJSON(w, http.StatusOK, map[string]any{"syncing": req.Enabled, "cycle": req.Cycle})
}

// ---------------------------------------------------------------------------
// state + ws

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st := s.coord.State()
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Debug("ws upgrade failed", "err", err)
		return
	}
	defer func() { _ = conn.Close() }()

	ch := s.coord.Subscribe()
	defer s.coord.Unsubscribe(ch)

	// Push current state immediately so the app has data on connect.
	if st := s.coord.State(); !st.UpdatedAt.IsZero() {
		_ = conn.WriteJSON(st)
	}

	// Drain client messages so the connection stays healthy.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case st := <-ch:
			if err := conn.WriteJSON(st); err != nil {
				s.log.Debug("ws write failed", "err", err)
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// metrics

func (s *Server) metricsJSON(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		httpError(w, http.StatusServiceUnavailable, "metrics disabled")
		return
	}
	writeJSON(w, http.StatusOK, s.metrics.Summary())
}

func (s *Server) promMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		httpError(w, http.StatusNotFound, "metrics disabled")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write(s.metrics.Prometheus())
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// middleware + helpers

// withAuth wraps a handler with bearer-token auth when an app token is set.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	if s.tokenAuth == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) || h[len(prefix):] != s.tokenAuth {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).String())
	})
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ensure spotify package is linked (AuthFlow references it in docs only)
var _ = spotify.ScopePlaying
