// Package store persists runtime state: the Spotify token (encrypted at
// rest) and small cache support.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// TokenLocker encrypts a Spotify token JSON blob with AES-256-GCM using a key
// held in a separate 0600 file, and persists the ciphertext in a 0600 data
// file. The key file is created with 0600 perms and never overwritten if it
// already exists.
type TokenLocker struct {
	keyFile   string
	stateFile string
}

// NewTokenLocker returns a locker for the given state and key paths.
func NewTokenLocker(keyFile, stateFile string) *TokenLocker {
	return &TokenLocker{keyFile: keyFile, stateFile: stateFile}
}

// Save encrypts data and writes it atomically (temp file + rename).
func (l *TokenLocker) Save(data []byte) error {
	key, err := l.ensureKey()
	if err != nil {
		return err
	}
	ct, err := encrypt(key, data)
	if err != nil {
		return fmt.Errorf("store: encrypt token: %w", err)
	}
	if err := writeFile0600(l.stateFile, ct); err != nil {
		return fmt.Errorf("store: persist token: %w", err)
	}
	return nil
}

// Load decrypts and returns the previously stored bytes. It returns
// os.IsNotExist errors unchanged so callers can treat a missing file as
// "never authenticated".
func (l *TokenLocker) Load() ([]byte, error) {
	ct, err := os.ReadFile(l.stateFile)
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(l.keyFile)
	if err != nil {
		return nil, fmt.Errorf("store: read key file: %w", err)
	}
	pt, err := decrypt(key, ct)
	if err != nil {
		return nil, fmt.Errorf("store: decrypt token: %w", err)
	}
	return pt, nil
}

// Has reports whether state exists on disk (even if not yet loaded).
func (l *TokenLocker) Has() bool {
	_, err := os.Stat(l.stateFile)
	return err == nil
}

func (l *TokenLocker) ensureKey() ([]byte, error) {
	if key, err := os.ReadFile(l.keyFile); err == nil && len(key) == 32 {
		return key, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("store: generate key: %w", err)
	}
	if err := writeFile0600(l.keyFile, key); err != nil {
		return nil, fmt.Errorf("store: persist key: %w", err)
	}
	return key, nil
}

func writeFile0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(key, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ct) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, body := ct[:gcm.NonceSize()], ct[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}

// SaveJSON is a convenience wrapper around Save for arbitrary structs.
func (l *TokenLocker) SaveJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return l.Save(b)
}

// LoadJSON loads and unmarshals state; a missing file yields os.ErrNotExist.
func (l *TokenLocker) LoadJSON(v any) error {
	b, err := l.Load()
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
