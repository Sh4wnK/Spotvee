// Package devices defines the light controller abstraction the sync engine
// drives. The Govee implementation lives in the adapter at the repo root;
// tests use in-memory fakes.
package devices

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Color is an RGB triplet (0-255 each).
type Color struct {
	R, G, B uint8
}

// Hex returns the color as a lowercase hex string.
func (c Color) Hex() string {
	return fmt.Sprintf("%02x%02x%02x", c.R, c.G, c.B)
}

// Device is a controllable light.
type Device struct {
	SKU  string
	ID   string
	Name string
}

// Controller is the interface Spotvee uses to talk to lights. It mirrors the
// upstream Govee wrapper's contract API (Devices, SetPower, SetBrightness,
// SetColorRGB) so the two stay in sync.
type Controller interface {
	Devices(ctx context.Context) ([]Device, error)
	SetPower(ctx context.Context, sku, id string, on bool) error
	SetBrightness(ctx context.Context, sku, id string, percent int) error
	SetColorRGB(ctx context.Context, sku, id string, c Color) error
}

// ManagedDevice pairs a Device with its per-device circuit breaker.
type ManagedDevice struct {
	Device
	cb *CircuitBreaker
}

// CircuitBreaker short-circuits a persistently failing device so one bad
// light can never stall the sync loop. Failures move the breaker through
// closed -> open -> half-open; after cooldown a probe request is allowed
// through to test recovery.
type CircuitBreaker struct {
	mu        sync.Mutex
	failures  int
	state     int // 0 closed, 1 open, 2 half-open
	openedAt  time.Time
	threshold int
	cooldown  time.Duration
}

const (
	stateClosed = iota
	stateOpen
	stateHalfOpen
)

// BreakerOptions configure a CircuitBreaker.
type BreakerOptions struct {
	// Threshold is the consecutive-failure count that opens the breaker.
	Threshold int
	// Cooldown is how long the breaker stays open before probing.
	Cooldown time.Duration
}

// DefaultBreakerOptions returns sane long-run defaults.
func DefaultBreakerOptions() BreakerOptions {
	return BreakerOptions{Threshold: 3, Cooldown: 30 * time.Second}
}

// NewBreaker builds a closed breaker.
func NewBreaker(o BreakerOptions) *CircuitBreaker {
	if o.Threshold <= 0 {
		o.Threshold = 3
	}
	if o.Cooldown <= 0 {
		o.Cooldown = 30 * time.Second
	}
	return &CircuitBreaker{threshold: o.Threshold, cooldown: o.Cooldown, state: stateClosed}
}

// Allow reports whether a new request may proceed. When the breaker is open
// it reports false until the cooldown elapses, at which point it transitions
// to half-open and allows exactly one probe through.
func (cb *CircuitBreaker) Allow(now time.Time) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case stateClosed:
		return true
	case stateHalfOpen:
		// Only one probe may run at a time.
		return false
	case stateOpen:
		if now.Sub(cb.openedAt) >= cb.cooldown {
			cb.state = stateHalfOpen
			return true
		}
		return false
	}
	return false
}

// Success records a successful call, closing the breaker.
func (cb *CircuitBreaker) Success() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.state = stateClosed
}

// Failure records a failed call, opening the breaker once the threshold is
// reached. In half-open state a single failure re-opens it immediately.
func (cb *CircuitBreaker) Failure(now time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	if cb.state == stateHalfOpen || cb.failures >= cb.threshold {
		cb.state = stateOpen
		cb.openedAt = now
	}
}

// State returns a human-readable breaker state.
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case stateClosed:
		return "closed"
	case stateHalfOpen:
		return "half-open"
	default:
		return "open"
	}
}
