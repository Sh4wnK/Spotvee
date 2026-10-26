// Package config loads Spotvee server configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds every runtime knob for the Spotvee server. All values are
// sourced from environment variables so secrets stay out of the repository.
type Config struct {
	// ListenAddr is the host:port the HTTP/WebSocket server binds to.
	ListenAddr string

	// AppToken is the bearer token the mobile app presents on every API
	// request. Optional; when empty the API runs unauthenticated.
	AppToken string

	// Spotify OAuth2 settings.
	SpotifyClientID     string
	SpotifyClientSecret string // optional when PKCE is used
	SpotifyRedirectURI  string

	// GoveeController selects which device backend to instantiate.
	// "govee" uses the shared Govee client; anything else is rejected.
	GoveeController string
	// GoveeAPIKey is the Govee developer API key.
	GoveeAPIKey string

	// TokenFile is where the encrypted Spotify token is persisted.
	TokenFile string
	// TokenKeyFile is where the 32-byte token-encryption key is stored
	// (0600). Create it only if missing; never overwrite an existing key.
	TokenKeyFile string

	// PollInterval is the default playback polling interval.
	PollInterval time.Duration
	// MinPollInterval / MaxPollInterval bound the adaptive watcher's
	// polling rate. Bounded memory: the limit exists to cap polling load.
	MinPollInterval time.Duration
	MaxPollInterval time.Duration
	// FastPollLead is how far (in milliseconds) of remaining track time the
	// watcher switches to accelerated polling.
	FastPollLead time.Duration

	// Sync settings.
	PaletteCycle bool

	// Image cache bounds.
	ImageCacheEntries int
	ImageCacheBytes   int64
	MaxArtBytes       int64

	// Palette overrides.
	PaletteSize     int
	Quantizer       string
	SatFloor        float64
	LumHigh         float64
	LumLow          float64
	VividWeight     float64
	HueSpread       float64
	VividFlip       bool
	VividBackground float64

	// Log level: "debug", "info", "warn" or "error".
	LogLevel string

	// MetricsExpose determines whether /metrics (Prometheus text) is served.
	MetricsExpose bool
}

// FromEnv builds a Config from the environment, applying defaults and
// validating the values that are mandatory or must fall in a sane range.
func FromEnv() (Config, error) {
	c := Config{
		ListenAddr:         getenv("SPOTVEE_LISTEN_ADDR", ":8080"),
		AppToken:           os.Getenv("SPOTVEE_APP_TOKEN"),
		SpotifyClientID:    os.Getenv("SPOTIFY_CLIENT_ID"),
		SpotifyRedirectURI: getenv("SPOTIFY_REDIRECT_URI", "http://localhost:8080/api/auth/callback"),
		GoveeController:    getenv("SPOTVEE_GOVEE_CONTROLLER", "govee"),
		GoveeAPIKey:        os.Getenv("GOVEE_API_KEY"),
		TokenFile:          getenv("SPOTVEE_TOKEN_FILE", "spotvee-state.json"),
		TokenKeyFile:       getenv("SPOTVEE_TOKEN_KEY_FILE", ".spotvee-token-key"),
		PollInterval:       durEnv("SPOTVEE_POLL_INTERVAL", 3*time.Second),
		MinPollInterval:    durEnv("SPOTVEE_MIN_POLL", 500*time.Millisecond),
		MaxPollInterval:    durEnv("SPOTVEE_MAX_POLL", 5*time.Second),
		FastPollLead:       durEnv("SPOTVEE_FAST_POLL_LEAD", 10*time.Second),
		PaletteCycle:       boolEnv("SPOTVEE_PALETTE_CYCLE", false),
		ImageCacheEntries:  intEnv("SPOTVEE_IMAGE_CACHE_ENTRIES", 256),
		ImageCacheBytes:    int64Env("SPOTVEE_IMAGE_CACHE_BYTES", 64<<20),
		MaxArtBytes:        int64Env("SPOTVEE_MAX_ART_BYTES", 8<<20),
		PaletteSize:        intEnv("SPOTVEE_PALETTE_SIZE", 4),
		Quantizer:          getenv("SPOTVEE_QUANTIZER", "mediancut"),
		SatFloor:           floatEnv("SPOTVEE_SAT_FLOOR", 0.18),
		LumHigh:            floatEnv("SPOTVEE_LUM_HIGH", 0.86),
		LumLow:             floatEnv("SPOTVEE_LUM_LOW", 0.12),
		VividWeight:        floatEnv("SPOTVEE_VIVID_WEIGHT", 0.7),
		HueSpread:          floatEnv("SPOTVEE_HUE_SPREAD", 0.22),
		VividFlip:          boolEnv("SPOTVEE_VIVID_FLIP", false),
		VividBackground:    floatEnv("SPOTVEE_VIVID_BACKGROUND", 0.55),
		LogLevel:           getenv("SPOTVEE_LOG_LEVEL", "info"),
		MetricsExpose:      boolEnv("SPOTVEE_METRICS", true),
	}

	if c.SpotifyClientID == "" {
		return c, errors.New("SPOTIFY_CLIENT_ID is required")
	}
	// SpotifyClientSecret is optional: with PKCE the token exchange only
	// needs the client ID. When set (client-credentials style apps) it is
	// used in the exchange.
	if c.GoveeController != "govee" {
		return c, fmt.Errorf("unsupported SPOTVEE_GOVEE_CONTROLLER %q", c.GoveeController)
	}
	if c.MaxArtBytes < 1<<10 {
		return c, errors.New("SPOTVEE_MAX_ART_BYTES must be at least 1 KiB")
	}
	if c.PaletteSize < 2 || c.PaletteSize > 16 {
		return c, errors.New("SPOTVEE_PALETTE_SIZE must be between 2 and 16")
	}
	if c.GoveeAPIKey == "" {
		return c, errors.New("GOVEE_API_KEY is required")
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func boolEnv(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func intEnv(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func int64Env(k string, def int64) int64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func floatEnv(k string, def float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func durEnv(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
