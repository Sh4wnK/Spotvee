package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer exercises the client against canned responses.
type fakeServer struct {
	mu         sync.Mutex
	cp         *CurrentlyPlaying
	cpStatus   int
	pollCount  int
	tokenCalls int
	// optionally refuse the first N polls with 401
	fail401 int
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokenCalls++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"scope":        ScopePlaying,
		})
	})
	mux.HandleFunc("/me/player/currently-playing", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.pollCount++
		if f.pollCount <= f.fail401 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.cpStatus == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(f.cp)
	})
	return mux
}

func testClient(t *testing.T, srv *httptest.Server) *Client {
	c, err := NewClient(Options{
		ClientID:     "cid",
		ClientSecret: "csecret",
		RedirectURI:  "http://localhost/cb",
		AuthURL:      srv.URL + "/authorize",
		TokenURL:     srv.URL + "/api/token",
		APIURL:       srv.URL,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func TestAuthURLLeadsToSpotify(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()
	c := testClient(t, srv)
	u := c.AuthURL("state-1", "verif-1")
	if !strings.Contains(u, "client_id=cid") {
		t.Fatalf("url missing client_id: %s", u)
	}
	if !strings.Contains(u, "code_challenge_method=S256") {
		t.Fatalf("url missing pkce: %s", u)
	}
	if !strings.Contains(u, "state=state-1") {
		t.Fatalf("url missing state: %s", u)
	}
}

func TestExchangePKCE(t *testing.T) {
	srv := &fakeServer{}
	hs := httptest.NewServer(srv.handler(t))
	defer hs.Close()
	c := testClient(t, hs)

	tok, err := c.Exchange(context.Background(), "code-1", "veryLongVerifierStringForSHA256Challenge")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tok.AccessToken != "new-access" {
		t.Fatalf("access token = %q", tok.AccessToken)
	}
	if tok.Expired() {
		t.Fatal("fresh token should not be expired")
	}
}

func TestCurrentlyPlaying(t *testing.T) {
	srv := &fakeServer{
		cp: &CurrentlyPlaying{
			ProgressMS: 42000,
			IsPlaying:  true,
			Item: &Track{
				ID:         "track-1",
				Name:       "Song",
				DurationMS: 180000,
				Artists:    []Artist{{Name: "Artist A"}},
				Album:      TrackAlbum{Name: "Album", Images: []AlbumImage{{URL: "https://i.scdn.co/art1", Width: 640}}},
			},
		},
	}
	hs := httptest.NewServer(srv.handler(t))
	defer hs.Close()
	c := testClient(t, hs)
	c.SetToken(Token{AccessToken: "tok", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})

	cp, err := c.CurrentlyPlaying(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if cp.Item.ID != "track-1" || cp.Item.Album.Images[0].URL == "" {
		t.Fatalf("unexpected cp: %+v", cp)
	}
}

func TestCurrentlyPlaying204(t *testing.T) {
	srv := &fakeServer{cpStatus: http.StatusNoContent}
	hs := httptest.NewServer(srv.handler(t))
	defer hs.Close()
	c := testClient(t, hs)
	c.SetToken(Token{AccessToken: "tok", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})

	cp, err := c.CurrentlyPlaying(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if cp != nil {
		t.Fatalf("want nil for 204, got %+v", cp)
	}
}

func TestAutoRefreshOn401(t *testing.T) {
	srv := &fakeServer{
		cp: &CurrentlyPlaying{
			ProgressMS: 0,
			IsPlaying:  true,
			Item:       &Track{ID: "t", Name: "n", DurationMS: 60000, Album: TrackAlbum{Name: "a"}},
		},
		fail401: 1, // first poll 401 -> triggers refresh, then succeeds
	}
	hs := httptest.NewServer(srv.handler(t))
	defer hs.Close()
	c := testClient(t, hs)
	c.SetToken(Token{AccessToken: "old", RefreshToken: "refresh-1", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})

	cp, err := c.CurrentlyPlaying(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if cp == nil || cp.Item.ID != "t" {
		t.Fatalf("unexpected cp: %+v", cp)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.tokenCalls == 0 {
		t.Fatal("expected token refresh on 401")
	}
	if got := c.GetToken().AccessToken; got != "new-access" {
		t.Fatalf("access token after refresh = %q, want new-access", got)
	}
}

func TestRateLimitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := testClient(t, srv)
	c.SetToken(Token{AccessToken: "tok", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})

	_, err := c.CurrentlyPlaying(context.Background())
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	var rle *RateLimitError
	if !asRateLimit(err, &rle) {
		t.Fatalf("error is %T, want *RateLimitError", err)
	}
	if rle.RetryAfter != "7" {
		t.Fatalf("retry-after = %q", rle.RetryAfter)
	}
}

func asRateLimit(err error, out **RateLimitError) bool {
	rl, ok := err.(*RateLimitError)
	if ok {
		*out = rl
	}
	return ok
}

func TestUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()
	c := testClient(t, srv)
	if _, err := c.CurrentlyPlaying(context.Background()); err == nil {
		t.Fatal("expected error without token")
	}
}
