package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/spotify"
	"github.com/Sh4wnK/Spotvee/internal/store"
)

// fakeClient records exchanges and swaps in a token.
type fakeClient struct {
	exchanged bool
}

func (f *fakeClient) Exchange(ctx context.Context, code, verifier string) (spotify.Token, error) {
	f.exchanged = true
	return spotify.Token{
		AccessToken:  "acc-" + code,
		RefreshToken: "ref-" + code,
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
		Scope:        spotify.ScopePlaying,
	}, nil
}

func (f *fakeClient) AuthURL(state, verifier string) string {
	return "https://accounts.spotify.com/authorize?state=" + state
}

func (f *fakeClient) SetToken(t spotify.Token)          {}
func (f *fakeClient) GetToken() spotify.Token           { return spotify.Token{} }
func (f *fakeClient) Refresh(ctx context.Context) error { return nil }

func TestLoginExchangePersists(t *testing.T) {
	dir := t.TempDir()
	locker := store.NewTokenLocker(filepath.Join(dir, "key"), filepath.Join(dir, "state"))
	fc := &fakeClient{}
	fl, err := New(Options{Client: fc, Locker: locker})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if fl.Authenticated() {
		t.Fatal("fresh flow must not be authenticated")
	}
	url := fl.LoginURL()
	if !strings.Contains(url, "state=") {
		t.Fatalf("login url %q missing state", url)
	}
	state := url[strings.Index(url, "state=")+len("state="):]
	if err := fl.Exchange(context.Background(), state, "code-1"); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if !fl.Authenticated() {
		t.Fatal("should be authenticated after exchange")
	}
	if !fc.exchanged {
		t.Fatal("client exchange not called")
	}

	// A fresh flow over the same locker restores the session.
	fl2, err := New(Options{Client: fc, Locker: locker})
	if err != nil {
		t.Fatalf("new2: %v", err)
	}
	if !fl2.Authenticated() {
		t.Fatal("persisted session should restore authenticated=true")
	}
}

func TestExchangeWithWrongStateFails(t *testing.T) {
	dir := t.TempDir()
	locker := store.NewTokenLocker(filepath.Join(dir, "key"), filepath.Join(dir, "state2"))
	fl, err := New(Options{Client: &fakeClient{}, Locker: locker})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	_ = fl.LoginURL()
	if err := fl.Exchange(context.Background(), "wrong-state", "code-x"); err == nil {
		t.Fatal("expected error for unknown state")
	}
	if fl.Authenticated() {
		t.Fatal("must stay unauthenticated after failed exchange")
	}
}
