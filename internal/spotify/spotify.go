package spotify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Scopes requested at authorization time.
const (
	ScopePlaying    = "user-read-currently-playing"
	ScopePlayback   = "user-read-playback-state"
	defaultAuthURL  = "https://accounts.spotify.com/authorize"
	defaultTokenURL = "https://accounts.spotify.com/api/token"
	defaultAPIURL   = "https://api.spotify.com/v1"
)

// Token is the OAuth2 token bundle persisted across restarts.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
	Scope        string    `json:"scope"`
}

// Expired reports whether the access token is at or past its expiry.
func (t Token) Expired() bool {
	return !t.Expiry.After(time.Now())
}

// Options configures the Spotify API client.
type Options struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	AuthURL      string // defaults to Spotify's authorize endpoint
	TokenURL     string // defaults to Spotify's token endpoint
	APIURL       string // defaults to Spotify's API base
	HTTPClient   *http.Client
}

// Client is a Spotify Web API client with PKCE + refresh-token flows.
type Client struct {
	opts        Options
	http        *http.Client
	mu          sync.Mutex
	token       Token
	refreshing  bool
	refreshErr  error
	refreshDone chan struct{}
}

// NewClient constructs a Client. It performs no network I/O.
func NewClient(o Options) (*Client, error) {
	if o.ClientID == "" {
		return nil, errors.New("spotify: ClientID is required")
	}
	if o.RedirectURI == "" {
		return nil, errors.New("spotify: RedirectURI is required")
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if o.AuthURL == "" {
		o.AuthURL = defaultAuthURL
	}
	if o.TokenURL == "" {
		o.TokenURL = defaultTokenURL
	}
	if o.APIURL == "" {
		o.APIURL = defaultAPIURL
	}
	return &Client{opts: o, http: o.HTTPClient}, nil
}

// PKCEVerifier creates a new verifier/challenge pair (S256).
func PKCEVerifier() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("spotify: generate verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// AuthURL builds the Spotify authorization URL with the given state,
// verifier and code_challenge (S256).
func (c *Client) AuthURL(state, verifier string) string {
	u, _ := url.Parse(c.opts.AuthURL)
	q := u.Query()
	q.Set("client_id", c.opts.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", c.opts.RedirectURI)
	q.Set("scope", ScopePlaying+" "+ScopePlayback)
	q.Set("state", state)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", challengeFromVerifier(verifier))
	q.Set("show_dialog", "false")
	u.RawQuery = q.Encode()
	return u.String()
}

func challengeFromVerifier(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Exchange swaps an authorization code for tokens using PKCE.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.opts.RedirectURI)
	form.Set("client_id", c.opts.ClientID)
	form.Set("code_verifier", verifier)
	if c.opts.ClientSecret != "" {
		form.Set("client_secret", c.opts.ClientSecret)
	}
	var t Token
	if err := c.tokenRequest(ctx, form, &t); err != nil {
		return Token{}, err
	}
	c.mu.Lock()
	c.token = t
	c.mu.Unlock()
	return t, nil
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values, out *Token) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("spotify: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("spotify: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("spotify: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("spotify: token endpoint %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("spotify: parse token response: %w", err)
	}
	expiry := time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	// Refresh a few seconds early to avoid boundary races.
	expiry = expiry.Add(-10 * time.Second)
	out.AccessToken = raw.AccessToken
	out.RefreshToken = raw.RefreshToken
	out.TokenType = raw.TokenType
	out.Expiry = expiry
	out.Scope = raw.Scope
	return nil
}

// GetToken returns the current token (no network).
func (c *Client) GetToken() Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// SetToken installs a previously persisted token. If it is expired and has a
// refresh token it is refreshed lazily on the next API call.
func (c *Client) SetToken(t Token) {
	c.mu.Lock()
	c.token = t
	c.mu.Unlock()
}

// Refresh refreshes the access token, coalescing concurrent callers.
func (c *Client) Refresh(ctx context.Context) error {
	c.mu.Lock()
	if c.refreshing {
		done := c.refreshDone
		c.mu.Unlock()
		select {
		case <-done:
			return c.refreshErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.refreshing = true
	c.refreshDone = make(chan struct{})
	c.refreshErr = nil
	c.mu.Unlock()

	refreshToken := ""
	c.mu.Lock()
	refreshToken = c.token.RefreshToken
	c.mu.Unlock()
	if refreshToken == "" {
		c.finishRefresh(errors.New("spotify: no refresh token; re-authenticate"))
		return c.refreshErr
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", c.opts.ClientID)
	if c.opts.ClientSecret != "" {
		form.Set("client_secret", c.opts.ClientSecret)
	}
	var t Token
	err := c.tokenRequest(ctx, form, &t)
	if err == nil {
		t.RefreshToken = refreshToken // keep the existing refresh token if the response omits it
		c.mu.Lock()
		c.token = t
		c.mu.Unlock()
	}
	c.finishRefresh(err)
	return err
}

func (c *Client) finishRefresh(err error) {
	c.mu.Lock()
	c.refreshErr = err
	close(c.refreshDone)
	c.refreshing = false
	c.mu.Unlock()
}

// doJSON performs an authenticated request, refreshing the token once on a
// 401 before giving up.
func (c *Client) doJSON(ctx context.Context, method, path string, out any) error {
	for attempt := range 2 {
		tok := c.GetToken()
		if tok.AccessToken == "" {
			return errors.New("spotify: not authenticated")
		}
		if tok.Expired() {
			if err := c.Refresh(ctx); err != nil {
				return err
			}
			tok = c.GetToken()
		}
		req, err := http.NewRequestWithContext(ctx, method, c.opts.APIURL+path, nil)
		if err != nil {
			return fmt.Errorf("spotify: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("spotify: request %s: %w", path, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("spotify: read %s: %w", path, readErr)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_ = c.Refresh(ctx)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: string(body)}
		}
		if out != nil {
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("spotify: parse %s: %w", path, err)
			}
		}
		return nil
	}
	return errors.New("spotify: unreachable")
}

// APIError is returned for non-200 Spotify responses.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("spotify: %s %s: %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// CurrentlyPlaying is the current playback + track summary.
type CurrentlyPlaying struct {
	Timestamp  int64  `json:"timestamp"`
	ProgressMS int64  `json:"progress_ms"`
	IsPlaying  bool   `json:"is_playing"`
	Item       *Track `json:"item"`
}

// Track is the subset of the Spotify track object Spotvee consumes.
type Track struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	DurationMS int64      `json:"duration_ms"`
	Artists    []Artist   `json:"artists"`
	Album      TrackAlbum `json:"album"`
}

// Artist is a track artist.
type Artist struct {
	Name string `json:"name"`
}

// TrackAlbum is the album subset with artwork URLs.
type TrackAlbum struct {
	Name   string       `json:"name"`
	Images []AlbumImage `json:"images"`
}

// AlbumImage is one artwork size variant.
type AlbumImage struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// CurrentlyPlaying fetches GET /v1/me/player/currently-playing.
// A 204 (no current track) is reported as (nil, nil).
func (c *Client) CurrentlyPlaying(ctx context.Context) (*CurrentlyPlaying, error) {
	tok := c.GetToken()
	if tok.AccessToken == "" {
		return nil, errors.New("spotify: not authenticated")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.APIURL+"/me/player/currently-playing", nil)
	if err != nil {
		return nil, fmt.Errorf("spotify: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify: request currently-playing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusUnauthorized:
		if err := c.Refresh(ctx); err != nil {
			return nil, err
		}
		return c.currentlyPlaying(ctx, 1)
	case http.StatusTooManyRequests:
		retry := resp.Header.Get("Retry-After")
		return nil, &RateLimitError{RetryAfter: retry}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode != http.StatusOK {
			return nil, &APIError{Method: http.MethodGet, Path: "/me/player/currently-playing", Status: resp.StatusCode, Body: string(body)}
		}
		var cp CurrentlyPlaying
		if err := json.Unmarshal(body, &cp); err != nil {
			return nil, fmt.Errorf("spotify: parse currently-playing: %w", err)
		}
		return &cp, nil
	}
}

// currentlyPlaying is the bounded-retry core of CurrentlyPlaying.
func (c *Client) currentlyPlaying(ctx context.Context, retries int) (*CurrentlyPlaying, error) {
	tok := c.GetToken()
	if tok.AccessToken == "" {
		return nil, errors.New("spotify: not authenticated")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.APIURL+"/me/player/currently-playing", nil)
	if err != nil {
		return nil, fmt.Errorf("spotify: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify: request currently-playing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusUnauthorized:
		if retries <= 0 {
			return nil, &APIError{Method: http.MethodGet, Path: "/me/player/currently-playing", Status: resp.StatusCode}
		}
		if err := c.Refresh(ctx); err != nil {
			return nil, err
		}
		return c.currentlyPlaying(ctx, retries-1)
	case http.StatusTooManyRequests:
		retry := resp.Header.Get("Retry-After")
		return nil, &RateLimitError{RetryAfter: retry}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode != http.StatusOK {
			return nil, &APIError{Method: http.MethodGet, Path: "/me/player/currently-playing", Status: resp.StatusCode, Body: string(body)}
		}
		var cp CurrentlyPlaying
		if err := json.Unmarshal(body, &cp); err != nil {
			return nil, fmt.Errorf("spotify: parse currently-playing: %w", err)
		}
		return &cp, nil
	}
}

// RateLimitError carries the Retry-After header value (may be empty).
type RateLimitError struct {
	RetryAfter string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter != "" {
		return "spotify: rate limited (retry-after " + e.RetryAfter + "s)"
	}
	return "spotify: rate limited"
}

// IsRateLimit reports whether err is a *RateLimitError.
func IsRateLimit(err error) bool {
	var rle *RateLimitError
	return errors.As(err, &rle)
}
