// Package art fetches and caches album-artwork images.
package art

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/store"
)

// Fetcher downloads images from URLs with a bounded LRU byte cache. All
// fetches carry a context so shutdown and per-request deadlines propagate.
type Fetcher struct {
	http      *http.Client
	cache     *store.LRU[[]byte]
	maxBytes  int64
	userAgent string
}

// Options configures a Fetcher.
type Options struct {
	HTTPClient    *http.Client
	MaxEntries    int
	MaxBytes      int64
	MaxImageBytes int64
	UserAgent     string
}

// New builds a Fetcher.
func New(o Options) *Fetcher {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 128
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 32 << 20
	}
	if o.MaxImageBytes <= 0 {
		o.MaxImageBytes = 8 << 20
	}
	if o.UserAgent == "" {
		o.UserAgent = "Spotvee/1.0"
	}
	return &Fetcher{
		http:      o.HTTPClient,
		cache:     store.NewLRU[[]byte](o.MaxEntries, o.MaxBytes),
		maxBytes:  o.MaxImageBytes,
		userAgent: o.UserAgent,
	}
}

// Fetch returns the image bytes for rawURL, using the cache when possible.
// The URL is validated and normalized; failures short-circuit without
// poisoning the cache.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("art: empty url")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("art: invalid url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("art: unsupported scheme %q", u.Scheme)
	}
	key := u.String()
	if cached, ok := f.cache.Get(key); ok {
		return cached, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, fmt.Errorf("art: build request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("art: fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("art: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("art: read body: %w", err)
	}
	if int64(len(body)) > f.maxBytes {
		return nil, fmt.Errorf("art: image exceeds %d bytes", f.maxBytes)
	}
	f.cache.Set(key, body, int64(len(body)))
	return body, nil
}

// CacheStats returns current cache utilization.
func (f *Fetcher) CacheStats() (entries int, bytes int64) {
	return f.cache.Len(), f.cache.Bytes()
}
