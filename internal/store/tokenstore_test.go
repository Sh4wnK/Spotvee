package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := NewTokenLocker(filepath.Join(dir, "key"), filepath.Join(dir, "state.json"))
	secret := []byte("{\"access_token\":\"abc\",\"refresh_token\":\"def\"}")
	if err := l.Save(secret); err != nil {
		t.Fatalf("save: %v", err)
	}

	// File perms must be 0600.
	info, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state perms = %o, want 600", perm)
	}
	ki, _ := os.Stat(filepath.Join(dir, "key"))
	if perm := ki.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key perms = %o, want 600", perm)
	}

	// Reload (fresh locker) decrypts what we saved.
	l2 := NewTokenLocker(filepath.Join(dir, "key"), filepath.Join(dir, "state.json"))
	got, err := l2.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round trip mismatch: %s vs %s", got, secret)
	}
}

func TestMissingFileIsNotExist(t *testing.T) {
	l := NewTokenLocker("/nonexistent/key", "/nonexistent/state.json")
	if _, err := l.Load(); !os.IsNotExist(err) {
		t.Fatalf("want IsNotExist, got %v", err)
	}
}

func TestKeyNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	l := NewTokenLocker(keyPath, filepath.Join(dir, "state.json"))
	if err := l.Save([]byte("first")); err != nil {
		t.Fatalf("save1: %v", err)
	}
	first, _ := os.ReadFile(keyPath)

	// A second save must reuse the existing key (same ciphertext-able key).
	if err := l.Save([]byte("second")); err != nil {
		t.Fatalf("save2: %v", err)
	}
	second, _ := os.ReadFile(keyPath)
	if !bytes.Equal(first, second) {
		t.Fatal("key file was overwritten; must be stable once created")
	}
}

func TestSaveJSONLoadJSON(t *testing.T) {
	dir := t.TempDir()
	l := NewTokenLocker(filepath.Join(dir, "key"), filepath.Join(dir, "state.json"))
	type tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	want := tok{AccessToken: "abc", RefreshToken: "xyz"}
	if err := l.SaveJSON(&want); err != nil {
		t.Fatalf("save: %v", err)
	}
	l2 := NewTokenLocker(filepath.Join(dir, "key"), filepath.Join(dir, "state.json"))
	var got tok
	if err := l2.LoadJSON(&got); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
