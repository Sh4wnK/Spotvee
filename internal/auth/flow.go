// Package auth implements the Spotify OAuth2 Authorization Code + PKCE flow
// with encrypted-at-rest token persistence and automatic refresh.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/spotify"
	"github.com/Sh4wnK/Spotvee/internal/store"
)

// Pending holds an in-flight authorization request (state <-> verifier).
type Pending struct {
	State    string
	Verifier string
	Created  time.Time
}

// oauthClient is the subset of *spotify.Client the flow uses.
type oauthClient interface {
	AuthURL(state, verifier string) string
	Exchange(ctx context.Context, code, verifier string) (spotify.Token, error)
	SetToken(spotify.Token)
	GetToken() spotify.Token
	Refresh(ctx context.Context) error
}

// Flow implements api.AuthFlow by wrapping the Spotify client.
type Flow struct {
	log    *slog.Logger
	client oauthClient
	locker *store.TokenLocker
	now    func() time.Time

	mu      sync.Mutex
	pending map[string]Pending
	authd   bool
}

// Options configure a Flow.
type Options struct {
	Client oauthClient
	Locker *store.TokenLocker
	Logger *slog.Logger
}

// New builds a Flow and loads any persisted token. It performs no network
// I/O. A missing persistence file simply means "not yet authenticated".
func New(o Options) (*Flow, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	f := &Flow{
		log:     o.Logger,
		client:  o.Client,
		locker:  o.Locker,
		now:     time.Now,
		pending: make(map[string]Pending),
	}
	if o.Locker != nil && o.Locker.Has() {
		var tok spotify.Token
		if err := o.Locker.LoadJSON(&tok); err != nil {
			o.Logger.Warn("failed to load persisted token; re-authentication required", "err", err)
		} else if tok.AccessToken != "" {
			f.client.SetToken(tok)
			f.authd = true
		}
	}
	return f, nil
}

// Authenticated reports whether a Spotify session exists.
func (f *Flow) Authenticated() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authd
}

// LoginURL starts a new authorization request and returns the Spotify
// authorization URL.
func (f *Flow) LoginURL() string {
	verifier, _, err := spotify.PKCEVerifier()
	if err != nil {
		f.log.Error("failed to generate pkce verifier", "err", err)
		return ""
	}
	state := randomHex(16)
	f.mu.Lock()
	// Prune stale pending entries so the map stays bounded.
	for k, p := range f.pending {
		if f.now().Sub(p.Created) > 10*time.Minute {
			delete(f.pending, k)
		}
	}
	f.pending[state] = Pending{State: state, Verifier: verifier, Created: f.now()}
	f.mu.Unlock()
	return f.client.AuthURL(state, verifier)
}

// Exchange completes the callback: it validates the state, exchanges the
// authorization code (PKCE) and persists the resulting token.
func (f *Flow) Exchange(ctx context.Context, state, code string) error {
	f.mu.Lock()
	p, ok := f.pending[state]
	delete(f.pending, state)
	f.mu.Unlock()
	if !ok {
		return errors.New("auth: unknown or expired state")
	}
	tok, err := f.client.Exchange(ctx, code, p.Verifier)
	if err != nil {
		return err
	}
	if f.locker != nil {
		if err := f.locker.SaveJSON(&tok); err != nil {
			f.log.Warn("failed to persist token", "err", err)
		}
	}
	f.mu.Lock()
	f.authd = true
	f.mu.Unlock()
	return nil
}

// Refresh forces a token refresh (used by tests and operators). It is also
// triggered automatically by the API client on 401/expiry.
func (f *Flow) Refresh(ctx context.Context) error {
	return f.client.Refresh(ctx)
}

// Token returns the current token.
func (f *Flow) Token() spotify.Token { return f.client.GetToken() }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return time.Now().Format("150405.000000000")
	}
	return hex.EncodeToString(b)
}
